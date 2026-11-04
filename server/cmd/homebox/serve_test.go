package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"homebox/internal/store"
)

type startupOutput chan []byte

func (out startupOutput) Write(data []byte) (int, error) {
	out <- append([]byte(nil), data...)
	return len(data), nil
}

func serverFixture(t *testing.T) (database, web string) {
	t.Helper()
	directory := privateDirectory(t)
	database = filepath.Join(directory, "homebox.sqlite")
	if _, err := invoke(t, "db", "migrate", "--database", database); err != nil {
		t.Fatal(err)
	}
	web = filepath.Join(directory, "web")
	if err := os.Mkdir(web, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>Test fixture</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return database, web
}

func TestServeActualSocketAndGracefulShutdownReleaseDatabase(t *testing.T) {
	database, web := serverFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := make(startupOutput, 1)
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, []string{"serve", "--listen", "127.0.0.1:0", "--database", database, "--web-dir", web}, output, io.Discard)
	}()
	var ready map[string]string
	select {
	case data := <-output:
		if err := json.Unmarshal(data, &ready); err != nil {
			t.Fatal(err)
		}
	case err := <-result:
		t.Fatalf("server exited before startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + ready["listening"] + "/api/v1/health/live")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]string
	err = json.NewDecoder(response.Body).Decode(&health)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || health["status"] != "ok" {
		t.Fatalf("real HTTP liveness failed: %v, %v", health, err)
	}
	if _, err = store.Open(t.Context(), store.Config{Path: database}); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("server did not retain database ownership: %v", err)
	}
	cancel()
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	if _, err = invoke(t, "db", "check", "--database", database); err != nil {
		t.Fatalf("shutdown leaked database ownership: %v", err)
	}
}

func TestServeRejectsMissingBuildInvalidFlagsAndUnmigratedData(t *testing.T) {
	database, web := serverFixture(t)
	for _, args := range [][]string{
		{"--unknown"}, {"extra"}, {"--web-dir", filepath.Join(web, "missing")},
		{"--web-dir", web, "--database", filepath.Join(filepath.Dir(database), "missing.sqlite")},
		{"--web-dir", web, "--database", database, "--listen", "invalid-address"},
	} {
		if err := serve(t.Context(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("invalid serve arguments accepted: %v", args)
		}
	}
	if err := serve(t.Context(), []string{"--help"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := serve(ctx, nil, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := serve(t.Context(), []string{"--web-dir", web, "--database", database, "--listen", "127.0.0.1:0"}, failedOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if _, err := invoke(t, "db", "check", "--database", database); err != nil {
		t.Fatalf("failed startup leaked database ownership: %v", err)
	}
}
