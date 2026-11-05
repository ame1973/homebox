package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Exercise final-state publication, not a fictional public Unit/Stock mutation.
// Future services must additionally bind their original proofs and append the
// actual immutable inventory/lifecycle events in this same private transaction.
func mutateHoldingProjectionFixture(ctx context.Context, fixture holdingFixture, key, final string, sourceQuantity, targetQuantity int64) (Stock, error) {
	request := struct {
		UnitID, ItemID, Final          string
		SourceQuantity, TargetQuantity int64
	}{fixture.unit.ID, fixture.quantity.ID, final, sourceQuantity, targetQuantity}
	return runDomainMutation(ctx, fixture.auth, fixture.token, key, "test_holdings_projection", request, func(m *mutationTx) (Stock, error) {
		if _, err := loadUnit(ctx, m.tx, fixture.unit.ID); err != nil {
			return Stock{}, err
		}
		for _, balance := range []Stock{fixture.stock, fixture.target} {
			if _, err := loadStock(ctx, m.tx, balance.ItemID, balance.LocationID); err != nil {
				return Stock{}, err
			}
		}
		for _, stage := range []struct {
			text           string
			source, target int64
		}{{"Intermediate", 1499, 1}, {final, sourceQuantity, targetQuantity}} {
			if _, err := m.tx.ExecContext(ctx, "UPDATE item_units SET location_id='target',serial_number=?,notes=?,updated_at=? WHERE id=?", stage.text+"Serial", stage.text+"Notes", stamp(m.now), fixture.unit.ID); err != nil {
				return Stock{}, err
			}
			if _, err := m.touch("unit", fixture.unit.ID, stage.text == "Intermediate"); err != nil {
				return Stock{}, err
			}
			for _, balance := range []struct {
				value    Stock
				quantity int64
			}{{fixture.stock, stage.source}, {fixture.target, stage.target}} {
				if _, err := m.tx.ExecContext(ctx, "UPDATE stock_balances SET quantity_base=?,updated_at=? WHERE item_id=? AND location_id=?", balance.quantity, stamp(m.now), balance.value.ItemID, balance.value.LocationID); err != nil {
					return Stock{}, err
				}
				if _, err := m.touch("stock", projectionPair(balance.value.ItemID, balance.value.LocationID), stage.text == "Intermediate"); err != nil {
					return Stock{}, err
				}
			}
			for _, owner := range []string{fixture.individual.ID, fixture.quantity.ID} {
				if err := m.touchItemSearch(owner, true); err != nil {
					return Stock{}, err
				}
			}
			for _, location := range []string{"source", "target"} {
				if err := m.touchLocationPath(location); err != nil {
					return Stock{}, err
				}
			}
		}
		return loadStock(ctx, m.tx, fixture.target.ItemID, fixture.target.LocationID)
	})
}

func TestHoldingPublicationUsesFinalSourceCanonicalPairsIndependentCountersAndApprovedSearch(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	setCatalogProjectionRevision(t, s, "item", fixture.quantity.ID, 7)
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"NeverCopyProjectionCache\":true}' WHERE entity_type IN('unit','stock')")
	execSQL(t, s.db, "INSERT INTO unit_search_documents(unit_id,title,reviewed_ai,revision,updated_at) VALUES(?,'NeverCopyUnitCache','NeverPublishRawOCR',987,'2026')", fixture.unit.ID)
	before := domainState(t, s)
	var reply Stock
	changed := requireProjectionReplay(t, s, func() {
		var err error
		reply, err = mutateHoldingProjectionFixture(t.Context(), fixture, "holding-final-source", "Final", 1490, 10)
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, key := range []projectionKey{
		{"unit", fixture.unit.ID}, {"stock", projectionPair(fixture.stock.ItemID, "source")}, {"stock", projectionPair(fixture.stock.ItemID, "target")},
		{"item", fixture.individual.ID}, {"item", fixture.quantity.ID},
		{"search_doc", projectionPair("item", fixture.individual.ID)}, {"search_doc", projectionPair("item", fixture.quantity.ID)}, {"search_doc", projectionPair("unit", fixture.unit.ID)},
		{"location", "source"}, {"location", "target"}, {"location", "root"},
	} {
		if !changed[key] {
			t.Fatal("holding publication omitted an explicitly staged dependency", key)
		}
	}
	if len(changed) != 11 || reply.Revision != 2 || reply.QuantityBase != 10 || getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID).Revision != 8 ||
		scalar[int](t, s.db, "SELECT SUM(quantity_base) FROM stock_balances WHERE item_id=?", fixture.quantity.ID) != 1500 {
		t.Fatal("one identity was bumped repeatedly, balances were rescaled, or Stock/Item proofs were conflated")
	}
	unit, err := loadUnit(t.Context(), s.db, fixture.unit.ID)
	if err != nil || unit.Revision != 2 || unit.LocationID == nil || *unit.LocationID != "target" || unit.SerialNumber == nil || *unit.SerialNumber != "FinalSerial" || unit.Notes != "FinalNotes" {
		t.Fatal("unit did not reflect final source", err)
	}
	for _, row := range []struct {
		kind, id string
		payload  any
	}{
		{"unit", unit.ID, unit.UnitData}, {"stock", projectionPair(reply.ItemID, reply.LocationID), reply.StockData},
	} {
		encoded, err := json.Marshal(row.payload)
		if err != nil || scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, row.id) != string(encoded) {
			t.Fatal("materializer copied cached/intermediate source or an inner revision", err)
		}
	}
	for _, text := range []string{"FinalSerial", "FinalNotes"} {
		if !strings.Contains(projectedSearchText(t, s, "unit", unit.ID), text) || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH ?", text) != 1 {
			t.Fatal("approved unit text did not come from exact final source", text)
		}
	}
	for _, text := range []string{"Intermediate", "NeverCopyProjectionCache", "NeverCopyUnitCache", "NeverPublishRawOCR"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE ?", "%"+text+"%") != 0 {
			t.Fatal("cache/private/intermediate text leaked into frozen publication", text)
		}
	}
	if scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", projectionPair("unit", unit.ID)) != 1 {
		t.Fatal("a poisoned cache revision supplied publication authority")
	}
	after := domainState(t, s)
	for _, table := range []string{"purchases", "purchase_lines", "media", "media_links", "ai_jobs", "ai_review_entries", "ocr_results", "inventory_events"} {
		if after[table] != before[table] {
			t.Fatal("publication fixture fabricated another domain's workflow", table)
		}
	}
	original := reply
	if _, err = mutateHoldingProjectionFixture(t.Context(), fixture, "holding-later-source", "Later", 1480, 20); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, fixture.auth)
	fixture.token = fresh
	before = domainState(t, s)
	if frozen, err := mutateHoldingProjectionFixture(t.Context(), fixture, "holding-final-source", "Final", 1490, 10); err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("fresh-session reconciliation did not return the original independent Stock reply", err)
	}
	requireDomainState(t, s, before)

	// Explicit source-removal publication is a primitive for guarded deletion or
	// import/restore diffs, not ordinary consume/transfer of retained zero stock.
	changed = requireProjectionReplay(t, s, func() {
		_, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "holding-independent-tombstone", "test_holding_tombstones", struct{}{}, func(m *mutationTx) (ActionAck, error) {
			if _, err := m.tx.ExecContext(m.ctx, "DELETE FROM item_units WHERE id=?", unit.ID); err != nil {
				return ActionAck{}, err
			}
			if _, err := m.tx.ExecContext(m.ctx, "DELETE FROM stock_balances WHERE item_id=? AND location_id='target'", fixture.quantity.ID); err != nil {
				return ActionAck{}, err
			}
			var revision int64
			for _, key := range []projectionKey{{"unit", unit.ID}, {"stock", projectionPair(fixture.quantity.ID, "target")}, {"search_doc", projectionPair("unit", unit.ID)}} {
				var err error
				if revision, err = m.touch(key.kind, key.id, true); err != nil {
					return ActionAck{}, err
				}
			}
			for _, owner := range []string{fixture.individual.ID, fixture.quantity.ID} {
				if err := m.touchItemSearch(owner, true); err != nil {
					return ActionAck{}, err
				}
			}
			return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, key := range []projectionKey{{"unit", unit.ID}, {"stock", projectionPair(fixture.quantity.ID, "target")}, {"search_doc", projectionPair("unit", unit.ID)}} {
		if !changed[key] || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id) != 0 || scalar[int](t, s.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", key.kind, key.id) != 1 {
			t.Fatal("explicit deletion omitted its independent null tombstone", key)
		}
	}
	if scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances WHERE item_id=? AND location_id='source'", fixture.quantity.ID) != 1480 {
		t.Fatal("one pair's removal changed another balance")
	}
	before = domainState(t, s)
	if frozen, err := mutateHoldingProjectionFixture(t.Context(), fixture, "holding-final-source", "Final", 1490, 10); err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("source deletion lost the original frozen acknowledgement", err)
	}
	requireDomainState(t, s, before)
}

func TestHoldingPublicationAllocatesOneVersionOnePayloadPerNewSource(t *testing.T) {
	for _, kind := range []string{"unit", "stock"} {
		t.Run(kind, func(t *testing.T) {
			fixture := holdingsFixture(t)
			s := fixture.store
			identity := "new-unit"
			if kind == "stock" {
				identity = projectionPair(fixture.quantity.ID, "root")
			}
			changed := requireProjectionReplay(t, s, func() {
				ack, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "new-holding-publication", "test_new_holding", kind, func(m *mutationTx) (ActionAck, error) {
					var err error
					owner := fixture.individual.ID
					if kind == "unit" {
						_, err = m.tx.ExecContext(m.ctx, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('new-unit',?,'NEW-ASSET','2026','2026')", owner)
					} else {
						owner = fixture.quantity.ID
						_, err = m.tx.ExecContext(m.ctx, "INSERT INTO stock_balances VALUES(?,'root',0,'2026')", owner)
					}
					if err != nil {
						return ActionAck{}, err
					}
					revision, err := m.touch(kind, identity, false)
					if err != nil {
						return ActionAck{}, err
					}
					if err = m.touchItemSearch(owner, true); err != nil {
						return ActionAck{}, err
					}
					return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, nil
				})
				if err != nil || ack.Revision != 1 {
					t.Fatal("new physical source did not acquire its own retained revision one", err)
				}
			})
			if !changed[projectionKey{kind, identity}] {
				t.Fatal("new independent holding payload was not published")
			}
		})
	}
}

func TestHoldingMaterializationRejectsCorruptMissingAndExhaustedFinalSource(t *testing.T) {
	for _, kind := range []string{"unit", "stock"} {
		for _, failure := range []string{"corrupt", "missing", "overflow"} {
			t.Run(kind+failure, func(t *testing.T) {
				fixture := holdingsFixture(t)
				s := fixture.store
				id := fixture.unit.ID
				if kind == "stock" {
					id = projectionPair(fixture.stock.ItemID, fixture.stock.LocationID)
				}
				if failure == "overflow" {
					setCatalogProjectionRevision(t, s, kind, id, maxSafeInteger)
				}
				before := domainState(t, s)
				ack, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "bad-holding-materialization", "test_bad_holding", failure, func(m *mutationTx) (ActionAck, error) {
					statement, args := "UPDATE item_units SET asset_code='' WHERE id=?", []any{fixture.unit.ID}
					if kind == "stock" {
						statement, args = "UPDATE stock_balances SET quantity_base=? WHERE item_id=? AND location_id=?", []any{maxSafeInteger + 1, fixture.stock.ItemID, fixture.stock.LocationID}
					}
					if failure == "missing" {
						if kind == "unit" {
							statement = "DELETE FROM item_units WHERE id=?"
						} else {
							statement, args = "DELETE FROM stock_balances WHERE item_id=? AND location_id=?", []any{fixture.stock.ItemID, fixture.stock.LocationID}
						}
					}
					if _, err := m.tx.ExecContext(m.ctx, statement, args...); err != nil {
						return ActionAck{}, err
					}
					revision, err := m.touch(kind, id, false)
					return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, err
				})
				want := ErrDomainState
				if failure == "missing" {
					want = ErrNotFound
				} else if failure == "overflow" {
					want = ErrRevisionOverflow
				}
				if !errors.Is(err, want) || ack.OK {
					t.Fatal("invalid final holding source escaped as success", err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
	fixture := holdingsFixture(t)
	tx, err := fixture.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mutation := mutationTx{ctx: t.Context(), tx: tx}
	if _, err = mutation.projectionPayload(projectionKey{"stock", `[ "item", "location" ]`}); !errors.Is(err, ErrDomainState) {
		t.Fatal("alternate composite spelling became a stock payload identity", err)
	}
}
