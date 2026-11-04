package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"homebox/internal/httpapi"
	"homebox/internal/store"
)

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) (finalErr error) {
	flags := flag.NewFlagSet("homebox serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("listen", "127.0.0.1:8080", "HTTP(S) listen address; non-loopback requires TLS or an explicit trusted proxy")
	database := flags.String("database", "data/homebox.sqlite", "Previously migrated local SQLite database")
	webDir := flags.String("web-dir", "web/dist", "Built public frontend files; never use the private data directory")
	origin := flags.String("public-origin", "", "Fixed browser origin; required for non-loopback, otherwise derived from the listener")
	certFile := flags.String("tls-cert", "", "PEM certificate chain for direct HTTPS; requires --tls-key")
	keyFile := flags.String("tls-key", "", "Private PEM key for direct HTTPS; requires --tls-cert")
	var proxies []netip.Prefix
	flags.Func("trusted-proxy", "Trusted reverse-proxy IP prefix (repeatable); proxy must preserve Host and overwrite forwarding headers", func(value string) error {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Bits() == 0 {
			return errors.New("trusted proxy must be an explicit IP prefix, not a wildcard")
		}
		proxies = append(proxies, prefix.Masked())
		return nil
	})
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments after serve command")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if (*certFile == "") != (*keyFile == "") {
		return errors.New("direct HTTPS requires both --tls-cert and --tls-key")
	}
	var certificates []tls.Certificate
	if *certFile != "" {
		pair, err := tls.LoadX509KeyPair(*certFile, *keyFile)
		if err != nil {
			return err
		}
		certificates = []tls.Certificate{pair}
	}
	root, err := os.OpenRoot(*webDir)
	if err != nil {
		return err
	}
	defer func() { finalErr = errors.Join(finalErr, root.Close()) }()
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	defer listener.Close()
	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return err
	}
	local, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if *origin == "" {
		if !local.IsLoopback() {
			return errors.New("non-loopback serving requires an explicit --public-origin")
		}
		scheme := "http"
		if len(certificates) != 0 {
			scheme = "https"
		}
		*origin = scheme + "://" + listener.Addr().String()
	}
	if len(certificates) != 0 && !strings.HasPrefix(*origin, "https://") || len(proxies) != 0 && !strings.HasPrefix(*origin, "https://") {
		return errors.New("TLS and trusted-proxy deployments require an HTTPS public origin")
	}
	if strings.HasPrefix(*origin, "https://") && len(certificates) == 0 && len(proxies) == 0 {
		return errors.New("HTTPS origin requires direct TLS or an explicit trusted reverse proxy")
	}
	if !local.IsLoopback() && len(certificates) == 0 && len(proxies) == 0 {
		return errors.New("plaintext serving without a trusted proxy is restricted to loopback")
	}
	db, err := store.Open(ctx, store.Config{Path: *database})
	if err != nil {
		return err
	}
	defer func() { finalErr = errors.Join(finalErr, db.Close()) }()
	auth, err := db.NewAuth(store.AuthConfig{})
	if err != nil {
		return err
	}
	handler, err := httpapi.New(httpapi.Config{Assets: root.FS(), Auth: auth, PublicOrigin: *origin, TrustedProxies: proxies})
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: certificates},
	}
	defer server.Close()
	if err = json.NewEncoder(stdout).Encode(map[string]string{
		"listening": listener.Addr().String(), "public_origin": *origin, "version": version, "build_id": buildID,
	}); err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() {
		if len(certificates) != 0 {
			result <- server.ServeTLS(listener, "", "")
		} else {
			result <- server.Serve(listener)
		}
	}()
	select {
	case err = <-result:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			return errors.Join(err, server.Close())
		}
		err = <-result
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
