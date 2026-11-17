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
	"path/filepath"
	"strings"
	"time"

	"homebox/internal/httpapi"
	"homebox/internal/store"

	"golang.org/x/sys/unix"
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
	if err = requirePrivateWebSeparation(root, *webDir, *database, *keyFile); err != nil {
		return err
	}
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
	drain := newDrainingHandler(handler)
	server := &http.Server{
		Handler:           drain,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: certificates},
	}
	defer func() {
		drain.stop()
		_ = server.Close()
		drain.wait()
	}()
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
		drain.stop()
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

var errPrivateWebOverlap = errors.New("private application data overlaps the public frontend directory or has unsafe links")

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// optionalResolvedPath preserves an absent leaf but rejects dangling links.
func optionalResolvedPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return path, nil
		}
	}
	return "", err
}

func requireSinglePrivateLink(path string, required bool) error {
	var info unix.Stat_t
	err := unix.Lstat(path, &info)
	if errors.Is(err, unix.ENOENT) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 {
		return errPrivateWebOverlap
	}
	return nil
}

// requirePrivateWebSeparation rejects configured private-path overlaps and
// hard-linked SQLite and TLS key files. Operators must still keep other secrets,
// media/backup hard links and separately mounted aliases out of the web root.
func requirePrivateWebSeparation(root *os.Root, webDir, database, keyFile string) error {
	web, err := filepath.Abs(webDir)
	if err != nil {
		return err
	}
	web, err = filepath.EvalSymlinks(web)
	if err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	named, err := os.Stat(web)
	if err != nil || !os.SameFile(opened, named) {
		return errors.Join(errPrivateWebOverlap, err)
	}
	database, err = filepath.Abs(database)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(database))
	if err != nil {
		return err
	}
	database = filepath.Join(parent, filepath.Base(database))
	if pathWithin(web, database) {
		return errPrivateWebOverlap
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".lock"} {
		if err = requireSinglePrivateLink(database+suffix, suffix == ""); err != nil {
			return err
		}
	}
	for _, private := range []string{database + ".media", filepath.Join(parent, "migration-backups")} {
		resolved, err := optionalResolvedPath(private)
		if err != nil {
			return err
		}
		if pathWithin(web, private) || pathWithin(private, web) || pathWithin(web, resolved) || pathWithin(resolved, web) {
			return errPrivateWebOverlap
		}
	}
	if keyFile != "" {
		key, err := filepath.Abs(keyFile)
		if err != nil {
			return err
		}
		keyParent, err := filepath.EvalSymlinks(filepath.Dir(key))
		if err != nil {
			return err
		}
		key = filepath.Join(keyParent, filepath.Base(key))
		resolved, err := filepath.EvalSymlinks(key)
		if err != nil {
			return err
		}
		if pathWithin(web, key) || pathWithin(web, resolved) {
			return errPrivateWebOverlap
		}
		if err = requireSinglePrivateLink(resolved, true); err != nil {
			return err
		}
	}
	named, err = os.Stat(web)
	if err != nil || !os.SameFile(opened, named) {
		return errors.Join(errPrivateWebOverlap, err)
	}
	return nil
}
