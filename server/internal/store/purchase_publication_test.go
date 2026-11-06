package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Exercise private final-state publication, not a fictional public Purchase API.
// Actual commands must additionally bind original proofs and financial policy.
func mutatePurchaseProjectionFixture(ctx context.Context, fixture purchaseFixture, key, final string) (Purchase, error) {
	request := struct{ PurchaseID, Final string }{fixture.purchase.ID, final}
	return runDomainMutation(ctx, fixture.auth, fixture.token, key, "test_purchase_projection", request, func(m *mutationTx) (Purchase, error) {
		if _, err := loadPurchase(ctx, m.tx, fixture.purchase.ID); err != nil {
			return Purchase{}, err
		}
		for _, existing := range []PurchaseLine{fixture.unitLine, fixture.stockLine} {
			if _, err := loadPurchaseLine(ctx, m.tx, existing.ID); err != nil {
				return Purchase{}, err
			}
		}
		var ids []string
		for _, allocation := range []PurchaseLineData{
			{ItemID: fixture.individual.ID, ItemUnitID: testPointer(fixture.unit.ID), QuantityBase: 1, PaidMinor: 10, Note: final + "UnitAllocation"},
			{ItemID: fixture.quantity.ID, QuantityBase: 1500, PaidMinor: 5, Note: final + "StockAllocation"},
		} {
			id, err := newDomainID()
			if err != nil {
				return Purchase{}, err
			}
			_, err = m.tx.ExecContext(ctx, `INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor,note,created_at,mutation_event_id) VALUES(?,?,?,?,?,?,?,?,?)`,
				id, fixture.purchase.ID, allocation.ItemID, allocation.ItemUnitID, allocation.QuantityBase, allocation.PaidMinor, allocation.Note, stamp(m.now), m.eventID)
			if err != nil {
				return Purchase{}, err
			}
			ids = append(ids, id)
		}
		for _, stage := range []string{"Intermediate", final} {
			if _, err := m.tx.ExecContext(ctx, "UPDATE purchases SET merchant_name=?,receipt_note=? WHERE id=?", stage+"Merchant", stage+"Receipt", fixture.purchase.ID); err != nil {
				return Purchase{}, err
			}
			if _, err := m.touch("purchase", fixture.purchase.ID, stage == "Intermediate"); err != nil {
				return Purchase{}, err
			}
			for _, id := range ids {
				revision, err := m.touch("purchase_line", id, stage == "Intermediate")
				if err != nil {
					return Purchase{}, err
				}
				if revision != 1 {
					return Purchase{}, ErrDomainState
				}
			}
			for _, item := range []string{fixture.individual.ID, fixture.quantity.ID} {
				if err := m.touchItemSearch(item, true); err != nil {
					return Purchase{}, err
				}
			}
		}
		return loadPurchase(ctx, m.tx, fixture.purchase.ID)
	})
}

func TestPurchasePublicationCoalescesFinalSourceIndependentLinesScopedApprovedSearchAndOriginalReply(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('sibling',?,'SIBLING','2026','2026')", fixture.individual.ID)
	seedCatalogProjection(t, s, "unit", "sibling", UnitData{ID: "sibling", ItemID: fixture.individual.ID, AssetCode: "SIBLING", Status: "stored"})
	execSQL(t, s.db, "INSERT INTO unit_search_documents(unit_id,title,reviewed_ai,revision,updated_at) VALUES(?,'NeverCopyPurchaseCache','NeverPublishPurchaseOCR',987,'2026')", fixture.unit.ID)
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"NeverCopyCurrentPurchase\":true}' WHERE entity_type IN('purchase','purchase_line')")
	before := domainState(t, s)
	var reply Purchase
	changed := requireProjectionReplay(t, s, func() {
		var err error
		reply, err = mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-final-source", "Final")
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.Revision != 8 || reply.MerchantName != "FinalMerchant" || reply.ReceiptNote != "FinalReceipt" || reply.PurchasedAt != fixture.purchase.PurchasedAt || reply.Currency != "JPY" {
		t.Fatal("header did not preserve exact final source and original date/currency")
	}
	for _, key := range []projectionKey{{"purchase", fixture.purchase.ID}, {"item", fixture.individual.ID}, {"item", fixture.quantity.ID},
		{"search_doc", projectionPair("item", fixture.individual.ID)}, {"search_doc", projectionPair("item", fixture.quantity.ID)},
		{"search_doc", projectionPair("unit", fixture.unit.ID)}, {"search_doc", projectionPair("unit", "sibling")}} {
		if !changed[key] {
			t.Fatal("purchase dependency omitted", key)
		}
	}
	rows, err := s.db.QueryContext(t.Context(), "SELECT id FROM purchase_lines WHERE mutation_event_id IS NOT NULL ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || len(changed) != 9 {
		t.Fatal("one identity was published repeatedly or shared audit became unique", len(ids), len(changed))
	}
	for _, id := range ids {
		line, err := loadPurchaseLine(t.Context(), s.db, id)
		if err != nil || line.Revision != 1 || !changed[projectionKey{"purchase_line", id}] {
			t.Fatal("Line proof was conflated with Purchase-plus-one", err)
		}
		encoded, _ := json.Marshal(line.PurchaseLineData)
		if scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='purchase_line' AND entity_id=?", id) != string(encoded) {
			t.Fatal("line publication copied stale cache or private metadata")
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_lines l JOIN mutation_events e ON e.id=l.mutation_event_id WHERE l.id=? AND l.created_at=e.occurred_at", id) != 1 {
			t.Fatal("producer history/audit did not commit with exact time")
		}
	}
	encoded, _ := json.Marshal(reply.PurchaseData)
	if scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='purchase' AND entity_id=?", reply.ID) != string(encoded) || scalar[int](t, s.db, "SELECT SUM(paid_minor) FROM purchase_lines") != 120015 {
		t.Fatal("header publication copied intermediate data or multiplied/double-counted allocations")
	}
	for _, text := range []string{"FinalMerchant", "FinalReceipt", "FinalUnitAllocation"} {
		if !strings.Contains(projectedSearchText(t, s, "unit", fixture.unit.ID), text) || strings.Contains(projectedSearchText(t, s, "unit", "sibling"), text) {
			t.Fatal("purchase evidence was missing or leaked into a sibling Unit", text)
		}
	}
	for _, text := range []string{"Intermediate", "NeverCopyPurchaseCache", "NeverPublishPurchaseOCR", "NeverCopyCurrentPurchase"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE ?", "%"+text+"%") != 0 {
			t.Fatal("raw/cache/intermediate text entered frozen publication", text)
		}
	}
	if scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", projectionPair("unit", fixture.unit.ID)) != 1 {
		t.Fatal("unverified search cache revision became authority")
	}
	after := domainState(t, s)
	for _, table := range []string{"locations", "location_events", "item_units", "stock_balances", "inventory_events", "media", "media_links", "ai_jobs", "ai_review_entries", "ocr_results"} {
		if after[table] != before[table] {
			t.Fatal("allocation publication invented physical/media/AI work", table)
		}
	}
	for _, statement := range []string{"UPDATE purchase_lines SET paid_minor=paid_minor", "DELETE FROM purchase_lines WHERE mutation_event_id IS NOT NULL"} {
		if _, err = s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("new posted allocation remained mutable", statement)
		}
	}
	original := reply
	if _, err = mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-later-source", "Later"); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, fixture.auth)
	fixture.token = fresh
	before = domainState(t, s)
	if frozen, err := mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-final-source", "Final"); err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("fresh login could not reconcile the original frozen header", err)
	}
	requireDomainState(t, s, before)
	if value, err := mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-final-source", "Changed"); !errors.Is(err, ErrIdempotencyConflict) || value != (Purchase{}) {
		t.Fatal("changed body reused a posted financial key", err)
	}
	requireDomainState(t, s, before)
}

func TestPurchaseCounterOnlyTombstonesRemainIndependentWithoutDeletingPostedHistory(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('purchase','gone-header',7),('purchase_line','gone-line',11)")
	purchaseBytes, lineBytes := scalar[string](t, s.db, purchaseOriginalBytesTest), scalar[string](t, s.db, purchaseLineOriginalBytesTest)
	changed := requireProjectionReplay(t, s, func() {
		_, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "purchase-counter-tombstones", "test_purchase_counter_tombstones", struct{}{}, func(m *mutationTx) (ActionAck, error) {
			var revision int64
			for _, key := range []projectionKey{{"purchase", "gone-header"}, {"purchase_line", "gone-line"}} {
				var err error
				revision, err = m.touch(key.kind, key.id, true)
				if err != nil {
					return ActionAck{}, err
				}
			}
			return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 2 || !changed[projectionKey{"purchase", "gone-header"}] || !changed[projectionKey{"purchase_line", "gone-line"}] || scalar[string](t, s.db, purchaseOriginalBytesTest) != purchaseBytes || scalar[string](t, s.db, purchaseLineOriginalBytesTest) != lineBytes {
		t.Fatal("independent tombstone mutated posted source or another identity")
	}
	for _, key := range []projectionKey{{"purchase", "gone-header"}, {"purchase_line", "gone-line"}} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id) != 0 || scalar[int](t, s.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", key.kind, key.id) != 1 {
			t.Fatal("counter-only removal did not publish its own null tombstone")
		}
	}
}

func TestPurchaseFinalMaterializationRejectsMissingCorruptOrExhaustedSourceWithoutPartialSuccess(t *testing.T) {
	for _, kind := range []string{"purchase", "purchase_line"} {
		for _, failure := range []string{"missing", "corrupt", "overflow"} {
			t.Run(kind+failure, func(t *testing.T) {
				fixture := purchasesFixture(t)
				s := fixture.store
				id := fixture.purchase.ID
				if kind == "purchase_line" {
					id = fixture.unitLine.ID
				}
				if failure == "overflow" {
					setCatalogProjectionRevision(t, s, kind, id, maxSafeInteger)
				}
				before := domainState(t, s)
				ack, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "bad-purchase-materialization", "test_bad_purchase_materialization", failure, func(m *mutationTx) (ActionAck, error) {
					identity := id
					if failure == "missing" {
						identity = "missing"
					}
					if failure == "corrupt" {
						if kind == "purchase" {
							if _, err := m.tx.ExecContext(m.ctx, "UPDATE purchases SET purchased_at='2026' WHERE id=?", id); err != nil {
								return ActionAck{}, err
							}
						} else {
							identity = "bad-new-line"
							if _, err := m.tx.ExecContext(m.ctx, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor) VALUES(?,?,?,1,?)", identity, fixture.purchase.ID, fixture.quantity.ID, maxSafeInteger+1); err != nil {
								return ActionAck{}, err
							}
						}
					}
					revision, err := m.touch(kind, identity, false)
					return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, err
				})
				want := ErrDomainState
				if failure == "missing" {
					want = ErrNotFound
				}
				if failure == "overflow" {
					want = ErrRevisionOverflow
				}
				if !errors.Is(err, want) || ack != (ActionAck{}) {
					t.Fatal("failed final financial source released success", err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
}
