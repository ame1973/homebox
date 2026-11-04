package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBootstrapFileIsPrivateUsableAndContainsNoPublicMetadataSecret(t *testing.T) {
	_, a, _ := authFixture(t)
	path := filepath.Join(privateTempDir(t), "bootstrap.token")
	delivery, err := a.IssueBootstrapFile(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("secret file was not mode 0600", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSuffix(string(content), "\n")
	if !validAuthToken(token) || len(content) != 44 || delivery.Path != path {
		t.Fatal("invalid private delivery format")
	}
	metadata, err := json.Marshal(delivery)
	if err != nil || strings.Contains(string(metadata), token) {
		t.Fatal("metadata disclosed the secret")
	}
	if _, err = a.Setup(t.Context(), token, "admin", testAdminPassword); err != nil {
		t.Fatal("delivered token was not activated", err)
	}
	if _, err = a.IssueBootstrapFile(t.Context(), filepath.Join(filepath.Dir(path), "another.token")); !errors.Is(err, ErrSetupClosed) {
		t.Fatal("configured installation issued another setup secret")
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(path), "another.token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed delivery left an output file")
	}
}

func TestBootstrapFileNeverOverwritesExistingFilesLinksOrSpecialFiles(t *testing.T) {
	s, a, _ := authFixture(t)
	first := filepath.Join(privateTempDir(t), "first.token")
	if _, err := a.IssueBootstrapFile(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	before := scalar[string](t, s.db, "SELECT token_hash FROM auth_bootstrap")
	content, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.IssueBootstrapFile(t.Context(), first); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing credential file was overwritten")
	}
	for _, kind := range []string{"symlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(privateTempDir(t), "occupied")
			switch kind {
			case "symlink":
				if err := os.Symlink(first, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.IssueBootstrapFile(t.Context(), path); err == nil {
				t.Fatal("occupied output path was replaced")
			}
		})
	}
	after, err := os.ReadFile(first)
	if err != nil || string(after) != string(content) || scalar[string](t, s.db, "SELECT token_hash FROM auth_bootstrap") != before {
		t.Fatal("failed publication altered the earlier credential")
	}
}

func TestBootstrapFileRejectsPermissiveDirectoriesAndCanceledWork(t *testing.T) {
	s, a, _ := authFixture(t)
	directory := privateTempDir(t)
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.IssueBootstrapFile(t.Context(), filepath.Join(directory, "secret.token")); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("public output directory accepted")
	}
	if _, err := a.IssueBootstrapFile(t.Context(), ""); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("empty output path accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	absent := filepath.Join(privateTempDir(t), "absent", "secret.token")
	if _, err := a.IssueBootstrapFile(ctx, absent); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled publication did not stop")
	}
	if _, err := os.Stat(filepath.Dir(absent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled publication created directories")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_bootstrap") != 0 {
		t.Fatal("invalid delivery activated a credential")
	}
}

func TestBootstrapDeliveryFailureKeepsThePreviousCredential(t *testing.T) {
	s, a, _ := authFixture(t)
	previous, _, err := a.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	planned := errors.New("planned delivery failure")
	if token, _, err := a.issueBootstrap(t.Context(), func(string) error { return planned }); token != "" || !errors.Is(err, planned) {
		t.Fatal("failed delivery published a credential")
	}
	if scalar[string](t, s.db, "SELECT token_hash FROM auth_bootstrap") != authDigest(previous) || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='bootstrap_issued'") != 1 {
		t.Fatal("delivery failure did not roll back rotation")
	}
}

func TestBootstrapDeliveryPrecedesDatabaseActivation(t *testing.T) {
	s, a, _ := authFixture(t)
	previous, _, err := a.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := openSQL(s.path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	delivered := false
	issued, _, err := a.issueBootstrap(t.Context(), func(value string) error {
		delivered = true
		if !validAuthToken(value) {
			return errors.New("delivery received an invalid token")
		}
		var digest string
		if err := reader.QueryRowContext(t.Context(), "SELECT token_hash FROM auth_bootstrap").Scan(&digest); err != nil {
			return err
		}
		if digest != authDigest(previous) {
			return errors.New("credential activated before delivery completed")
		}
		return nil
	})
	if err != nil || !delivered || scalar[string](t, s.db, "SELECT token_hash FROM auth_bootstrap") != authDigest(issued) {
		t.Fatal("delivery/activation ordering failed", err)
	}
}

func TestBootstrapDeliveryCancellationAndCommitFailureRemoveNewFiles(t *testing.T) {
	t.Run("cancellation after delivery", func(t *testing.T) {
		s, a, _ := authFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if token, _, err := a.issueBootstrap(ctx, func(string) error { cancel(); return nil }); token != "" || err == nil {
			t.Fatal("canceled activation returned a secret")
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_bootstrap") != 0 {
			t.Fatal("canceled activation persisted a hash")
		}
	})
	t.Run("commit failure after durable file", func(t *testing.T) {
		s, a, _ := authFixture(t)
		previous, _, err := a.IssueBootstrap(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, s.db, "CREATE TABLE delivery_commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
		execSQL(t, s.db, "CREATE TRIGGER fail_delivery_commit AFTER INSERT ON security_audit WHEN new.event_type='bootstrap_issued' BEGIN INSERT INTO delivery_commit_guard VALUES('missing'); END")
		path := filepath.Join(privateTempDir(t), "failed.token")
		if result, err := a.IssueBootstrapFile(t.Context(), path); err == nil || result.Path != "" {
			t.Fatal("failed commit returned file metadata")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed commit left a credential file")
		}
		if scalar[string](t, s.db, "SELECT token_hash FROM auth_bootstrap") != authDigest(previous) {
			t.Fatal("failed publication replaced the previous credential")
		}
	})
}
