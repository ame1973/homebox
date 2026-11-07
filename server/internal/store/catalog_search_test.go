package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func seedSearchUnit(t *testing.T, s *Store, id, itemID, code string, serial *string, notes string) {
	t.Helper()
	execSQL(t, s.db, `INSERT INTO item_units(id,item_id,asset_code,serial_number,notes,created_at,updated_at)
 VALUES(?,?,?,?,?,'2026','2026')`, id, itemID, code, serial, notes)
}

func publishCatalogSearch(t *testing.T, taxonomy *Taxonomy, token, key, itemID string) {
	t.Helper()
	_, err := runDomainMutation(t.Context(), taxonomy.auth, token, key, "test_publish_catalog_search", itemID, func(m *mutationTx) (ActionAck, error) {
		if err := m.touchItemSearch(itemID, false); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCatalogUnitSearchKeepsIdentifiersAndPurchaseEvidenceInTheirOwnScope(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	seedCatalogItem(t, taxonomy, token, "item", nil)
	seedCatalogItem(t, taxonomy, token, "other", nil)
	execSQL(t, s.db, "UPDATE items SET name='UnrelatedCatalogTitle' WHERE id='other'")
	unitID := strings.Repeat("界", 124) + "<& \""
	seedSearchUnit(t, s, unitID, "item", "ASSETALPHA", testPointer("SerialAlpha"), "UnitAlphaNote")
	seedSearchUnit(t, s, "unit-beta", "item", "ASSETBETA", testPointer("SerialBeta"), "UnitBetaNote")
	seedSearchUnit(t, s, "unit-other", "other", "ASSETOTHER", nil, "UnrelatedUnitNote")
	for _, id := range []string{unitID, "unit-beta", "unit-other"} {
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('unit',?,1)", id)
	}
	for _, purchase := range []struct {
		id, item, merchant, receipt, note string
		unit                              *string
	}{
		{"alpha", "item", "MerchantAlpha", "ReceiptAlpha", "LineAlpha", &unitID},
		{"beta", "item", "MerchantBeta", "ReceiptBeta", "LineBeta", testPointer("unit-beta")},
		{"common", "item", "MerchantCommon", "ReceiptCommon", "LineCommon", nil},
		{"other", "other", "MerchantOther", "ReceiptOther", "LineOther", testPointer("unit-other")},
	} {
		seedReadPurchase(t, s, PurchaseData{ID: purchase.id, MerchantName: purchase.merchant, PurchasedAt: "2026-01-01T00:00:00Z", Currency: "TWD", ReceiptNote: purchase.receipt})
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: purchase.id, PurchaseID: purchase.id, ItemID: purchase.item, ItemUnitID: purchase.unit, QuantityBase: 1, PaidMinor: 100, Note: purchase.note})
	}
	seedReadPurchase(t, s, PurchaseData{ID: "unlinked", MerchantName: "UnlinkedMerchant", PurchasedAt: "2026-01-01T00:00:00Z", Currency: "TWD", ReceiptNote: "UnlinkedReceipt"})
	seedReadPurchaseLine(t, s, PurchaseLineData{ID: "alpha-again", PurchaseID: "alpha", ItemID: "item", ItemUnitID: &unitID, QuantityBase: 1, PaidMinor: 100, Note: "LineAlphaAgain"})
	changed := requireProjectionReplay(t, s, func() { publishCatalogSearch(t, taxonomy, token, "publish-unit-scopes", "item") })
	if len(changed) != 3 || !changed[projectionKey{"search_doc", projectionPair("unit", unitID)}] ||
		changed[projectionKey{"search_doc", projectionPair("unit", "unit-other")}] {
		t.Fatal("search fanout lost a canonical long identity or included another item's unit")
	}
	item := approvedText(t, s, "item")
	alpha := projectedSearchText(t, s, "unit", unitID)
	beta := projectedSearchText(t, s, "unit", "unit-beta")
	for _, term := range []string{"ManualTitle", "ManualBrand", "ManualModel", "ManualDescription", "ManualNote"} {
		if !strings.Contains(item, term) || !strings.Contains(alpha, term) || !strings.Contains(beta, term) {
			t.Fatal("shared manual catalog fields were lost", term)
		}
	}
	for _, term := range []string{"ASSETALPHA", "SerialAlpha", "UnitAlphaNote", "MerchantAlpha", "ReceiptAlpha", "LineAlpha", "LineAlphaAgain"} {
		if !strings.Contains(alpha, term) || strings.Contains(beta, term) ||
			scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH ?", term) != 1 {
			t.Fatal("unit identifier or purchase evidence escaped its exact unit scope", term)
		}
	}
	for _, term := range []string{"MerchantBeta", "ReceiptBeta", "LineBeta", "MerchantCommon", "ReceiptCommon", "LineCommon"} {
		if !strings.Contains(item, term) || strings.Contains(alpha, term) {
			t.Fatal("item and unit purchase scopes were conflated", term)
		}
	}
	for _, term := range []string{"ASSETALPHA", "SerialAlpha", "UnitAlphaNote", "ASSETBETA", "SerialBeta", "UnitBetaNote"} {
		if strings.Contains(item, term) {
			t.Fatal("unit-specific terms contaminated item publication", term)
		}
	}
	for _, term := range []string{"MerchantOther", "ReceiptOther", "LineOther", "UnrelatedCatalogTitle", "UnrelatedUnitNote", "UnlinkedMerchant", "UnlinkedReceipt"} {
		if strings.Contains(item, term) || strings.Contains(alpha, term) || strings.Contains(beta, term) {
			t.Fatal("an unrelated purchase/item was not correlated to its owner", term)
		}
	}
	if strings.Count(alpha, "MerchantAlpha") != 1 {
		t.Fatal("multiple matching lines duplicated their purchase evidence")
	}

	// A unit-only publisher must read authoritative fields, not either FTS cache.
	execSQL(t, s.db, "UPDATE search_documents SET title='PoisonedItemCache' WHERE item_id='item'")
	execSQL(t, s.db, "UPDATE unit_search_documents SET title='PoisonedUnitCache',revision=99 WHERE unit_id=?", unitID)
	changed = requireProjectionReplay(t, s, func() {
		_, err := runDomainMutation(t.Context(), taxonomy.auth, token, "unit-only-source-publication", "test_unit_only_search", unitID, func(m *mutationTx) (ActionAck, error) {
			if _, err := m.touch("search_doc", projectionPair("unit", unitID), false); err != nil {
				return ActionAck{}, err
			}
			if _, err := m.tx.ExecContext(m.ctx, "UPDATE item_units SET notes='FinalUnitNote' WHERE id=?", unitID); err != nil {
				return ActionAck{}, err
			}
			return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	alpha = projectedSearchText(t, s, "unit", unitID)
	if len(changed) != 1 || !strings.Contains(alpha, "FinalUnitNote") || strings.Contains(alpha, "UnitAlphaNote") ||
		strings.Contains(alpha, "Poisoned") || scalar[int](t, s.db, "SELECT revision FROM unit_search_documents WHERE unit_id=?", unitID) != 2 ||
		scalar[string](t, s.db, "SELECT title FROM search_documents WHERE item_id='item'") != "PoisonedItemCache" {
		t.Fatal("unit-only publication copied cache text/revisions or rewrote an unstaged item cache")
	}
}

func TestCatalogUnitSearchCoalescesFinalSourceAndPreservesFrozenOlderRows(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	tag := createTag(t, taxonomy, token, "BeforeTag")
	seedCatalogItem(t, taxonomy, token, "item", nil, tag.ID)
	seedSearchUnit(t, s, "unit", "item", "ASSET", nil, "BeforeUnit")
	publishCatalogSearch(t, taxonomy, token, "initial-unit-search", "item")
	sequence := scalar[int64](t, s.db, "SELECT MAX(seq) FROM sync_changes")
	before := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE entity_type='search_doc' AND entity_id=? ORDER BY seq DESC LIMIT 1", projectionPair("unit", "unit"))
	changed := requireProjectionReplay(t, s, func() {
		_, err := runDomainMutation(t.Context(), taxonomy.auth, token, "unit-final-source", "test_unit_final_source", struct{}{}, func(m *mutationTx) (Tag, error) {
			for _, label := range []string{"Intermediate", "Final"} {
				if err := m.touchItemSearch("item", true); err != nil {
					return Tag{}, err
				}
				for _, statement := range []string{"UPDATE items SET name=? WHERE id='item'", "UPDATE item_units SET notes=? WHERE id='unit'", "UPDATE tags SET name=? WHERE id=?"} {
					args := []any{label}
					if strings.Contains(statement, "tags") {
						args = append(args, tag.ID)
					}
					if _, err := m.tx.ExecContext(m.ctx, statement, args...); err != nil {
						return Tag{}, err
					}
				}
				if _, err := m.touch("tag", tag.ID, false); err != nil {
					return Tag{}, err
				}
			}
			return loadTag(m.ctx, m.tx, tag.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 4 || strings.Contains(projectedSearchText(t, s, "unit", "unit"), "Intermediate") ||
		!strings.Contains(projectedSearchText(t, s, "unit", "unit"), "Final") ||
		scalar[int](t, s.db, "SELECT revision FROM unit_search_documents WHERE unit_id='unit'") != 2 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='search_doc' AND entity_id=? AND seq>?", projectionPair("unit", "unit"), sequence) != 1 ||
		scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE entity_type='search_doc' AND entity_id=? AND seq<=? ORDER BY seq DESC LIMIT 1", projectionPair("unit", "unit"), sequence) != before {
		t.Fatal("unit search exposed an intermediate source, duplicate revision, or rewritten frozen row")
	}
}

func TestCatalogUnitSearchFailuresRollbackAllSourcePublicationAndAcknowledgements(t *testing.T) {
	for _, failure := range []string{"cache insert", "cache update", "current", "later outbox", "deferred commit", "late authorization", "counter overflow"} {
		t.Run(failure, func(t *testing.T) {
			s, taxonomy, auth, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "BeforeClass", nil)
			seedCatalogItem(t, taxonomy, token, "item", &category.ID)
			seedSearchUnit(t, s, "unit-a", "item", "ASSET-A", nil, "UnitA")
			seedSearchUnit(t, s, "unit-b", "item", "ASSET-B", nil, "UnitB")
			publishCatalogSearch(t, taxonomy, token, "baseline-unit-search", "item")
			category = getCategory(t, taxonomy, token, category.ID)
			var trigger string
			switch failure {
			case "cache insert":
				trigger = "BEFORE INSERT ON unit_search_documents WHEN new.unit_id='unit-b' BEGIN SELECT RAISE(ABORT,'cache insert failure'); END"
			case "cache update":
				trigger = "BEFORE UPDATE ON unit_search_documents WHEN new.unit_id='unit-b' BEGIN SELECT RAISE(ABORT,'cache update failure'); END"
			case "current":
				trigger = "BEFORE UPDATE ON current_projections WHEN new.entity_type='search_doc' AND new.entity_id=json_array('unit','unit-b') BEGIN SELECT RAISE(ABORT,'current failure'); END"
			case "later outbox":
				trigger = "BEFORE INSERT ON sync_changes WHEN new.entity_type='search_doc' AND new.entity_id=json_array('unit','unit-b') BEGIN SELECT RAISE(ABORT,'outbox failure'); END"
			case "deferred commit":
				execSQL(t, s.db, "CREATE TABLE unit_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				trigger = "AFTER INSERT ON mutation_events BEGIN INSERT INTO unit_commit_guard VALUES('missing'); END"
			case "late authorization":
				trigger = "AFTER UPDATE ON unit_search_documents WHEN new.unit_id='unit-b' BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END"
			case "counter overflow":
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				for _, statement := range []string{"UPDATE projection_revisions SET revision=? WHERE entity_type='search_doc' AND entity_id=?", "UPDATE current_projections SET entity_revision=? WHERE entity_type='search_doc' AND entity_id=?"} {
					if _, err := tx.ExecContext(t.Context(), statement, maxSafeInteger, projectionPair("unit", "unit-b")); err != nil {
						t.Fatal(err)
					}
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if trigger != "" {
				execSQL(t, s.db, "CREATE TRIGGER fail_unit_search "+trigger)
			}
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			before := domainState(t, s)
			result, err := taxonomy.PatchCategory(t.Context(), token, "rollback-unit-dependent-search", category.ID, category.Revision, CategoryPatch{Name: testPointer("AfterClass")})
			if err == nil || result.ID != "" || failure == "counter overflow" && !errors.Is(err, ErrRevisionOverflow) {
				t.Fatal("failed unit dependency exposed a successful resource or lost its overflow guard", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'BeforeClass'") != 2 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'AfterClass'") != 0 {
				t.Fatal("failed publication leaked partially rebuilt unit FTS terms")
			}
			if trigger != "" {
				execSQL(t, s.db, "DROP TRIGGER fail_unit_search")
				if _, err := taxonomy.PatchCategory(t.Context(), token, "rollback-unit-dependent-search", category.ID, category.Revision, CategoryPatch{Name: testPointer("AfterClass")}); err != nil {
					t.Fatal("rolled-back key/proof/connection could not be explicitly retried", err)
				}
			}
		})
	}
}

func TestCatalogUnitSearchRejectsCorruptSourceWithoutPublishingPartialResults(t *testing.T) {
	for _, corruption := range []string{"asset code", "serial", "notes", "tracking mode", "identity"} {
		t.Run(corruption, func(t *testing.T) {
			s, taxonomy, _, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "Category", nil)
			seedCatalogItem(t, taxonomy, token, "item", &category.ID)
			seedSearchUnit(t, s, "unit", "item", "ASSET", nil, "Note")
			switch corruption {
			case "asset code":
				execSQL(t, s.db, "UPDATE item_units SET asset_code='' WHERE id='unit'")
			case "serial":
				execSQL(t, s.db, "UPDATE item_units SET serial_number=? WHERE id='unit'", string([]byte{0xff}))
			case "notes":
				execSQL(t, s.db, "UPDATE item_units SET notes=? WHERE id='unit'", string([]byte{0xff}))
			case "tracking mode":
				execSQL(t, s.db, "UPDATE items SET tracking_mode='quantity' WHERE id='item'")
			case "identity":
				execSQL(t, s.db, "UPDATE item_units SET id=? WHERE id='unit'", strings.Repeat("x", 129))
			}
			before := domainState(t, s)
			result, err := taxonomy.PatchCategory(t.Context(), token, "bad-unit-search-source", category.ID, 2, CategoryPatch{Name: testPointer("After")})
			if !errors.Is(err, ErrDomainState) || result.ID != "" {
				t.Fatal("corrupt unit source was silently repaired or published", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogSearchMaterializerRejectsUnstagedDeletedOrUnsupportedIdentities(t *testing.T) {
	s := openTestStore(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	m := &mutationTx{ctx: t.Context(), tx: tx, now: time.Now().UTC(), pending: make(map[projectionKey]pendingProjection)}
	for _, invalid := range []struct {
		id      string
		pending *pendingProjection
	}{
		{"not-json", nil},
		{projectionPair("unit", "missing"), nil},
		{projectionPair("other", "missing"), &pendingProjection{revision: 1}},
		{projectionPair("unit", "missing"), &pendingProjection{revision: 1, deleted: true}},
		{projectionPair("unit", "missing"), &pendingProjection{revision: 0}},
	} {
		m.pending = make(map[projectionKey]pendingProjection)
		if invalid.pending != nil {
			m.pending[projectionKey{"search_doc", invalid.id}] = *invalid.pending
		}
		if value, err := m.materializeSearch(invalid.id); !errors.Is(err, ErrDomainState) || value.EntityID != "" {
			t.Fatal("invalid staged identity reached search source publication", err)
		}
	}
	for _, kind := range []string{"item", "unit"} {
		id := projectionPair(kind, "missing")
		m.pending[projectionKey{"search_doc", id}] = pendingProjection{revision: 1}
		if value, err := m.materializeSearch(id); !errors.Is(err, ErrNotFound) || value.EntityID != "" {
			t.Fatal("a missing source identity produced a search document", err)
		}
	}
}
