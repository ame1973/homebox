package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var storeFixtureSchema struct {
	once      sync.Once
	directory string
	snapshot  string
	err       error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if storeFixtureSchema.directory != "" {
		if err := os.RemoveAll(storeFixtureSchema.directory); err != nil {
			fmt.Fprintln(os.Stderr, "Remove private test schema:", err)
			code = 1
		}
	}
	os.Exit(code)
}

// Build and verify an empty schema once per test process, never caching domain,
// account, bootstrap, session or server-secret state. Migration-specific tests use
// openFreshTestStore or their explicit legacy fixtures instead of this snapshot.
func fixtureSchemaSnapshot(t *testing.T) string {
	t.Helper()
	storeFixtureSchema.once.Do(func() {
		ctx := context.Background()
		directory, err := os.MkdirTemp("", "homebox-store-test-schema-")
		if err != nil {
			storeFixtureSchema.err = err
			return
		}
		storeFixtureSchema.directory = directory
		s, err := Open(ctx, Config{Path: filepath.Join(directory, "empty.sqlite"), Migrate: true})
		if err != nil {
			storeFixtureSchema.err = err
			return
		}
		var privateRows int
		err = s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM admin_accounts) + (SELECT COUNT(*) FROM sessions) +
 (SELECT COUNT(*) FROM auth_bootstrap) + (SELECT COUNT(*) FROM auth_reauth_grants) + (SELECT COUNT(*) FROM auth_server_secret) +
 (SELECT COUNT(*) FROM security_audit) + (SELECT COUNT(*) FROM api_idempotency) + (SELECT COUNT(*) FROM mutation_events) +
 (SELECT COUNT(*) FROM current_projections) + (SELECT COUNT(*) FROM sync_changes)`).Scan(&privateRows)
		if err == nil && privateRows != 0 {
			err = errors.New("test schema snapshot contains private or published state")
		}
		if err == nil {
			storeFixtureSchema.snapshot, _, err = s.snapshot(ctx, directory)
		}
		err = errors.Join(err, s.Close())
		if err == nil {
			err = VerifySnapshot(ctx, storeFixtureSchema.snapshot)
		}
		storeFixtureSchema.err = err
	})
	if storeFixtureSchema.err != nil {
		t.Fatal(storeFixtureSchema.err)
	}
	return storeFixtureSchema.snapshot
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(privateTempDir(t), "homebox.sqlite")
	source, err := os.Open(fixtureSchemaSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(errors.Join(err, source.Close()))
	}
	// This is the closed, verified, self-contained snapshot, never a live WAL DB.
	_, copyErr := io.Copy(file, source)
	if err = errors.Join(copyErr, file.Sync(), file.Close(), source.Close()); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	// Independent instance/epoch initialization mirrors the reference fresh SQL.
	// No authentication/domain state has been created or copied at this point.
	if _, err = s.db.ExecContext(t.Context(), `UPDATE sync_state SET server_instance_id=lower(hex(randomblob(16))),
 dataset_epoch=lower(hex(randomblob(16))),updated_at=? WHERE singleton=1`, stamp(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreFixtureSnapshotsKeepIndependentStateIdentityAndSecrets(t *testing.T) {
	first, second := openTestStore(t), openTestStore(t)
	if first.path == second.path || scalar[string](t, first.db, "SELECT server_instance_id FROM sync_state") == scalar[string](t, second.db, "SELECT server_instance_id FROM sync_state") ||
		scalar[string](t, first.db, "SELECT dataset_epoch FROM sync_state") == scalar[string](t, second.db, "SELECT dataset_epoch FROM sync_state") {
		t.Fatal("test stores share identity, epoch or a database path")
	}
	for _, s := range []*Store{first, second} {
		for _, table := range []string{"admin_accounts", "sessions", "auth_bootstrap", "auth_reauth_grants", "auth_server_secret", "security_audit", "api_idempotency", "mutation_events", "current_projections", "projection_revisions", "sync_changes", "items", "locations", "purchases", "asset_profiles"} {
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table) != 0 {
				t.Fatal("test fixture copied private/source/counter/receipt state", table)
			}
		}
		if info, err := os.Stat(filepath.Dir(s.path)); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("test fixture directory is not private", err)
		}
		for _, suffix := range []string{"", ".lock", "-wal", "-shm"} {
			if info, err := os.Stat(s.path + suffix); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("test fixture database/sidecar is not independently private", err)
			}
		}
	}
	firstSecret, err := first.loadAuthServerSecret(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secondSecret, err := second.loadAuthServerSecret(t.Context())
	if err != nil || string(firstSecret) == string(secondSecret) {
		t.Fatal("real independent server-secret initialization was bypassed", err)
	}
	execSQL(t, first.db, "INSERT INTO locations(id,code,name,created_at,updated_at) VALUES('private','PRIVATE','First fixture','legacy','legacy')")
	if scalar[int](t, second.db, "SELECT COUNT(*) FROM locations") != 0 || scalar[int](t, second.db, "SELECT COUNT(*) FROM security_audit") != 1 {
		t.Fatal("test fixtures share source or authentication audit")
	}
	if err = VerifySnapshot(t.Context(), fixtureSchemaSnapshot(t)); err != nil {
		t.Fatal("a writable test fixture mutated the original empty snapshot", err)
	}
	closedTemplate, err := openSQL(fixtureSchemaSnapshot(t), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closedTemplate.Close(); err != nil {
			t.Error(err)
		}
	})
	if scalar[int](t, closedTemplate, "SELECT COUNT(*) FROM locations") != 0 || scalar[int](t, closedTemplate, "SELECT COUNT(*) FROM auth_server_secret") != 0 || scalar[int](t, closedTemplate, "SELECT COUNT(*) FROM security_audit") != 0 {
		t.Fatal("empty schema snapshot acquired mutable source or credentials")
	}
}
