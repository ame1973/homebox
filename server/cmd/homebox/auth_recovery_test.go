package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homebox/internal/store"
)

func provisionCLIAccount(t *testing.T, database string) {
	t.Helper()
	if _, err := invoke(t, "db", "migrate", "--database", database); err != nil {
		t.Fatal(err)
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
	token, _, err := auth.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.Setup(t.Context(), token, "admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCLIRequiresConfirmationAndStoppedServer(t *testing.T) {
	directory := privateDirectory(t)
	database := filepath.Join(directory, "homebox.sqlite")
	output := filepath.Join(directory, "recovery.txt")
	provisionCLIAccount(t, database)
	for _, confirmation := range []string{"", "reset_password", "RESET"} {
		var stdout, stderr bytes.Buffer
		if err := run(t.Context(), []string{"auth", "reset-password", "--database", database, "--output", output, "--confirm", confirmation}, &stdout, &stderr); err == nil {
			t.Fatal("recovery accepted missing or wrong confirmation")
		}
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unconfirmed recovery created a file")
	}
	db, err := store.Open(t.Context(), store.Config{Path: database})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := invoke(t, "auth", "reset-password", "--database", database, "--output", output, "--confirm", "RESET_PASSWORD"); !errors.Is(err, store.ErrLocked) {
		t.Fatal("recovery bypassed running-process ownership")
	}
}

func TestRecoveryCLIPrintsMetadataAndTemporaryCredentialMustBeChanged(t *testing.T) {
	directory := privateDirectory(t)
	database := filepath.Join(directory, "homebox.sqlite")
	output := filepath.Join(directory, "recovery.txt")
	provisionCLIAccount(t, database)
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"auth", "reset-password", "--database", database, "--output", output, "--confirm", "RESET_PASSWORD"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	temporary := strings.TrimSuffix(string(content), "\n")
	if len(temporary) != 26 || bytes.Contains(stdout.Bytes(), []byte(temporary)) || bytes.Contains(stderr.Bytes(), []byte(temporary)) {
		t.Fatal("recovery exposed or failed to generate a temporary password")
	}
	var metadata store.RecoveryFile
	if err = json.Unmarshal(stdout.Bytes(), &metadata); err != nil || metadata.Path != output || metadata.Username != "admin" || !metadata.MustChangePassword || metadata.Revision != 2 {
		t.Fatal("invalid recovery metadata", err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("recovery output was not private", err)
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
	if _, _, err = auth.Login(t.Context(), "admin", "correct horse battery staple"); !errors.Is(err, store.ErrAuthentication) {
		t.Fatal("old credential survived CLI recovery")
	}
	session, _, err := auth.Login(t.Context(), "admin", temporary)
	if err != nil || !session.Admin.MustChangePassword {
		t.Fatal("temporary credential did not require change", err)
	}
}

func TestRecoveryCLIMetadataOutputFailureKeepsCommittedPrivateFile(t *testing.T) {
	directory := privateDirectory(t)
	database := filepath.Join(directory, "homebox.sqlite")
	output := filepath.Join(directory, "recovery.txt")
	provisionCLIAccount(t, database)
	if err := run(t.Context(), []string{"auth", "reset-password", "--database", database, "--output", output, "--confirm", "RESET_PASSWORD"}, failedOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("output failure was hidden")
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal("durable recovery was deleted after reporting failure", err)
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
	if _, _, err = auth.Login(t.Context(), "admin", strings.TrimSuffix(string(content), "\n")); err != nil {
		t.Fatal("committed recovery credential was not usable", err)
	}
}
