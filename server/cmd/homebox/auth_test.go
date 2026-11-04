package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homebox/internal/store"
)

func TestAuthBootstrapCommandDeliversOnlyPrivateFileMetadata(t *testing.T) {
	directory := privateDirectory(t)
	database := filepath.Join(directory, "homebox.sqlite")
	output := filepath.Join(directory, "bootstrap.token")
	if _, err := invoke(t, "db", "migrate", "--database", database); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	before := time.Now()
	if err := run(t.Context(), []string{"auth", "bootstrap", "--database", database, "--output", output}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var metadata store.BootstrapFile
	if err := json.Unmarshal(stdout.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Path != output || !metadata.ExpiresAt.After(before) || metadata.ExpiresAt.After(before.Add(31*time.Minute)) {
		t.Fatal("unexpected file metadata")
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSuffix(string(content), "\n")
	if len(token) != 43 || bytes.Contains(stdout.Bytes(), []byte(token)) || bytes.Contains(stderr.Bytes(), []byte(token)) {
		t.Fatal("CLI exposed the raw setup secret")
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("CLI output was not private", err)
	}
	if _, err = invoke(t, "auth", "bootstrap", "--database", database, "--output", output); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing file was overwritten")
	}
	db, err := store.Open(t.Context(), store.Config{Path: database})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth, err := db.NewAuth(store.AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.Setup(t.Context(), token, "admin", "correct horse battery staple"); err != nil {
		t.Fatal("delivered secret was not usable", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(directory, "must-not-exist.token")
	if _, err = invoke(t, "auth", "bootstrap", "--database", database, "--output", second); !errors.Is(err, store.ErrSetupClosed) {
		t.Fatal("configured installation reopened setup")
	}
	if _, err = os.Stat(second); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed command left a new file")
	}
}

func TestAuthCommandHelpAndInvalidArgumentsDoNotProvision(t *testing.T) {
	for _, args := range [][]string{{"auth"}, {"auth", "help"}, {"auth", "--help"}, {"auth", "bootstrap", "--help"}} {
		var output, diagnostic bytes.Buffer
		if err := run(t.Context(), args, &output, &diagnostic); err != nil || output.Len()+diagnostic.Len() == 0 {
			t.Fatal("missing auth help", err)
		}
	}
	for _, args := range [][]string{
		{"auth", "unknown"}, {"auth", "bootstrap"}, {"auth", "bootstrap", "--output", ""},
		{"auth", "bootstrap", "--output", "unused", "--timeout", "0s"},
		{"auth", "bootstrap", "--output", "unused", "--timeout", "-1s"},
		{"auth", "bootstrap", "--output", "unused", "unexpected"},
		{"auth", "bootstrap", "--unknown-flag"},
	} {
		var output, diagnostic bytes.Buffer
		if err := run(t.Context(), args, &output, &diagnostic); err == nil {
			t.Fatal("invalid auth arguments succeeded")
		}
	}
	directory := privateDirectory(t)
	missing := filepath.Join(directory, "missing.sqlite")
	output := filepath.Join(directory, "must-not-exist.token")
	if _, err := invoke(t, "auth", "bootstrap", "--database", missing, "--output", output); !errors.Is(err, store.ErrMigrationRequired) {
		t.Fatal("auth command silently initialized a database")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed auth command created an output")
	}
}

func TestAuthCommandRequiresExclusiveOwnershipAndHonorsCancellation(t *testing.T) {
	directory := privateDirectory(t)
	database := filepath.Join(directory, "homebox.sqlite")
	output := filepath.Join(directory, "must-not-exist.token")
	if _, err := invoke(t, "db", "migrate", "--database", database); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), store.Config{Path: database})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := invoke(t, "auth", "bootstrap", "--database", database, "--output", output); !errors.Is(err, store.ErrLocked) {
		t.Fatal("auth command bypassed process ownership")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{"auth", "bootstrap", "--database", database, "--output", output}, &stdout, &stderr); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled auth command did not stop")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused command created a file")
	}
}

func TestAuthMetadataOutputFailureDoesNotLoseDurableDelivery(t *testing.T) {
	directory := privateDirectory(t)
	database := filepath.Join(directory, "homebox.sqlite")
	output := filepath.Join(directory, "bootstrap.token")
	if _, err := invoke(t, "db", "migrate", "--database", database); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"auth", "bootstrap", "--database", database, "--output", output}, failedOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("metadata output error was hidden")
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal("committed private delivery was lost", err)
	}
	db, err := store.Open(t.Context(), store.Config{Path: database})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth, err := db.NewAuth(store.AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.Setup(t.Context(), strings.TrimSuffix(string(content), "\n"), "admin", "correct horse battery staple"); err != nil {
		t.Fatal("durable token was not activated", err)
	}
}
