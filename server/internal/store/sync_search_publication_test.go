package store

import (
	"bytes"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type storedSearchProof struct {
	seq, revision int64
	payload       string
	proof         []byte
}

func requireStoredSearchProof(t *testing.T, s *Store, a *Auth, identity string) storedSearchProof {
	t.Helper()
	var value storedSearchProof
	err := s.db.QueryRowContext(t.Context(), `SELECT c.seq,c.entity_revision,c.projection_json,p.proof
 FROM sync_changes c JOIN sync_search_proofs p ON p.seq=c.seq
 WHERE c.entity_type='search_doc' AND c.entity_id=? AND c.operation='upsert' ORDER BY c.seq DESC LIMIT 1`, identity).
		Scan(&value.seq, &value.revision, &value.payload, &value.proof)
	if err != nil {
		t.Fatal("published search has no stored proof", err)
	}
	instance := scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state")
	epoch := scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state")
	expected, err := a.syncSearchProof(t.Context(), instance, epoch, identity, value.seq, value.revision, []byte(value.payload))
	if err != nil || !bytes.Equal(value.proof, expected[:]) {
		t.Fatal("stored proof does not bind the exact frozen search record", err)
	}
	return value
}

func TestSearchPublicationSignsActualReviewedSourceAndPreservesHistory(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "InitialName"})
	itemIdentity := projectionPair("item", item.ID)
	original := requireStoredSearchProof(t, s, a, itemIdentity)
	inventory, err := NewInventory(a)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := inventory.CreateUnit(t.Context(), token, "proof-unit-create", item.ID, item.Revision, UnitCreate{SerialNumber: testPointer("ActualSerial")})
	if err != nil {
		t.Fatal(err)
	}
	for _, review := range []struct {
		id, state, raw string
		reviewed       *string
	}{
		{"proof-reviewed", "accepted", "ApprovedKeyword", testPointer("2026-11-18T00:00:00Z")},
		{"proof-pending", "needs_review", "PrivatePendingOCR", nil},
		{"proof-rejected", "rejected", "PrivateRejectedOCR", testPointer("2026-11-18T00:00:00Z")},
	} {
		execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,reviewed_at,created_at,updated_at)
 VALUES(?,?,'keyword',?,?,?,'2026','2026')`, review.id, item.ID, review.state, review.raw, review.reviewed)
	}
	owner := getCatalogItem(t, catalog, token, item.ID)
	alias, err := catalog.CreateAlias(t.Context(), token, "proof-reviewed-alias", item.ID, owner.Revision, "ApprovedAlias")
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{itemIdentity, projectionPair("unit", unit.ID)} {
		current := requireStoredSearchProof(t, s, a, identity)
		for _, term := range []string{"InitialName", "ApprovedKeyword", "ApprovedAlias"} {
			if !strings.Contains(current.payload, term) {
				t.Fatal("signed payload omitted actual approved source", term)
			}
		}
		for _, term := range []string{"PrivatePendingOCR", "PrivateRejectedOCR"} {
			if strings.Contains(current.payload, term) {
				t.Fatal("signed payload included unreviewed or rejected text", term)
			}
		}
		if identity == itemIdentity && strings.Contains(current.payload, "ActualSerial") || identity != itemIdentity && !strings.Contains(current.payload, "ActualSerial") {
			t.Fatal("signed search did not preserve item/unit source scope")
		}
	}
	if scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=?", original.seq) != original.payload ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", original.seq), original.proof) {
		t.Fatal("later source changes altered the original frozen bytes or proof")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 {
		t.Fatal("search publication did not independently pin its signer")
	}
	count := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs")
	again, err := catalog.CreateAlias(t.Context(), token, "proof-reviewed-alias", item.ID, owner.Revision, "ApprovedAlias")
	if err != nil || !reflect.DeepEqual(again, alias) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != count {
		t.Fatal("original-key replay changed a proof or reply", err)
	}
	cursor := testSyncDeltaCursor(t, s, a, 0)
	if page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 100, PayloadBytes: 4096, PageBytes: 32768}); !errors.Is(err, errSyncSearchUnproven) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("private delta exposed a search upsert before reader verification existed", page, err)
	}
}

func TestSearchPublicationRollsBackLateSourceProofAndSignerChanges(t *testing.T) {
	for _, test := range []struct {
		name, timing, table, statement, removeGuard string
	}{
		{"ignored signer", "BEFORE", "sync_search_signer", "SELECT RAISE(IGNORE)", ""},
		{"ignored proof", "BEFORE", "sync_search_proofs", "SELECT RAISE(IGNORE)", ""},
		{"instance after source", "AFTER", "items", "UPDATE sync_state SET server_instance_id='ffffffffffffffffffffffffffffffff'", ""},
		{"retention floor after source", "AFTER", "items", "UPDATE sync_state SET min_retained_seq=1", ""},
		{"source after proof", "AFTER", "sync_search_proofs", "UPDATE items SET name='LateSource'", ""},
		{"key after proof", "AFTER", "sync_search_proofs", "UPDATE auth_server_secret SET key_bytes=zeroblob(32)", ""},
		{"source after receipt", "AFTER", "api_idempotency", "UPDATE items SET name='LateSource'", ""},
		{"source after audit", "AFTER", "mutation_events", "UPDATE items SET name='LateSource'", ""},
		{"source after activity", "AFTER UPDATE", "sessions", "UPDATE items SET name='LateSource'", ""},
		{"instance after activity", "AFTER UPDATE", "sessions", "UPDATE sync_state SET server_instance_id='ffffffffffffffffffffffffffffffff'", ""},
		{"proof after audit", "AFTER", "mutation_events", "UPDATE sync_search_proofs SET proof=zeroblob(32)", "sync_search_proofs_no_update"},
		{"signer changed after audit", "AFTER", "mutation_events", "UPDATE sync_search_signer SET key_proof=zeroblob(32)", "sync_search_signer_no_update"},
		{"signer removed after audit", "AFTER", "mutation_events", "DELETE FROM sync_search_signer", "sync_search_signer_no_delete"},
		{"frozen after audit", "AFTER", "mutation_events", "UPDATE sync_changes SET projection_json='{}' WHERE entity_type='search_doc'", "sync_changes_no_update"},
		{"current after audit", "AFTER", "mutation_events", "UPDATE current_projections SET payload_json='{}' WHERE entity_type='search_doc'", ""},
		{"current timestamp after audit", "AFTER", "mutation_events", "UPDATE current_projections SET updated_at='2000-01-01T00:00:00Z' WHERE entity_type='search_doc'", ""},
		{"counter after audit", "AFTER", "mutation_events", "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='search_doc'; UPDATE current_projections SET entity_revision=entity_revision+1 WHERE entity_type='search_doc'", ""},
		{"pruned after audit", "AFTER", "mutation_events", "DELETE FROM sync_changes WHERE entity_type='search_doc'", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, catalog, a, token := catalogFixture(t)
			if test.removeGuard != "" {
				execSQL(t, s.db, "DROP TRIGGER "+test.removeGuard)
			}
			event := test.timing
			if !strings.Contains(event, "UPDATE") {
				event += " INSERT"
			}
			execSQL(t, s.db, "CREATE TRIGGER damage_search "+event+" ON "+test.table+" BEGIN "+test.statement+"; END")
			before, key := domainState(t, s), bytes.Clone(a.serverSecret)
			result, err := catalog.Create(t.Context(), token, "proof-failure-key", ItemCreate{Name: "ExpectedSource"})
			if err == nil || result.ID != "" {
				t.Fatal("search publication accepted a late failure", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 ||
				!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret"), key) {
				t.Fatal("failed publication leaked a proof, signer or key change")
			}
			execSQL(t, s.db, "DROP TRIGGER damage_search")
			created, err := catalog.Create(t.Context(), token, "proof-failure-key", ItemCreate{Name: "ExpectedSource"})
			if err != nil {
				t.Fatal("rolled-back command could not be retried explicitly", err)
			}
			requireStoredSearchProof(t, s, a, projectionPair("item", created.ID))
		})
	}
}

func TestSearchPublicationPreservesExistingMediaSigner(t *testing.T) {
	for _, test := range []struct{ name, trigger string }{
		{"source write", "AFTER INSERT ON items"},
		{"search cache write", "AFTER INSERT ON search_documents"},
		{"search signer pin", "AFTER INSERT ON sync_search_signer"},
		{"audit write", "AFTER INSERT ON mutation_events WHEN NEW.operation='post_items'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, catalog, a, token := catalogFixture(t)
			if _, err := keyedReceipt(t, a, token, "existing-media-receipt"); err != nil {
				t.Fatal(err)
			}
			marker := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM auth_keyed_receipt_secret"))
			execSQL(t, s.db, "DROP TRIGGER auth_keyed_receipt_secret_no_delete")
			execSQL(t, s.db, "CREATE TRIGGER remove_media_signer "+test.trigger+" BEGIN DELETE FROM auth_keyed_receipt_secret; END")
			before := domainState(t, s)
			item, err := catalog.Create(t.Context(), token, "proof-preserve-media", ItemCreate{Name: "Search source"})
			if err == nil || item.ID != "" {
				t.Fatal("search publication accepted loss of an existing Media signer", err)
			}
			requireDomainState(t, s, before)
			if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM auth_keyed_receipt_secret"), marker) ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 {
				t.Fatal("failed publication lost a receipt dependency or retained a search proof")
			}
		})
	}
}

func TestSearchPublicationDoesNotRecreateAnExistingSigner(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	pinTestSyncSearchSigner(t, s, a)
	original := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_search_signer"))
	execSQL(t, s.db, "DROP TRIGGER sync_search_signer_no_delete")
	execSQL(t, s.db, "CREATE TRIGGER remove_search_signer AFTER INSERT ON items BEGIN DELETE FROM sync_search_signer; END")
	before := domainState(t, s)
	item, err := catalog.Create(t.Context(), token, "proof-preserve-search-signer", ItemCreate{Name: "Search source"})
	if err == nil || item.ID != "" {
		t.Fatal("search publication recreated an existing signer after its deletion", err)
	}
	requireDomainState(t, s, before)
	if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_search_signer"), original) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 {
		t.Fatal("failed publication changed a signer or left a proof")
	}
}

func TestSearchPublicationAllowsARealKeyedMediaPinInTheSameMutation(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	key := "proof-media-and-search"
	fingerprint, err := a.mediaUploadFingerprint(t.Context(), key, "image/png", []byte("original raw image"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runDomainMutation(t.Context(), a, token, key, "post_media", mediaUploadReceiptRequest{RawFileMAC: fingerprint}, func(m *mutationTx) (ActionAck, error) {
		if _, err := m.tx.ExecContext(m.ctx, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at)
 VALUES('media-search-item','ReviewedSource','individual',?,?)`, stamp(m.now), stamp(m.now)); err != nil {
			return ActionAck{}, err
		}
		if err := m.touchItemSearch("media-search-item", true); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	})
	if err != nil || !result.OK || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 1 {
		t.Fatal("a real keyed Media request could not pin both dependencies", err)
	}
	requireStoredSearchProof(t, s, a, projectionPair("item", "media-search-item"))
}

func TestSearchPublicationRejectsReviewChangesAfterProjection(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Review owner"})
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,created_at,updated_at)
 VALUES('pending-review',?,'keyword','needs_review','PrivateOCR','2026','2026')`, item.ID)
	execSQL(t, s.db, `CREATE TRIGGER approve_during_activity AFTER UPDATE ON sessions BEGIN
 UPDATE ai_review_entries SET state='accepted',reviewed_at='2026-11-18T00:00:00Z' WHERE id='pending-review'; END`)
	before := domainState(t, s)
	original := requireStoredSearchProof(t, s, a, projectionPair("item", item.ID))
	if alias, err := catalog.CreateAlias(t.Context(), token, "proof-late-review", item.ID, item.Revision, "Reviewed alias"); !errors.Is(err, ErrDomainState) || alias.ID != "" {
		t.Fatal("publication signed source changed by a late review", err)
	}
	requireDomainState(t, s, before)
	if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", original.seq), original.proof) || strings.Contains(original.payload, "PrivateOCR") {
		t.Fatal("failed review change contaminated an earlier signed projection")
	}
}

func TestSearchPublicationLeavesLegacyChangesUnproven(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	seq, payload := seedUnprovenSearchChange(t, s)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "New source"})
	requireStoredSearchProof(t, s, a, projectionPair("item", item.ID))
	if scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=?", seq) != payload ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs WHERE seq=?", seq) != 0 {
		t.Fatal("a new publication changed or guessed a legacy search proof")
	}
	cursor := testSyncDeltaCursor(t, s, a, 0)
	if page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 100, PayloadBytes: 4096, PageBytes: 32768}); !errors.Is(err, errSyncSearchUnproven) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("a new signer authorized unproven legacy search", page, err)
	}
}

func TestSearchPublicationDoesNotClaimUnknownExistingProof(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	seq, payload := seedUnprovenSearchChange(t, s)
	execSQL(t, s.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(32))", seq)
	before, key := domainState(t, s), bytes.Clone(a.serverSecret)
	if item, err := catalog.Create(t.Context(), token, "proof-unknown-prior", ItemCreate{Name: "New source"}); !errors.Is(err, ErrDomainState) || item.ID != "" {
		t.Fatal("publication guessed a signer for an unknown stored proof", err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 1 ||
		scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=?", seq) != payload ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret"), key) {
		t.Fatal("failed publication changed unknown proof state")
	}
}

func TestSearchPublicationRequiresActualUnitAuthority(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "ActualOwner"})
	execSQL(t, s.db, `INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at)
 VALUES('unproven-unit',?,'ASSET','2026','2026')`, item.ID)
	before := domainState(t, s)
	proofCount := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs")
	if result, err := catalog.CreateAlias(t.Context(), token, "proof-missing-unit", item.ID, item.Revision, "Alias"); !errors.Is(err, ErrDomainState) || result.ID != "" {
		t.Fatal("search publication accepted a Unit without its independent counter", err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != proofCount {
		t.Fatal("failed Unit authority check leaked a proof")
	}
	requireStoredSearchProof(t, s, a, projectionPair("item", item.ID))
}

func TestSearchPublicationChecksSourceAfterTheLastOptionalCheck(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	before := domainState(t, s)
	var tx *sql.Tx
	checks := 0
	check := func() error {
		checks++
		if checks == 2 {
			_, err := tx.ExecContext(t.Context(), "UPDATE items SET name='ChangedAfterFinalCheck' WHERE id='last-check-item'")
			return err
		}
		return nil
	}
	result, err := runDomainMutationWithFinalCheck(t.Context(), a, token, "proof-last-check", "test_last_check", struct{}{}, func(m *mutationTx) (ActionAck, error) {
		tx = m.tx
		if _, err := m.tx.ExecContext(m.ctx, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at)
 VALUES('last-check-item','ReviewedSource','individual',?,?)`, stamp(m.now), stamp(m.now)); err != nil {
			return ActionAck{}, err
		}
		if err := m.touchItemSearch("last-check-item", true); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	}, check)
	if err == nil || result != (ActionAck{}) || checks != 2 {
		t.Fatal("a source changed by the final callback was signed", checks, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 {
		t.Fatal("failed final validation retained a signer or proof")
	}
}

func TestSearchPublicationBindsNewDatasetEpochWithoutRewritingOldProof(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	oldItem := createCatalogItem(t, catalog, token, ItemCreate{Name: "Old dataset"})
	oldIdentity := projectionPair("item", oldItem.ID)
	old := requireStoredSearchProof(t, s, a, oldIdentity)
	instance := scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state")
	oldEpoch := scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state")
	newEpoch := "simulated-new-dataset-epoch"
	execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch=? WHERE singleton=1", newEpoch)
	if _, err := catalog.Create(t.Context(), token, "old-epoch-denied", ItemCreate{Name: "Old session"}); !errors.Is(err, ErrAuthentication) {
		t.Fatal("an old-epoch session published after dataset rotation", err)
	}
	_, fresh := login(t, a)
	newItem := createCatalogItem(t, catalog, fresh, ItemCreate{Name: "New dataset"})
	requireStoredSearchProof(t, s, a, projectionPair("item", newItem.ID))
	prior, err := a.syncSearchProof(t.Context(), instance, oldEpoch, oldIdentity, old.seq, old.revision, []byte(old.payload))
	if err != nil || !bytes.Equal(prior[:], old.proof) {
		t.Fatal("old proof lost its original dataset binding", err)
	}
	rotated, err := a.syncSearchProof(t.Context(), instance, newEpoch, oldIdentity, old.seq, old.revision, []byte(old.payload))
	if err != nil || bytes.Equal(rotated[:], old.proof) ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", old.seq), old.proof) {
		t.Fatal("old proof was rewritten or accepted for the new dataset", err)
	}
}

func TestSearchPublicationReplaysAfterOwnerDeletionWithoutNewProof(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	input := ItemCreate{Name: "ReplayedAfterDelete"}
	first, err := catalog.Create(t.Context(), token, "proof-original-create", input)
	if err != nil {
		t.Fatal(err)
	}
	original := requireStoredSearchProof(t, s, a, projectionPair("item", first.ID))
	if _, err = catalog.Delete(t.Context(), token, "proof-delete-item", first.ID, first.Revision); err != nil {
		t.Fatal(err)
	}
	count := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs")
	tombstones := scalar[int](t, s.db, `SELECT COUNT(*) FROM sync_changes c LEFT JOIN sync_search_proofs p ON p.seq=c.seq
 WHERE c.entity_type='search_doc' AND c.entity_id=? AND c.operation='delete' AND c.projection_json IS NULL AND p.seq IS NULL`, projectionPair("item", first.ID))
	replay, err := catalog.Create(t.Context(), token, "proof-original-create", input)
	if err != nil || !reflect.DeepEqual(first, replay) || tombstones != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != count ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", original.seq), original.proof) {
		t.Fatal("replay or tombstone changed the original signed history", err)
	}
}
