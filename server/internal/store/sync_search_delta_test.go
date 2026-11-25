package store

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestSyncDeltaReadsSignedSearchHistoryAfterSourceDeletion(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Earlier approved name"})
	identity := projectionPair("item", item.ID)
	earlier := requireStoredSearchProof(t, s, a, identity)
	updated, err := catalog.Patch(t.Context(), token, "signed-search-rename", item.ID, item.Revision, ItemPatch{Name: testPointer("Later approved name")})
	if err != nil {
		t.Fatal(err)
	}
	later := requireStoredSearchProof(t, s, a, identity)
	if _, err = catalog.Delete(t.Context(), token, "signed-search-delete", item.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	cursor := testSyncDeltaCursor(t, s, a, 0)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	var search []syncChange
	seen := make(map[string]bool)
	for pages := 0; ; pages++ {
		if pages >= 20 || seen[cursor] {
			t.Fatal("signed search paging did not progress")
		}
		seen[cursor] = true
		page, err := a.readSyncDeltaPage(t.Context(), token, cursor, limits)
		if err != nil {
			t.Fatal("verified frozen history was withheld", err)
		}
		for _, change := range page.Changes {
			if change.Record.EntityType == "search_doc" && change.Record.EntityID == identity {
				search = append(search, change)
			}
		}
		cursor = page.NextCursor
		if !page.HasMore {
			break
		}
	}
	if len(search) != 3 || search[0].Seq != earlier.seq || search[0].Record.EntityRevision != earlier.revision || string(search[0].Record.Payload) != earlier.payload ||
		search[1].Seq != later.seq || search[1].Record.EntityRevision != later.revision || string(search[1].Record.Payload) != later.payload ||
		search[2].Record.Operation != "delete" || search[2].Record.Payload != nil {
		t.Fatal("verified history lost its original bytes, revision or tombstone", search)
	}
	if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", earlier.seq), earlier.proof) ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", later.seq), later.proof) {
		t.Fatal("a historical read modified stored proof bytes")
	}
}

func TestSyncDeltaRejectsTamperedSearchProofAndLookahead(t *testing.T) {
	for _, test := range []string{"missing proof", "changed proof", "short proof", "changed payload", "changed identity", "changed revision", "missing signer", "changed instance", "changed epoch"} {
		t.Run(test, func(t *testing.T) {
			s, catalog, a, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Approved source"})
			identity := projectionPair("item", item.ID)
			proof := requireStoredSearchProof(t, s, a, identity)
			switch test {
			case "missing proof":
				execSQL(t, s.db, "DROP TRIGGER sync_search_proofs_no_delete")
				execSQL(t, s.db, "DELETE FROM sync_search_proofs WHERE seq=?", proof.seq)
			case "changed proof":
				execSQL(t, s.db, "DROP TRIGGER sync_search_proofs_no_update")
				execSQL(t, s.db, "UPDATE sync_search_proofs SET proof=zeroblob(32) WHERE seq=?", proof.seq)
			case "short proof":
				execSQL(t, s.db, "DROP TRIGGER sync_search_proofs_no_update")
				execSQL(t, s.db, "PRAGMA ignore_check_constraints=ON")
				defer execSQL(t, s.db, "PRAGMA ignore_check_constraints=OFF")
				execSQL(t, s.db, "UPDATE sync_search_proofs SET proof=zeroblob(31) WHERE seq=?", proof.seq)
			case "changed payload":
				execSQL(t, s.db, "DROP TRIGGER sync_changes_no_update")
				execSQL(t, s.db, "UPDATE sync_changes SET projection_json=json_set(projection_json,'$.approved_text','Private OCR') WHERE seq=?", proof.seq)
			case "changed identity":
				execSQL(t, s.db, "DROP TRIGGER sync_changes_no_update")
				execSQL(t, s.db, "UPDATE sync_changes SET entity_id=?,projection_json=json_set(projection_json,'$.entity_id','other') WHERE seq=?", projectionPair("item", "other"), proof.seq)
			case "changed revision":
				execSQL(t, s.db, "DROP TRIGGER sync_changes_no_update")
				execSQL(t, s.db, "UPDATE sync_changes SET entity_revision=entity_revision+1 WHERE seq=?", proof.seq)
			case "missing signer":
				execSQL(t, s.db, "DROP TRIGGER sync_search_signer_no_delete")
				execSQL(t, s.db, "DELETE FROM sync_search_signer")
			case "changed instance":
				execSQL(t, s.db, "UPDATE sync_state SET server_instance_id='ffffffffffffffffffffffffffffffff'")
			case "changed epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='another-dataset-epoch'")
				_, token = login(t, a)
			}
			before := domainState(t, s)
			cursor := testSyncDeltaCursor(t, s, a, 0)
			page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
			if !errors.Is(err, errSyncSearchUnproven) || !reflect.DeepEqual(page, syncDeltaPage{}) {
				t.Fatal("tampered search lookahead returned a prefix", page, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestSyncDeltaRejectsProofAttachedToNonSearchOrTombstone(t *testing.T) {
	for _, kind := range []string{"location", "search tombstone"} {
		t.Run(kind, func(t *testing.T) {
			s, locations, a, token := locationFixture(t)
			location := createLocation(t, locations, token, "PROOF-PARENT", nil)
			seq := scalar[int64](t, s.db, "SELECT seq FROM sync_changes WHERE entity_type='location' AND entity_id=?", location.ID)
			if kind == "search tombstone" {
				execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',?,'delete',1,NULL,'2026-01-01T00:00:00Z')`, projectionPair("item", "deleted"))
				seq = scalar[int64](t, s.db, "SELECT MAX(seq) FROM sync_changes")
			}
			execSQL(t, s.db, "DROP TRIGGER sync_search_proofs_require_upsert")
			execSQL(t, s.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(32))", seq)
			before := domainState(t, s)
			cursor := testSyncDeltaCursor(t, s, a, seq-1)
			page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
			if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncDeltaPage{}) {
				t.Fatal("an unexpected proof was ignored", page, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestSyncDeltaRejectsProofCopiedToAnotherSequence(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Approved source"})
	identity := projectionPair("item", item.ID)
	original := requireStoredSearchProof(t, s, a, identity)
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',?,'upsert',?,?,'2026-01-01T00:00:00Z')`, identity, original.revision, original.payload)
	copied := scalar[int64](t, s.db, "SELECT MAX(seq) FROM sync_changes")
	execSQL(t, s.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,?)", copied, original.proof)
	cursor := testSyncDeltaCursor(t, s, a, original.seq)
	before := domainState(t, s)
	page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
	if !errors.Is(err, errSyncSearchUnproven) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("a proof copied to a different sequence authorized search text", page, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncDeltaChecksSearchProofAfterSessionActivity(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Approved source"})
	proof := requireStoredSearchProof(t, s, a, projectionPair("item", item.ID))
	execSQL(t, s.db, "DROP TRIGGER sync_search_proofs_no_update")
	execSQL(t, s.db, `CREATE TRIGGER corrupt_proof_during_activity AFTER UPDATE ON sessions BEGIN
 UPDATE sync_search_proofs SET proof=zeroblob(32); END`)
	before := domainState(t, s)
	cursor := testSyncDeltaCursor(t, s, a, 0)
	page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
	if !errors.Is(err, errSyncSearchUnproven) || !reflect.DeepEqual(page, syncDeltaPage{}) ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", proof.seq), proof.proof) {
		t.Fatal("late proof corruption exposed a prefix or committed", page, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncDeltaWithholdsUnprovenSearchInjectedByActivity(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Approved source"})
	proof := requireStoredSearchProof(t, s, a, projectionPair("item", item.ID))
	execSQL(t, s.db, `CREATE TRIGGER inject_private_search AFTER UPDATE ON sessions BEGIN
 INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',json_array('item','injected'),'upsert',1,'{"entity_type":"item","entity_id":"injected","approved_text":"Private OCR"}','2026-01-01T00:00:00Z'); END`)
	before := domainState(t, s)
	cursor := testSyncDeltaCursor(t, s, a, proof.seq)
	page, err := a.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 10, PayloadBytes: 4096, PageBytes: 8192})
	if !errors.Is(err, errSyncSearchUnproven) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("unproven search inserted during activity was exposed", page, err)
	}
	requireDomainState(t, s, before)
}
