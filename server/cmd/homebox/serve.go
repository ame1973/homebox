package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"homebox/internal/httpapi"
	"homebox/internal/store"
)

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) (finalErr error) {
	flags := flag.NewFlagSet("homebox serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("listen", "127.0.0.1:8080", "HTTP listen address; use a trusted HTTPS proxy for production")
	database := flags.String("database", "data/homebox.sqlite", "Previously migrated local SQLite database")
	webDir := flags.String("web-dir", "web/dist", "Built public frontend files; never use the private data directory")
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
	root, err := os.OpenRoot(*webDir)
	if err != nil {
		return err
	}
	defer func() { finalErr = errors.Join(finalErr, root.Close()) }()
	handler, err := httpapi.New(root.FS())
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, store.Config{Path: *database})
	if err != nil {
		return err
	}
	defer func() { finalErr = errors.Join(finalErr, db.Close()) }()
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	defer server.Close()
	if err = json.NewEncoder(stdout).Encode(map[string]string{
		"listening": listener.Addr().String(), "version": version, "build_id": buildID,
	}); err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
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
