package store

import (
	"bytes"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"homebox/migrations"
)

func syncProofRootTwentySixFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, old := syncProofRootTwentyFiveFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[25]
	execSQL(t, old.db, migration.SQL)
	execSQL(t, old.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, old.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, old
}

func pinTestSyncSearchSigner(t *testing.T, s *Store, a *Auth) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = pinSyncSearchSigner(t.Context(), tx, a.serverSecret); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncSearchSignerMigrationRetainsUnknownProofsWithoutGuessingAKey(t *testing.T) {
	path, old := syncProofRootTwentySixFixture(t)
	seq, payload := seedUnprovenSearchChange(t, old)
	proof := bytes.Repeat([]byte{0x5a}, 32)
	execSQL(t, old.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,?)", seq, proof)
	before, projections := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	if err := old.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 26 || s.Report().To < 27 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("signer migration changed the dataset or omitted its recovery snapshot")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projections, currentProjectionState(t, s)) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", seq), proof) ||
		scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=?", seq) != payload {
		t.Fatal("migration changed retained bytes or guessed an unknown signer")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("the pre-migration snapshot did not verify", err)
	}
}

func TestSyncSearchSignerMigrationRejectsLateInjectionAndRollsBack(t *testing.T) {
	path, old := syncProofRootTwentySixFixture(t)
	execSQL(t, old.db, `CREATE TRIGGER inject_sync_signer AFTER INSERT ON schema_migration_checksums WHEN NEW.version=27 BEGIN
 INSERT INTO sync_search_signer(singleton,key_proof) VALUES(1,zeroblob(32)); END`)
	before := domainState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	if err = old.db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("migration committed an injected signer")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed migration did not retain its recovery snapshot", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 26 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='sync_search_signer'") != 0 {
		t.Fatal("failed migration leaked signer schema or ledger state", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed migration did not preserve a verified snapshot", err)
	}
}

func TestSyncSearchSignerUsesAnIndependentImmutableKeyProof(t *testing.T) {
	known := bytes.Repeat([]byte{0x5a}, 32)
	if got := hex.EncodeToString(syncSearchSignerProof(known)); got != "78ebaba483e319ac3f8a94627b12780422407cdc3fe21d93c1e23b677ffebc12" || bytes.Equal(syncSearchSignerProof(known), keyedReceiptSecretProof(known)) {
		t.Fatal("search signer proof did not match its independent HMAC domain", got)
	}
	s, a, _, _, _ := completedAuthFixture(t)
	pinTestSyncSearchSigner(t, s, a)
	pinTestSyncSearchSigner(t, s, a)
	proof := scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_search_signer")
	if !bytes.Equal(proof, syncSearchSignerProof(a.serverSecret)) || bytes.Equal(proof, a.serverSecret) ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 {
		t.Fatal("search signer was not independently bound to the private key")
	}
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	for _, statement := range []string{
		"UPDATE sync_search_signer SET key_proof=zeroblob(32)",
		"DELETE FROM sync_search_signer",
		"INSERT OR REPLACE INTO sync_search_signer VALUES(1,zeroblob(32))",
		"INSERT INTO sync_search_signer VALUES(2,zeroblob(32))",
	} {
		if _, err := s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("immutable search signer accepted a write", statement)
		}
		if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_search_signer"), proof) {
			t.Fatal("a rejected signer write changed its proof")
		}
	}
	if _, err := s.NewAuth(AuthConfig{}); err != nil {
		t.Fatal("authentication rejected its unchanged signer", err)
	}
}

func TestSyncSearchSignerCannotBeReplacedOrRegenerated(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "replaced"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, a, _, _, _ := completedAuthFixture(t)
			pinTestSyncSearchSigner(t, s, a)
			execSQL(t, s.db, "UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'")
			execSQL(t, s.db, "DELETE FROM auth_reauth_grants")
			execSQL(t, s.db, "DELETE FROM api_idempotency WHERE operation IN ('post_auth_logout','post_auth_password','post_auth_reauth')")
			if missing {
				execSQL(t, s.db, "DELETE FROM auth_server_secret")
			} else {
				execSQL(t, s.db, "UPDATE auth_server_secret SET key_bytes=zeroblob(32)")
			}
			if _, err := s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) {
				t.Fatal("authentication accepted a missing or changed search signer", err)
			}
			if missing && scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 {
				t.Fatal("a replacement key was silently generated")
			}
		})
	}
}

func TestSyncSearchSignerDoesNotClaimUnknownProofsOrRegenerateTheirKey(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Unproven search"})
	seq := scalar[int64](t, s.db, "SELECT seq FROM sync_changes WHERE entity_type='search_doc' AND entity_id=?", projectionPair("item", item.ID))
	execSQL(t, s.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(32))", seq)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = pinSyncSearchSigner(t.Context(), tx, a.serverSecret); !errors.Is(err, ErrDomainState) {
		_ = tx.Rollback()
		t.Fatal("an unknown retained marker was assigned a new signer", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 {
		t.Fatal("a failed pin invented a signer")
	}
	if _, err = s.NewAuth(AuthConfig{}); err != nil {
		t.Fatal("unknown untrusted proof bytes blocked ordinary authentication", err)
	}
	execSQL(t, s.db, "UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'")
	execSQL(t, s.db, "DELETE FROM auth_reauth_grants")
	execSQL(t, s.db, "DELETE FROM api_idempotency WHERE operation IN ('post_auth_logout','post_auth_password','post_auth_reauth')")
	execSQL(t, s.db, "DELETE FROM auth_server_secret")
	if _, err = s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 {
		t.Fatal("an unknown proof allowed its missing key to be regenerated", err)
	}
}

func TestSyncSearchSignerPinRejectsLateKeyOrProofInjection(t *testing.T) {
	for _, test := range []struct{ name, timing, statement, removeGuard string }{
		{"ignored marker", "BEFORE", "SELECT RAISE(IGNORE)", ""},
		{"key changed", "AFTER", "UPDATE auth_server_secret SET key_bytes=zeroblob(32)", ""},
		{"key removed", "AFTER", "DELETE FROM auth_server_secret", ""},
		{"marker changed", "AFTER", "UPDATE sync_search_signer SET key_proof=zeroblob(32)", "sync_search_signer_no_update"},
		{"marker removed", "AFTER", "DELETE FROM sync_search_signer", "sync_search_signer_no_delete"},
		{"receipt marker injected", "AFTER", "INSERT INTO auth_keyed_receipt_secret VALUES(1,zeroblob(32))", ""},
		{"proof injected", "AFTER", `INSERT INTO sync_search_proofs(seq,proof) SELECT seq,zeroblob(32) FROM sync_changes WHERE entity_type='search_doc' LIMIT 1`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, catalog, a, token := catalogFixture(t)
			createCatalogItem(t, catalog, token, ItemCreate{Name: "Late proof"})
			original := bytes.Clone(a.serverSecret)
			if test.removeGuard != "" {
				execSQL(t, s.db, "DROP TRIGGER "+test.removeGuard)
			}
			execSQL(t, s.db, "CREATE TRIGGER inject_after_sync_signer "+test.timing+" INSERT ON sync_search_signer BEGIN "+test.statement+"; END")
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = pinSyncSearchSigner(t.Context(), tx, a.serverSecret); err == nil {
				_ = tx.Rollback()
				t.Fatal("late signer mutation was accepted")
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 ||
				!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret"), original) {
				t.Fatal("failed signer pin leaked a marker, proof or key change")
			}
		})
	}
}

func TestSyncSearchSignerInitializationRejectsLateProofInjection(t *testing.T) {
	s := openTestStore(t)
	seq, _ := seedUnprovenSearchChange(t, s)
	execSQL(t, s.db, `CREATE TRIGGER inject_proof_during_key_init AFTER INSERT ON security_audit BEGIN
 INSERT INTO sync_search_proofs(seq,proof) VALUES(`+strconv.FormatInt(seq, 10)+`,zeroblob(32)); END`)
	if _, err := s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) {
		t.Fatal("key initialization accepted a late unknown proof", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 {
		t.Fatal("failed key initialization leaked the key, audit or injected proof")
	}
}

func TestSyncSearchSignerDeltaChecksTheMarkerAfterSessionActivity(t *testing.T) {
	s, locations, a, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-SIGNER", nil)
	pinTestSyncSearchSigner(t, s, a)
	original := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_search_signer"))
	cursor := testSyncDeltaCursor(t, s, a, 0)
	execSQL(t, s.db, "DROP TRIGGER sync_search_signer_no_update")
	execSQL(t, s.db, `CREATE TRIGGER change_sync_signer AFTER UPDATE ON sessions BEGIN
 UPDATE sync_search_signer SET key_proof=zeroblob(32); END`)
	page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 10, PayloadBytes: 4096, PageBytes: 8192})
	if !errors.Is(err, ErrAuthState) || !reflect.DeepEqual(page, syncDeltaPage{}) ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_search_signer"), original) {
		t.Fatal("late signer damage exposed a page or committed", page, err)
	}
}

func TestSyncSearchSignerStillWithholdsUnprovenSearch(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Unknown provenance"})
	seq := scalar[int64](t, s.db, "SELECT seq FROM sync_changes WHERE entity_type='search_doc' AND entity_id=?", projectionPair("item", item.ID))
	pinTestSyncSearchSigner(t, s, a)
	execSQL(t, s.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(32))", seq)
	cursor := testSyncDeltaCursor(t, s, a, 0)
	page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 100, PayloadBytes: 4096, PageBytes: 8192})
	if !errors.Is(err, errSyncSearchUnproven) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("a key marker and arbitrary proof authorized historical search text", page, err)
	}
}

func TestSyncSearchSignerDoesNotAuthorizeLegacyMediaReceiptReplay(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	if _, err := keyedReceipt(t, a, token, "original-media-key"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "DROP TRIGGER auth_keyed_receipt_secret_no_delete")
	execSQL(t, s.db, "DELETE FROM auth_keyed_receipt_secret")
	pinTestSyncSearchSigner(t, s, a)
	if result, err := keyedReceipt(t, a, token, "original-media-key"); !errors.Is(err, ErrDomainState) || result != (ActionAck{}) {
		t.Fatal("a search signer authorized an unproven media receipt", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 {
		t.Fatal("replay reconstructed the independent media receipt marker")
	}
}
