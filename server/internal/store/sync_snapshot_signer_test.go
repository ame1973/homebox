package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

func snapshotSignerFixture(t *testing.T, withRow bool) (*Store, *Auth) {
	t.Helper()
	s := openTestStore(t)
	a, err := s.NewAuth(AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	seedSnapshotSignerFixture(t, s, withRow)
	return s, a
}

func seedSnapshotSignerFixture(t *testing.T, s *Store, withRow bool) {
	t.Helper()
	execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'signer-snapshot',server_instance_id,dataset_epoch,1,0,'2026-11-27T00:00:00.000000000Z','2026-11-27T00:15:00.000000000Z','ready' FROM sync_state`)
	if withRow {
		addSnapshotMetadataRow(t, s, "signer-snapshot", 0, "location", "fixture-id", `{}`)
	}
}

func pinTestSnapshotSigner(t *testing.T, s *Store, a *Auth) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = pinSyncSnapshotSigner(t.Context(), tx, a.serverSecret); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncSnapshotSignerBindsAnIndependentImmutableKey(t *testing.T) {
	known := bytes.Repeat([]byte{0x5a}, 32)
	proof := syncSnapshotSignerProof(known)
	if hex.EncodeToString(proof) != "a4aeb1835f2aa1bb5f256c8fe59c3b234a01414130bdcd124618a9c3bbcd12a9" ||
		bytes.Equal(proof, syncSearchSignerProof(known)) || bytes.Equal(proof, keyedReceiptSecretProof(known)) {
		t.Fatal("snapshot signer did not match its independent HMAC domain")
	}
	s, a := snapshotSignerFixture(t, false)
	pinTestSnapshotSigner(t, s, a)
	pinTestSnapshotSigner(t, s, a)
	stored := scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer")
	if !bytes.Equal(stored, syncSnapshotSignerProof(a.serverSecret)) || bytes.Equal(stored, a.serverSecret) ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 {
		t.Fatal("snapshot signer changed or claimed an independent key dependency")
	}
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	for _, statement := range []string{
		"UPDATE sync_snapshot_signer SET key_proof=zeroblob(32)",
		"DELETE FROM sync_snapshot_signer",
		"INSERT OR REPLACE INTO sync_snapshot_signer VALUES(1,zeroblob(32))",
		"INSERT INTO sync_snapshot_signer VALUES(2,zeroblob(32))",
	} {
		if _, err := s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("snapshot signer accepted an invalid write", statement)
		}
		if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"), stored) {
			t.Fatal("a rejected signer write changed the marker")
		}
	}
	if _, err := s.NewAuth(AuthConfig{}); err != nil {
		t.Fatal("authentication rejected the unchanged snapshot signer", err)
	}
}

func TestSyncSnapshotSignerPreventsKeyReplacementAndRegeneration(t *testing.T) {
	for _, test := range []struct{ name, dependency string }{
		{"pinned marker", "signer"}, {"unknown row proof", "row"}, {"unknown empty seal", "seal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, a := snapshotSignerFixture(t, test.dependency == "row")
			switch test.dependency {
			case "signer":
				pinTestSnapshotSigner(t, s, a)
			case "row":
				execSQL(t, s.db, "INSERT INTO sync_snapshot_row_proofs VALUES('signer-snapshot',0,zeroblob(32))")
			case "seal":
				execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES('signer-snapshot',0,zeroblob(32))")
			}
			if test.dependency != "signer" {
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = pinSyncSnapshotSigner(t.Context(), tx, a.serverSecret); !errors.Is(err, ErrDomainState) {
					_ = tx.Rollback()
					t.Fatal("an unknown proof or seal was assigned a new signer", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if _, err = s.NewAuth(AuthConfig{}); err != nil {
					t.Fatal("unknown untrusted snapshot bytes blocked ordinary authentication", err)
				}
			}
			execSQL(t, s.db, "DELETE FROM auth_server_secret")
			if _, err := s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 {
				t.Fatal("snapshot dependency allowed a missing key to be regenerated", err)
			}
		})
	}
	s, a := snapshotSignerFixture(t, false)
	pinTestSnapshotSigner(t, s, a)
	execSQL(t, s.db, "UPDATE auth_server_secret SET key_bytes=zeroblob(32)")
	if _, err := s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) {
		t.Fatal("authentication accepted a changed snapshot signing key", err)
	}
}

func TestSyncSnapshotSignerDoesNotBindUnsealedLegacyRows(t *testing.T) {
	s, a := snapshotSignerFixture(t, true)
	previous := bytes.Clone(a.serverSecret)
	execSQL(t, s.db, "DELETE FROM auth_server_secret")
	replacement, err := s.NewAuth(AuthConfig{})
	if err != nil || bytes.Equal(replacement.serverSecret, previous) ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_rows WHERE snapshot_id='signer-snapshot'") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer") != 0 {
		t.Fatal("an unsealed legacy row was assigned a guessed snapshot signer", err)
	}
}

func TestSyncSnapshotSignerPinRollsBackLateChanges(t *testing.T) {
	for _, test := range []struct {
		name, timing, statement, removeGuard string
		row                                  bool
	}{
		{"ignored marker", "BEFORE", "SELECT RAISE(IGNORE)", "", false},
		{"key changed", "AFTER", "UPDATE auth_server_secret SET key_bytes=zeroblob(32)", "", false},
		{"key removed", "AFTER", "DELETE FROM auth_server_secret", "", false},
		{"marker changed", "AFTER", "UPDATE sync_snapshot_signer SET key_proof=zeroblob(32)", "sync_snapshot_signer_no_update", false},
		{"marker removed", "AFTER", "DELETE FROM sync_snapshot_signer", "sync_snapshot_signer_no_delete", false},
		{"row proof injected", "AFTER", "INSERT INTO sync_snapshot_row_proofs VALUES('signer-snapshot',0,zeroblob(32))", "", true},
		{"seal injected", "AFTER", "INSERT INTO sync_snapshot_seals VALUES('signer-snapshot',0,zeroblob(32))", "", false},
		{"valid search signer injected", "AFTER", "SEARCH", "", false},
		{"valid receipt signer injected", "AFTER", "RECEIPT", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, a := snapshotSignerFixture(t, test.row)
			original := bytes.Clone(a.serverSecret)
			if test.removeGuard != "" {
				execSQL(t, s.db, "DROP TRIGGER "+test.removeGuard)
			}
			statement := test.statement
			switch statement {
			case "SEARCH":
				statement = fmt.Sprintf("INSERT INTO sync_search_signer VALUES(1,X'%x')", syncSearchSignerProof(original))
			case "RECEIPT":
				statement = fmt.Sprintf("INSERT INTO auth_keyed_receipt_secret VALUES(1,X'%x')", keyedReceiptSecretProof(original))
			}
			execSQL(t, s.db, "CREATE TRIGGER inject_snapshot_signer "+test.timing+" INSERT ON sync_snapshot_signer BEGIN "+statement+"; END")
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = pinSyncSnapshotSigner(t.Context(), tx, a.serverSecret); err == nil {
				_ = tx.Rollback()
				t.Fatal("snapshot signer pin accepted a late change")
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_row_proofs") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_seals") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 ||
				!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret"), original) {
				t.Fatal("failed signer pin committed a changed dependency")
			}
		})
	}
}

func TestSyncSnapshotSignerPinPreservesExistingKeyDependencies(t *testing.T) {
	for _, dependency := range []string{"search", "receipt"} {
		t.Run(dependency, func(t *testing.T) {
			s, a := snapshotSignerFixture(t, false)
			table, guard := "sync_search_signer", "sync_search_signer_no_delete"
			if dependency == "search" {
				pinTestSyncSearchSigner(t, s, a)
			} else {
				table, guard = "auth_keyed_receipt_secret", "auth_keyed_receipt_secret_no_delete"
				execSQL(t, s.db, "INSERT INTO auth_keyed_receipt_secret VALUES(1,?)", keyedReceiptSecretProof(a.serverSecret))
			}
			original := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM "+table))
			execSQL(t, s.db, "DROP TRIGGER "+guard)
			execSQL(t, s.db, "CREATE TRIGGER remove_dependency_during_snapshot_pin AFTER INSERT ON sync_snapshot_signer BEGIN DELETE FROM "+table+"; END")
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = pinSyncSnapshotSigner(t.Context(), tx, a.serverSecret); !errors.Is(err, ErrAuthState) {
				_ = tx.Rollback()
				t.Fatal("snapshot pin accepted a missing pre-existing key dependency", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM "+table), original) ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer") != 0 {
				t.Fatal("failed snapshot pin committed a lost dependency")
			}
		})
	}
}

func TestSyncSearchSignerPinPreservesSnapshotDependency(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "injected"
		if existing {
			name = "removed"
		}
		t.Run(name, func(t *testing.T) {
			s, a := snapshotSignerFixture(t, false)
			statement := fmt.Sprintf("INSERT INTO sync_snapshot_signer VALUES(1,X'%x')", syncSnapshotSignerProof(a.serverSecret))
			if existing {
				pinTestSnapshotSigner(t, s, a)
				execSQL(t, s.db, "DROP TRIGGER sync_snapshot_signer_no_delete")
				statement = "DELETE FROM sync_snapshot_signer"
			}
			execSQL(t, s.db, "CREATE TRIGGER alter_snapshot_during_search_pin AFTER INSERT ON sync_search_signer BEGIN "+statement+"; END")
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = pinSyncSearchSigner(t.Context(), tx, a.serverSecret); !errors.Is(err, ErrAuthState) {
				_ = tx.Rollback()
				t.Fatal("search pin accepted a changed snapshot dependency", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			want := 0
			if existing {
				want = 1
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer") != want ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 {
				t.Fatal("failed search pin committed a changed dependency")
			}
		})
	}
}

func TestSearchPublicationPreservesExistingSnapshotSigner(t *testing.T) {
	for _, test := range []struct{ name, trigger string }{
		{"source write", "AFTER INSERT ON items"},
		{"search cache write", "AFTER INSERT ON search_documents"},
		{"search signer pin", "AFTER INSERT ON sync_search_signer"},
		{"audit write", "AFTER INSERT ON mutation_events WHEN NEW.operation='post_items'"},
		{"session activity", "AFTER UPDATE ON sessions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, catalog, a, token := catalogFixture(t)
			pinTestSnapshotSigner(t, s, a)
			marker := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"))
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_signer_no_delete")
			execSQL(t, s.db, "CREATE TRIGGER remove_snapshot_signer "+test.trigger+" BEGIN DELETE FROM sync_snapshot_signer; END")
			before := domainState(t, s)
			item, err := catalog.Create(t.Context(), token, "proof-preserve-snapshot", ItemCreate{Name: "Search source"})
			if err == nil || item.ID != "" {
				t.Fatal("search publication accepted loss of an existing snapshot signer", err)
			}
			requireDomainState(t, s, before)
			if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"), marker) ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 {
				t.Fatal("failed publication lost the snapshot signer or retained a search proof")
			}
		})
	}
}

func TestKeyedReceiptPreservesSnapshotSignerForNewCommands(t *testing.T) {
	for _, test := range []struct {
		name, trigger string
		pinned        bool
	}{
		{"marker inserted", "AFTER INSERT ON auth_keyed_receipt_secret", false},
		{"marker removed during receipt pin", "AFTER INSERT ON auth_keyed_receipt_secret", true},
		{"marker removed during audit", "AFTER INSERT ON mutation_events WHEN NEW.operation='post_media'", true},
		{"marker removed during activity", "AFTER UPDATE ON sessions", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, a, _, _, token := completedAuthFixture(t)
			statement := fmt.Sprintf("INSERT INTO sync_snapshot_signer VALUES(1,X'%x')", syncSnapshotSignerProof(a.serverSecret))
			var marker []byte
			if test.pinned {
				pinTestSnapshotSigner(t, s, a)
				marker = bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"))
				execSQL(t, s.db, "DROP TRIGGER sync_snapshot_signer_no_delete")
				statement = "DELETE FROM sync_snapshot_signer"
			}
			execSQL(t, s.db, "CREATE TRIGGER change_snapshot_during_receipt "+test.trigger+" BEGIN "+statement+"; END")
			before := domainState(t, s)
			value, err := keyedReceipt(t, a, token, "snapshot-receipt-proof")
			if !errors.Is(err, ErrAuthState) || value != (ActionAck{}) {
				t.Fatal("new keyed receipt accepted a changed snapshot signer", err)
			}
			requireDomainState(t, s, before)
			count := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer")
			if test.pinned && (count != 1 || !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"), marker)) || !test.pinned && count != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 {
				t.Fatal("failed receipt committed a changed snapshot dependency")
			}
		})
	}
}

func TestKeyedReceiptRechecksSnapshotAfterFinalCallback(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	pinTestSnapshotSigner(t, s, a)
	marker := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"))
	execSQL(t, s.db, "DROP TRIGGER sync_snapshot_signer_no_delete")
	fingerprint, err := a.mediaUploadFingerprint(t.Context(), "snapshot-final-proof", "image/png", []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	var damage func() error
	value, err := runDomainMutationWithFinalCheck(t.Context(), a, token, "snapshot-final-proof", "post_media", mediaUploadReceiptRequest{RawFileMAC: fingerprint},
		func(m *mutationTx) (ActionAck, error) {
			damage = func() error {
				_, err := m.tx.ExecContext(t.Context(), "DELETE FROM sync_snapshot_signer")
				return err
			}
			return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
		}, func() error { return damage() })
	if !errors.Is(err, ErrAuthState) || value != (ActionAck{}) ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"), marker) ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 {
		t.Fatal("final callback changed the snapshot signer or committed a receipt", err)
	}
}

func TestKeyedReceiptReplayDoesNotRequireCurrentSnapshotSigner(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	pinTestSnapshotSigner(t, s, a)
	first, err := keyedReceipt(t, a, token, "snapshot-replay-proof")
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "DROP TRIGGER sync_snapshot_signer_no_delete")
	execSQL(t, s.db, "DELETE FROM sync_snapshot_signer")
	again, err := keyedReceipt(t, a, token, "snapshot-replay-proof")
	if err != nil || again != first {
		t.Fatal("a completed original-key receipt incorrectly depended on current snapshot state", err)
	}
}

func TestSyncSnapshotMetadataPreservesSignerAfterSessionActivity(t *testing.T) {
	for _, test := range []struct{ name, guard, statement string }{
		{"changed", "sync_snapshot_signer_no_update", "UPDATE sync_snapshot_signer SET key_proof=zeroblob(32)"},
		{"removed", "sync_snapshot_signer_no_delete", "DELETE FROM sync_snapshot_signer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, a, _, token, _, signed := snapshotMetadataFixture(t)
			pinTestSnapshotSigner(t, s, a)
			marker := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"))
			execSQL(t, s.db, "DROP TRIGGER "+test.guard)
			execSQL(t, s.db, "CREATE TRIGGER damage_snapshot_during_activity AFTER UPDATE ON sessions BEGIN "+test.statement+"; END")
			value, err := a.readSyncSnapshotMetadata(t.Context(), token, signed)
			if !errors.Is(err, ErrAuthState) || value != (syncSnapshotMetadata{}) ||
				!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"), marker) {
				t.Fatal("snapshot metadata accepted signer damage during session activity", err)
			}
		})
	}
}

func TestSyncSnapshotSignerInitializationRejectsLateInjection(t *testing.T) {
	for _, test := range []struct {
		name, statement string
		row             bool
	}{
		{"signer", "INSERT INTO sync_snapshot_signer VALUES(1,zeroblob(32))", false},
		{"row proof", "INSERT INTO sync_snapshot_row_proofs VALUES('signer-snapshot',0,zeroblob(32))", true},
		{"seal", "INSERT INTO sync_snapshot_seals VALUES('signer-snapshot',0,zeroblob(32))", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := openTestStore(t)
			seedSnapshotSignerFixture(t, s, test.row)
			execSQL(t, s.db, "CREATE TRIGGER inject_snapshot_during_key_init AFTER INSERT ON security_audit BEGIN "+test.statement+"; END")
			if _, err := s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) {
				t.Fatal("key initialization accepted late snapshot dependency", err)
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_row_proofs") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_seals") != 0 {
				t.Fatal("failed key initialization leaked a key, audit or snapshot dependency")
			}
		})
	}
}

func TestSyncSnapshotSignerCanceledPinDoesNotWrite(t *testing.T) {
	s, a := snapshotSignerFixture(t, false)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = pinSyncSnapshotSigner(ctx, tx, a.serverSecret); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled pin did not fail", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer") != 0 {
		t.Fatal("canceled signer pin left a marker")
	}
}
