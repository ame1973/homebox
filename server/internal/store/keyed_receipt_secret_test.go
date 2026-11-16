package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func keyedReceipt(t *testing.T, a *Auth, token, key string) (ActionAck, error) {
	t.Helper()
	fingerprint, err := a.mediaUploadFingerprint(t.Context(), key, "image/png", []byte("original raw image"))
	if err != nil {
		return ActionAck{}, err
	}
	return runDomainMutation(t.Context(), a, token, key, "post_media", mediaUploadReceiptRequest{RawFileMAC: fingerprint}, func(m *mutationTx) (ActionAck, error) {
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	})
}

func TestKeyedReceiptRequiresTypedRawFingerprintAndPinsOnlyCommittedRequests(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	apply := func(m *mutationTx) (ActionAck, error) {
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	}
	for _, test := range []struct {
		operation string
		request   any
	}{
		{"post_media", struct{}{}},
		{"post_media", mediaUploadReceiptRequest{RawFileMAC: strings.Repeat("A", 64)}},
		{"post_media", mediaUploadReceiptRequest{RawFileMAC: strings.Repeat("a", 63)}},
		{"post_items", mediaUploadReceiptRequest{RawFileMAC: strings.Repeat("a", 64)}},
	} {
		if value, err := runDomainMutation(t.Context(), a, token, "rejected-key", test.operation, test.request, apply); !errors.Is(err, ErrDomainInput) || value != (ActionAck{}) {
			t.Fatal("a noncanonical or misrouted keyed receipt was accepted", err)
		}
	}
	other := *a
	other.serverSecret = bytes.Repeat([]byte{0x5a}, 32)
	if value, err := keyedReceipt(t, &other, token, "wrong-secret-key"); !errors.Is(err, ErrAuthState) || value != (ActionAck{}) {
		t.Fatal("a fingerprint made with a different in-memory secret was accepted", err)
	}
	fingerprint, err := a.mediaUploadFingerprint(t.Context(), "failing-key", "image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("domain operation failed")
	if _, err = runDomainMutation(t.Context(), a, token, "failing-key", "post_media", mediaUploadReceiptRequest{RawFileMAC: fingerprint}, func(*mutationTx) (ActionAck, error) {
		return ActionAck{}, sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal("failed operation did not propagate its error", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency WHERE operation='post_media'") != 0 {
		t.Fatal("a rejected or rolled-back request pinned a key or receipt")
	}
}

func TestKeyedReceiptRollsBackOnSecretOrMarkerChangesAfterWrites(t *testing.T) {
	for _, test := range []struct {
		name, setup, removeGuard string
	}{
		{"audit replaces secret", `CREATE TRIGGER late_key_change AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE auth_server_secret SET key_bytes=zeroblob(32); END`, ""},
		{"audit removes secret", `CREATE TRIGGER late_key_change AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN DELETE FROM auth_server_secret; END`, ""},
		{"session activity replaces secret", `CREATE TRIGGER late_key_change AFTER UPDATE ON sessions BEGIN UPDATE auth_server_secret SET key_bytes=zeroblob(32); END`, ""},
		{"marker insert ignored", `CREATE TRIGGER ignore_key_marker BEFORE INSERT ON auth_keyed_receipt_secret BEGIN SELECT RAISE(IGNORE); END`, ""},
		{"audit removes marker", `CREATE TRIGGER late_marker_delete AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN DELETE FROM auth_keyed_receipt_secret; END`, ""},
		{"audit corrupts marker without guard", `CREATE TRIGGER late_marker_change AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE auth_keyed_receipt_secret SET key_proof=zeroblob(32); END`, "auth_keyed_receipt_secret_no_update"},
		{"audit deletes marker without guard", `CREATE TRIGGER late_marker_delete AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN DELETE FROM auth_keyed_receipt_secret; END`, "auth_keyed_receipt_secret_no_delete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, a, _, _, token := completedAuthFixture(t)
			before := bytes.Clone(a.serverSecret)
			if test.removeGuard != "" {
				execSQL(t, s.db, "DROP TRIGGER "+test.removeGuard)
			}
			execSQL(t, s.db, test.setup)
			if result, err := keyedReceipt(t, a, token, "original-key"); err == nil || result != (ActionAck{}) {
				t.Fatal("late secret/marker tampering produced an acknowledgement", err)
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency WHERE operation='post_media'") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_media'") != 0 ||
				!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret"), before) {
				t.Fatal("failed publication left a key, receipt, audit or secret change")
			}
		})
	}
}

func TestKeyedReceiptReplayChecksExactSecretAfterSessionActivity(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	first, err := keyedReceipt(t, a, token, "original-key")
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(a.serverSecret)
	execSQL(t, s.db, `CREATE TRIGGER replay_key_change AFTER UPDATE ON sessions BEGIN UPDATE auth_server_secret SET key_bytes=zeroblob(32); END`)
	if result, err := keyedReceipt(t, a, token, "original-key"); !errors.Is(err, ErrAuthState) || result != (ActionAck{}) {
		t.Fatal("replay returned a receipt after the database secret changed", err)
	}
	if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret"), before) ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency WHERE operation='post_media'") != 1 {
		t.Fatal("failed replay committed a secret change or lost its original receipt")
	}
	execSQL(t, s.db, "DROP TRIGGER replay_key_change")
	if result, err := keyedReceipt(t, a, token, "original-key"); err != nil || result != first {
		t.Fatal("the original receipt could not be reconciled after rollback", err)
	}
}

func TestPinnedKeyCannotBeReplacedOrRegeneratedSilently(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "replaced"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, a, _, _, token := completedAuthFixture(t)
			if _, err := keyedReceipt(t, a, token, "original-key"); err != nil {
				t.Fatal(err)
			}
			// Remove all other conditions that already prevent key initialization.
			execSQL(t, s.db, "UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'")
			execSQL(t, s.db, "DELETE FROM auth_reauth_grants")
			execSQL(t, s.db, "DELETE FROM api_idempotency WHERE operation IN ('post_auth_logout','post_auth_password','post_auth_reauth')")
			if missing {
				execSQL(t, s.db, "DELETE FROM auth_server_secret")
			} else {
				execSQL(t, s.db, "UPDATE auth_server_secret SET key_bytes=zeroblob(32)")
			}
			if _, err := s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) {
				t.Fatal("authentication accepted a missing or changed pinned key", err)
			}
			if missing && scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 {
				t.Fatal("a replacement key was silently generated")
			}
		})
	}
}

func TestKeyedReceiptSecretMarkerIsImmutableEvenWithRecursiveTriggersDisabled(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	if _, err := keyedReceipt(t, a, token, "original-key"); err != nil {
		t.Fatal(err)
	}
	original := scalar[[]byte](t, s.db, "SELECT key_proof FROM auth_keyed_receipt_secret")
	if len(original) != 32 || bytes.Equal(original, a.serverSecret) || !bytes.Equal(original, keyedReceiptSecretProof(a.serverSecret)) {
		t.Fatal("key dependency is not bound to the private server secret")
	}
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	for _, query := range []string{
		"UPDATE auth_keyed_receipt_secret SET key_proof=zeroblob(32)",
		"DELETE FROM auth_keyed_receipt_secret",
		"INSERT OR REPLACE INTO auth_keyed_receipt_secret VALUES(1,zeroblob(32))",
	} {
		if _, err := s.db.ExecContext(t.Context(), query); err == nil {
			t.Fatal("immutable key marker accepted a write", query)
		}
		if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM auth_keyed_receipt_secret"), original) {
			t.Fatal("rejected marker write changed its proof")
		}
	}
}

func TestKeyedReceiptRefusesMissingMarkerOnReplay(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	if _, err := keyedReceipt(t, a, token, "original-key"); err != nil {
		t.Fatal(err)
	}
	// Simulate database corruption beyond the ordinary immutable SQL interface.
	execSQL(t, s.db, "DROP TRIGGER auth_keyed_receipt_secret_no_delete")
	execSQL(t, s.db, "DELETE FROM auth_keyed_receipt_secret")
	if result, err := keyedReceipt(t, a, token, "original-key"); !errors.Is(err, ErrDomainState) || result != (ActionAck{}) {
		t.Fatal("a missing dependency was silently reconstructed during replay", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 {
		t.Fatal("replay reconstructed a missing marker")
	}
}

func TestServerSecretInitializationRejectsLateAuditMutation(t *testing.T) {
	s := openTestStore(t)
	execSQL(t, s.db, `CREATE TRIGGER replace_new_key AFTER INSERT ON security_audit BEGIN UPDATE auth_server_secret SET key_bytes=zeroblob(32); END`)
	if _, err := s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) {
		t.Fatal("initialization returned a key different from the database", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit") != 0 {
		t.Fatal("failed key initialization did not roll back")
	}
}

func keyedReceiptRootTwentyOneFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, old := barcodeRootTwentyFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[20]
	execSQL(t, old.db, migration.SQL)
	execSQL(t, old.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, old.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, old
}

func TestKeyedReceiptMigrationRetainsLegacyReceiptsWithoutGuessingTheirKey(t *testing.T) {
	path, old := keyedReceiptRootTwentyOneFixture(t)
	execSQL(t, old.db, `INSERT INTO api_idempotency VALUES('legacy-media','post_media','legacy-fingerprint',200,' {"legacy":true} ','2026','2099')`)
	execSQL(t, old.db, `UPDATE current_projections SET payload_json=' {"retained":true} ' WHERE entity_type='asset'`)
	before, projected := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 21 || s.Report().To < 22 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("key dependency migration omitted a recovery snapshot or changed the epoch")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projected, currentProjectionState(t, s)) || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 ||
		scalar[string](t, s.db, "SELECT response_json FROM api_idempotency WHERE key='legacy-media'") != ` {"legacy":true} ` {
		t.Fatal("migration changed frozen bytes or guessed a legacy receipt was keyed")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-migration snapshot was not verified", err)
	}
	if _, err = s.NewAuth(AuthConfig{}); err != nil {
		t.Fatal("an unkeyed legacy media receipt incorrectly blocked normal key initialization", err)
	}
}

func TestKeyedReceiptMigrationRollsBackOnLateLedgerFailure(t *testing.T) {
	path, old := keyedReceiptRootTwentyOneFixture(t)
	execSQL(t, old.db, `CREATE TRIGGER fail_keyed_receipt_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=22 BEGIN SELECT RAISE(ABORT,'key dependency ledger failure'); END`)
	before := domainState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed ledger write committed the key dependency migration")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed migration lost its recovery snapshot", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 21 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 21 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='auth_keyed_receipt_secret'") != 0 {
		t.Fatal("failed migration leaked DDL, source or ledger state", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed migration did not retain the verified original snapshot", err)
	}
}
