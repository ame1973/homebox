package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSyncSearchBackfillPublishesMissingItemFromActualApprovedSource(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Actual source", Description: "Actual description"})
	identity := projectionPair("item", item.ID)
	original := requireStoredSearchProof(t, s, a, identity)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	execSQL(t, s.db, "UPDATE search_documents SET title='Poisoned cache',reviewed_ai='Private OCR' WHERE item_id=?", item.ID)
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,reviewed_at,created_at,updated_at)
 VALUES('approved',?,'keyword','accepted','Reviewed evidence','2026-11-18T00:00:00Z','2026','2026'),
 ('pending',?,'keyword','needs_review','Unreviewed private OCR',NULL,'2026','2026')`, item.ID, item.ID)
	_, grant := issueGrant(t, a, token, "reauth-backfill-item")
	before := domainState(t, s)
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-item-original", request)
	if err != nil || !ack.OK || ack.Revision != original.revision+1 {
		t.Fatal("missing Item search was not materialized with its retained counter", ack, err)
	}
	newProof := requireStoredSearchProof(t, s, a, identity)
	if newProof.revision != ack.Revision || !strings.Contains(newProof.payload, "Actual source") || !strings.Contains(newProof.payload, "Reviewed evidence") ||
		strings.Contains(newProof.payload, "Poisoned cache") || strings.Contains(newProof.payload, "Private OCR") || strings.Contains(newProof.payload, "Unreviewed private OCR") {
		t.Fatal("backfill copied cache/private text or omitted reviewed source", newProof.payload)
	}
	if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", original.seq), original.proof) ||
		scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=?", original.seq) != original.payload {
		t.Fatal("backfill rewrote earlier frozen search history")
	}
	after := domainState(t, s)
	for _, table := range []string{"items", "item_units", "stock_balances", "purchases", "purchase_lines", "inventory_events", "ai_review_entries", "ocr_results"} {
		if before[table] != after[table] {
			t.Fatal("search backfill changed authoritative source", table)
		}
	}
	if getCatalogItem(t, catalog, token, item.ID).Revision != item.Revision {
		t.Fatal("search backfill changed the Item revision")
	}
}

func TestSyncSearchBackfillPublishesOnlyTheSelectedUnit(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	inventory, err := NewInventory(a)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Individual owner"})
	unit, err := inventory.CreateUnit(t.Context(), token, "backfill-create-unit", item.ID, item.Revision, UnitCreate{SerialNumber: testPointer("Exact serial")})
	if err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, catalog, token, item.ID)
	unitIdentity, itemIdentity := projectionPair("unit", unit.ID), projectionPair("item", item.ID)
	original := requireStoredSearchProof(t, s, a, unitIdentity)
	itemPayload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", itemIdentity)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", unitIdentity)
	_, grant := issueGrant(t, a, token, "reauth-backfill-unit")
	before := domainState(t, s)
	request := syncSearchBackfillRequest{EntityType: "unit", EntityID: unit.ID, ItemID: item.ID, ItemRevision: item.Revision, UnitRevision: unit.Revision}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-unit-original", request)
	if err != nil || !ack.OK || ack.Revision != original.revision+1 {
		t.Fatal("missing Unit search was not materialized", ack, err)
	}
	unitProof := requireStoredSearchProof(t, s, a, unitIdentity)
	if !strings.Contains(unitProof.payload, "Exact serial") || scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", itemIdentity) != itemPayload {
		t.Fatal("unit backfill lost its exact scope or republished its Item")
	}
	after := domainState(t, s)
	for _, table := range []string{"items", "item_units", "stock_balances", "inventory_events", "purchases", "purchase_lines"} {
		if before[table] != after[table] {
			t.Fatal("unit search backfill changed physical or financial source", table)
		}
	}
	if getCatalogItem(t, catalog, token, item.ID).Revision != item.Revision || scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", unit.ID) != unit.Revision {
		t.Fatal("search backfill advanced Item or Unit authority")
	}
}

func TestSyncSearchBackfillChecksOwnerProofsAndInput(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	inventory, err := NewInventory(a)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	other := createCatalogItem(t, catalog, token, ItemCreate{Name: "Other"})
	unit, err := inventory.CreateUnit(t.Context(), token, "backfill-proof-unit", item.ID, item.Revision, UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, catalog, token, item.ID)
	valid := syncSearchBackfillRequest{EntityType: "unit", EntityID: unit.ID, ItemID: item.ID, ItemRevision: item.Revision, UnitRevision: unit.Revision}
	_, grant := issueGrant(t, a, token, "reauth-backfill-invalid")
	for _, test := range []struct {
		name string
		edit func(*syncSearchBackfillRequest)
		want error
	}{
		{"unknown kind", func(v *syncSearchBackfillRequest) { v.EntityType = "other" }, ErrDomainInput},
		{"missing id", func(v *syncSearchBackfillRequest) { v.EntityID = "" }, ErrDomainInput},
		{"missing Item proof", func(v *syncSearchBackfillRequest) { v.ItemRevision = 0 }, ErrPreconditionRequired},
		{"missing Unit proof", func(v *syncSearchBackfillRequest) { v.UnitRevision = 0 }, ErrPreconditionRequired},
		{"stale Item proof", func(v *syncSearchBackfillRequest) { v.ItemRevision++ }, ErrRevisionConflict},
		{"stale Unit proof", func(v *syncSearchBackfillRequest) { v.UnitRevision++ }, ErrRevisionConflict},
		{"different owner", func(v *syncSearchBackfillRequest) { v.ItemID, v.ItemRevision = other.ID, other.Revision }, ErrDomainInput},
		{"item with Unit proof", func(v *syncSearchBackfillRequest) { v.EntityType, v.EntityID = "item", v.ItemID }, ErrDomainInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.edit(&request)
			before := domainState(t, s)
			ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-invalid-key", request)
			if !errors.Is(err, test.want) || ack != (ActionAck{}) {
				t.Fatal("invalid search backfill input or proof was accepted", ack, err)
			}
			requireDomainState(t, s, before)
		})
	}
	if ack, err := a.backfillSyncSearch(t.Context(), "invalid-session", grant, "backfill-invalid-session", valid); !errors.Is(err, ErrAuthentication) || ack != (ActionAck{}) {
		t.Fatal("unauthenticated backfill returned an acknowledgement", ack, err)
	}
}

func TestSyncSearchBackfillRejectsMissingOrRegressedSearchCounters(t *testing.T) {
	for _, test := range []string{"missing with retained history", "regressed behind history", "current regressed behind history", "missing after retention"} {
		t.Run(test, func(t *testing.T) {
			s, catalog, a, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Counter owner"})
			identity := projectionPair("item", item.ID)
			if strings.Contains(test, "regressed behind history") {
				var err error
				item, err = catalog.Patch(t.Context(), token, "backfill-counter-patch", item.ID, item.Revision, ItemPatch{Name: testPointer("Later source")})
				if err != nil {
					t.Fatal(err)
				}
			}
			if test == "current regressed behind history" {
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				for _, statement := range []string{
					"UPDATE projection_revisions SET revision=1 WHERE entity_type='search_doc' AND entity_id=?",
					"UPDATE current_projections SET entity_revision=1 WHERE entity_type='search_doc' AND entity_id=?",
				} {
					if _, err = tx.ExecContext(t.Context(), statement, identity); err != nil {
						t.Fatal(err)
					}
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else {
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
				if test == "regressed behind history" {
					execSQL(t, s.db, "UPDATE projection_revisions SET revision=1 WHERE entity_type='search_doc' AND entity_id=?", identity)
				} else {
					execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", identity)
				}
			}
			if test == "missing after retention" {
				last := scalar[int64](t, s.db, "SELECT MAX(seq) FROM sync_changes")
				execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq<=?", last)
				execSQL(t, s.db, "UPDATE sync_state SET min_retained_seq=?", last)
			}
			_, grant := issueGrant(t, a, token, "reauth-backfill-counter")
			before := domainState(t, s)
			request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
			ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-bad-counter", request)
			if !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
				t.Fatal("backfill accepted an unproven or regressed search counter", ack, err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NULL", authDigest(grant)) != 1 {
				t.Fatal("failed counter validation consumed the grant")
			}
		})
	}
}

func TestSyncSearchBackfillCreatesFirstCounterOnlyWithoutRetainedHistory(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at)
 VALUES('legacy-without-search','Legacy source','individual','2026','2026')`)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item','legacy-without-search',1)")
	_, grant := issueGrant(t, a, token, "reauth-backfill-first-counter")
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: "legacy-without-search", ItemID: "legacy-without-search", ItemRevision: 1}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-first-counter", request)
	if err != nil || !ack.OK || ack.Revision != 1 {
		t.Fatal("first safe search counter was not created", ack, err)
	}
	requireStoredSearchProof(t, s, a, projectionPair("item", request.EntityID))
}

func TestSyncSearchBackfillPreservesExactLegacyIdentities(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	itemID, unitID := "item/\x00家庭\n", "unit\\\x00測試\r\n"
	execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at)
 VALUES(?,'Legacy owner','individual','2026','2026')`, itemID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", itemID)
	execSQL(t, s.db, `INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at)
 VALUES(?,?,'Legacy asset','2026','2026')`, unitID, itemID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('unit',?,1)", unitID)
	for _, request := range []syncSearchBackfillRequest{
		{EntityType: "item", EntityID: itemID, ItemID: itemID, ItemRevision: 1},
		{EntityType: "unit", EntityID: unitID, ItemID: itemID, ItemRevision: 1, UnitRevision: 1},
	} {
		_, grant := issueGrant(t, a, token, "reauth-legacy-"+request.EntityType)
		ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-legacy-"+request.EntityType, request)
		if err != nil || !ack.OK || ack.Revision != 1 {
			t.Fatal("legacy identity could not be backfilled exactly", request.EntityType, ack, err)
		}
		proof := requireStoredSearchProof(t, s, a, projectionPair(request.EntityType, request.EntityID))
		var value searchData
		if err = json.Unmarshal([]byte(proof.payload), &value); err != nil || value.EntityType != request.EntityType || value.EntityID != request.EntityID {
			t.Fatal("backfill changed a legacy identity", request.EntityType, err)
		}
	}
}

func TestSyncSearchBackfillChecksRetainedSnapshotRevisions(t *testing.T) {
	for _, test := range []struct {
		name                      string
		counter, frozen           int64
		otherEpoch, otherInstance bool
		want                      error
	}{
		{"missing counter", 0, 1, false, false, ErrDomainState},
		{"counter behind snapshot", 1, 3, false, false, ErrDomainState},
		{"counter matches snapshot", 3, 3, false, false, nil},
		{"snapshot from another epoch", 1, 3, true, false, nil},
		{"snapshot from another instance", 1, 3, false, true, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, a, _, _, token := completedAuthFixture(t)
			execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at)
 VALUES('snapshot-source','Legacy source','individual','2026','2026')`)
			execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item','snapshot-source',1)")
			identity := projectionPair("item", "snapshot-source")
			if test.counter != 0 {
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('search_doc',?,?)", identity, test.counter)
			}
			execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'legacy-snapshot',server_instance_id,dataset_epoch,1,0,'2026-11-04T12:00:00Z','2026-11-04T12:15:00Z','ready' FROM sync_state`)
			execSQL(t, s.db, `INSERT INTO sync_snapshot_rows(snapshot_id,ordinal,entity_type,entity_id,entity_revision,payload_json)
 VALUES('legacy-snapshot',0,'search_doc',?,?,?)`, identity, test.frozen, `{"entity_type":"item","entity_id":"snapshot-source","approved_text":"Legacy source"}`)
			if test.otherEpoch {
				execSQL(t, s.db, "UPDATE sync_snapshots SET dataset_epoch='previous-epoch' WHERE id='legacy-snapshot'")
			}
			if test.otherInstance {
				execSQL(t, s.db, "UPDATE sync_snapshots SET server_instance_id='ffffffffffffffffffffffffffffffff' WHERE id='legacy-snapshot'")
			}
			_, grant := issueGrant(t, a, token, "reauth-backfill-snapshot")
			before := domainState(t, s)
			request := syncSearchBackfillRequest{EntityType: "item", EntityID: "snapshot-source", ItemID: "snapshot-source", ItemRevision: 1}
			ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-snapshot-counter", request)
			if test.want != nil {
				if !errors.Is(err, test.want) || ack != (ActionAck{}) {
					t.Fatal("backfill ignored retained snapshot revision", ack, err)
				}
				requireDomainState(t, s, before)
				if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NULL", authDigest(grant)) != 1 {
					t.Fatal("failed snapshot check consumed the grant")
				}
			} else if err != nil || !ack.OK || ack.Revision != test.counter+1 {
				t.Fatal("a matching retained snapshot prevented advancing its counter", ack, err)
			} else {
				requireStoredSearchProof(t, s, a, identity)
			}
		})
	}
}

func TestSyncSearchBackfillRejectsSnapshotInjectedDuringActivity(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Snapshot source"})
	identity := projectionPair("item", item.ID)
	execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'late-snapshot',server_instance_id,dataset_epoch,1,0,'2026-11-04T12:00:00Z','2026-11-04T12:15:00Z','ready' FROM sync_state`)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	_, grant := issueGrant(t, a, token, "reauth-backfill-late-snapshot")
	execSQL(t, s.db, `CREATE TRIGGER inject_backfill_snapshot AFTER UPDATE ON sessions BEGIN
 INSERT INTO sync_snapshot_rows(snapshot_id,ordinal,entity_type,entity_id,entity_revision,payload_json)
 SELECT 'late-snapshot',0,entity_type,entity_id,entity_revision+10,projection_json FROM sync_changes WHERE entity_type='search_doc' ORDER BY seq LIMIT 1; END`)
	before := domainState(t, s)
	proofCount := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs")
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-late-snapshot", request)
	if !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
		t.Fatal("backfill ignored a late higher snapshot revision", ack, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_rows WHERE snapshot_id='late-snapshot'") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != proofCount ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NULL", authDigest(grant)) != 1 {
		t.Fatal("failed snapshot validation leaked a row, proof or grant consumption")
	}
}

func TestSyncSearchBackfillReusesRetainedCounterAfterPruning(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Retained counter"})
	identity := projectionPair("item", item.ID)
	original := requireStoredSearchProof(t, s, a, identity)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	last := scalar[int64](t, s.db, "SELECT MAX(seq) FROM sync_changes")
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq<=?", last)
	execSQL(t, s.db, "UPDATE sync_state SET min_retained_seq=?", last)
	_, grant := issueGrant(t, a, token, "reauth-backfill-retained-counter")
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-retained-counter", request)
	if err != nil || !ack.OK || ack.Revision != original.revision+1 {
		t.Fatal("a valid retained search counter could not advance after pruning", ack, err)
	}
	newProof := requireStoredSearchProof(t, s, a, identity)
	if newProof.seq <= last {
		t.Fatal("backfill reused a pruned sequence")
	}
}

func TestSyncSearchBackfillRejectsLateHigherSearchHistory(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Current source"})
	identity := projectionPair("item", item.ID)
	_, grant := issueGrant(t, a, token, "reauth-backfill-late-history")
	execSQL(t, s.db, `CREATE TRIGGER inject_backfill_history AFTER UPDATE ON sessions BEGIN
 INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 SELECT 'search_doc',entity_id,'upsert',entity_revision+1,payload_json,'2026-01-01T00:00:00Z'
 FROM current_projections WHERE entity_type='search_doc'; END`)
	before := domainState(t, s)
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-late-history", request)
	if !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
		t.Fatal("backfill ignored a later retained revision", ack, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NULL", authDigest(grant)) != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity) != 1 {
		t.Fatal("failed backfill consumed its grant or changed current search")
	}
}

func TestSyncSearchBackfillNoopDoesNotInventHistoryOrRepairBadCurrent(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Current source"})
	identity := projectionPair("item", item.ID)
	proof := requireStoredSearchProof(t, s, a, identity)
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq=?", proof.seq)
	retained := "  " + proof.payload + " \n"
	execSQL(t, s.db, "UPDATE current_projections SET payload_json=? WHERE entity_type='search_doc' AND entity_id=?", retained, identity)
	_, grant := issueGrant(t, a, token, "reauth-backfill-noop")
	before := domainState(t, s)
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-noop-current", request)
	if err != nil || !ack.OK || ack.Revision != proof.revision || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 {
		t.Fatal("valid existing current search was rewritten or guessed a proof", ack, err)
	}
	after := domainState(t, s)
	for table, prior := range before {
		if table != "api_idempotency" && table != "mutation_events" && table != "sessions" && prior != after[table] {
			t.Fatal("no-op backfill changed more than its receipt and activity", table)
		}
	}
	execSQL(t, s.db, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.approved_text','Private OCR') WHERE entity_type='search_doc' AND entity_id=?", identity)
	_, grant = issueGrant(t, a, token, "reauth-backfill-bad-current")
	before = domainState(t, s)
	ack, err = a.backfillSyncSearch(t.Context(), token, grant, "backfill-bad-current", request)
	if !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
		t.Fatal("inconsistent current search was silently repaired", ack, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncSearchBackfillRechecksNoopAfterSessionActivity(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Current source"})
	_, grant := issueGrant(t, a, token, "reauth-backfill-late")
	execSQL(t, s.db, "CREATE TRIGGER change_backfill_source AFTER UPDATE ON sessions BEGIN UPDATE items SET name='Late source'; END")
	before := domainState(t, s)
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-late-source", request)
	if !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
		t.Fatal("no-op returned success after its source changed", ack, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncSearchBackfillRejectsSourceChangesDuringGrantConsumption(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "current"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			s, catalog, a, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Reviewed source"})
			if missing {
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", projectionPair("item", item.ID))
			}
			_, grant := issueGrant(t, a, token, "reauth-backfill-grant-source")
			execSQL(t, s.db, `CREATE TRIGGER change_source_during_grant AFTER UPDATE OF consumed_at ON auth_reauth_grants BEGIN
 UPDATE items SET name='Late source'; END`)
			before := domainState(t, s)
			proofs := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs")
			request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
			ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-grant-source", request)
			if !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
				t.Fatal("source changed during grant consumption was accepted", ack, err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NULL", authDigest(grant)) != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != proofs {
				t.Fatal("failed source verification consumed a grant or committed a proof")
			}
		})
	}
}

func TestSyncSearchBackfillRequiresOneSessionBoundGrantWithoutPersistingIt(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Grant owner"})
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	if ack, err := a.backfillSyncSearch(t.Context(), token, "", "backfill-no-grant", request); !errors.Is(err, ErrReauthRequired) || ack != (ActionAck{}) {
		t.Fatal("a new backfill accepted a missing grant", ack, err)
	}
	_, grant := issueGrant(t, a, token, "reauth-backfill-required")
	_, otherSession := login(t, a)
	if ack, err := a.backfillSyncSearch(t.Context(), otherSession, grant, "backfill-wrong-session", request); !errors.Is(err, ErrReauthRequired) || ack != (ActionAck{}) {
		t.Fatal("a different session consumed the grant", ack, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NULL", authDigest(grant)) != 1 {
		t.Fatal("failed grant use consumed the original capability")
	}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-authorized", request)
	if err != nil || !ack.OK || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NOT NULL", authDigest(grant)) != 1 {
		t.Fatal("authorized backfill did not consume its grant", ack, err)
	}
	if scalar[int](t, s.db, `SELECT COUNT(*) FROM api_idempotency WHERE instr(key,?)>0 OR instr(request_sha256,?)>0 OR instr(response_json,?)>0`, grant, grant, grant) != 0 ||
		scalar[int](t, s.db, `SELECT COUNT(*) FROM mutation_events WHERE instr(idempotency_key,?)>0 OR instr(result_json,?)>0 OR instr(reason,?)>0 OR instr(note,?)>0`, grant, grant, grant, grant) != 0 {
		t.Fatal("raw reauthentication capability was persisted in an audit or receipt")
	}
	if again, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-new-key", request); !errors.Is(err, ErrReauthRequired) || again != (ActionAck{}) {
		t.Fatal("a consumed grant authorized a new command", again, err)
	}
	if replay, err := a.backfillSyncSearch(t.Context(), otherSession, "", "backfill-authorized", request); err != nil || replay != ack {
		t.Fatal("a fresh session could not explicitly reconcile the original receipt", replay, err)
	}
}

func TestSyncSearchBackfillRejectsExpiredGrantAndCancellation(t *testing.T) {
	s, a, now, _, token := completedAuthFixture(t)
	catalog, err := NewCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Grant expiry"})
	_, grant := issueGrant(t, a, token, "reauth-backfill-expiry")
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	*now = now.Add(reauthLifetime)
	before := domainState(t, s)
	if ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-expired-grant", request); !errors.Is(err, ErrReauthRequired) || ack != (ActionAck{}) {
		t.Fatal("an expired grant authorized backfill", ack, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NULL", authDigest(grant)) != 1 {
		t.Fatal("expired grant validation changed its stored consumption")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if ack, err := a.backfillSyncSearch(ctx, token, grant, "backfill-canceled", request); !errors.Is(err, context.Canceled) || ack != (ActionAck{}) {
		t.Fatal("canceled backfill returned a result", ack, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncSearchBackfillRollsBackLateGrantChanges(t *testing.T) {
	for _, test := range []struct {
		name, timing, statement string
		missing                 bool
	}{
		{"reset after audit", "AFTER INSERT ON mutation_events WHEN NEW.operation='internal_sync_search_backfill'", "UPDATE auth_reauth_grants SET consumed_at=NULL", false},
		{"extend after audit", "AFTER INSERT ON mutation_events WHEN NEW.operation='internal_sync_search_backfill'", "UPDATE auth_reauth_grants SET expires_at='2099-01-01T00:00:00Z'", false},
		{"delete after audit", "AFTER INSERT ON mutation_events WHEN NEW.operation='internal_sync_search_backfill'", "DELETE FROM auth_reauth_grants", false},
		{"reset after activity", "AFTER UPDATE ON sessions", "UPDATE auth_reauth_grants SET consumed_at=NULL", false},
		{"extend during consumption", "AFTER UPDATE OF consumed_at ON auth_reauth_grants WHEN NEW.consumed_at IS NOT NULL", "UPDATE auth_reauth_grants SET expires_at='2099-01-01T00:00:00Z'", false},
		{"reset after missing-source publication", "AFTER INSERT ON mutation_events WHEN NEW.operation='internal_sync_search_backfill'", "UPDATE auth_reauth_grants SET consumed_at=NULL", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, catalog, a, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Grant owner"})
			if test.missing {
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", projectionPair("item", item.ID))
			}
			proofs := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs")
			_, grant := issueGrant(t, a, token, "reauth-backfill-late-grant")
			execSQL(t, s.db, "CREATE TRIGGER damage_backfill_grant "+test.timing+" BEGIN "+test.statement+"; END")
			before := domainState(t, s)
			request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
			ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-late-grant", request)
			if err == nil || ack != (ActionAck{}) {
				t.Fatal("late grant mutation was accepted", ack, err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NULL", authDigest(grant)) != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != proofs {
				t.Fatal("failed mutation consumed or changed its grant or proof state")
			}
			execSQL(t, s.db, "DROP TRIGGER damage_backfill_grant")
			if ack, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-late-grant", request); err != nil || !ack.OK {
				t.Fatal("rolled-back original command could not be explicitly retried", ack, err)
			}
		})
	}
}

func TestSyncSearchBackfillConcurrentGrantAndOriginalKey(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		name := "different keys"
		if sameKey {
			name = "same key"
		}
		t.Run(name, func(t *testing.T) {
			s, catalog, a, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Concurrent owner"})
			_, grant := issueGrant(t, a, token, "reauth-backfill-concurrent")
			request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
			type result struct {
				ack ActionAck
				err error
			}
			start, results := make(chan struct{}), make(chan result, 2)
			for _, key := range []string{"backfill-concurrent-one", "backfill-concurrent-two"} {
				if sameKey {
					key = "backfill-concurrent-same"
				}
				go func() {
					<-start
					ack, err := a.backfillSyncSearch(t.Context(), token, grant, key, request)
					results <- result{ack, err}
				}()
			}
			close(start)
			first, second := <-results, <-results
			if sameKey {
				if first.err != nil || second.err != nil || first.ack != second.ack || !first.ack.OK {
					t.Fatal("same-key concurrent reconciliation differed", first, second)
				}
			} else {
				if first.err != nil {
					first, second = second, first
				}
				if first.err != nil || !first.ack.OK || !errors.Is(second.err, ErrReauthRequired) || second.ack != (ActionAck{}) {
					t.Fatal("one grant authorized two different concurrent commands", first, second)
				}
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='internal_sync_search_backfill'") != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE token_hash=? AND consumed_at IS NOT NULL", authDigest(grant)) != 1 {
				t.Fatal("concurrent backfill duplicated an audit or grant consumption")
			}
		})
	}
}

func TestSyncSearchBackfillOriginalKeyReplaysAfterOwnerDeletion(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Replay owner"})
	identity := projectionPair("item", item.ID)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: item.ID, ItemID: item.ID, ItemRevision: item.Revision}
	_, grant := issueGrant(t, a, token, "reauth-backfill-replay")
	first, err := a.backfillSyncSearch(t.Context(), token, grant, "backfill-original-replay", request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Delete(t.Context(), token, "backfill-delete-owner", item.ID, item.Revision); err != nil {
		t.Fatal(err)
	}
	count := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs")
	_, fresh := login(t, a)
	replay, err := a.backfillSyncSearch(t.Context(), fresh, "", "backfill-original-replay", request)
	if err != nil || !reflect.DeepEqual(replay, first) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != count {
		t.Fatal("a fresh session could not reconcile the original backfill receipt", replay, err)
	}
}
