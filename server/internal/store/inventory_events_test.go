package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func insertInventoryEventTest(t *testing.T, tx *sql.Tx, value InventoryEventData, revision int64, note, reason string) {
	t.Helper()
	_, err := tx.ExecContext(t.Context(), `INSERT INTO inventory_events(id,item_id,item_unit_id,from_location_id,to_location_id,action,quantity_base,occurred_at,note,reason)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, value.ID, value.ItemID, value.ItemUnitID, value.FromLocationID, value.ToLocationID, value.Action, value.QuantityBase, value.OccurredAt, note, reason)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 0 {
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('inventory_event',?,?)", value.ID, revision); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInventoryEventLoaderKeepsExactPayloadAndRequiresRealBoundedAuthority(t *testing.T) {
	fixture, _ := inventoryFixture(t)
	for _, quantity := range []int64{-maxSafeInteger, 0, maxSafeInteger} {
		t.Run(strconv.FormatInt(quantity, 10), func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			value := InventoryEventData{ID: strings.Repeat("界", 128), ItemID: fixture.quantity.ID, Action: "\x00Retained é\n", OccurredAt: "2026-11-04T20:00:00.123456789+08:00", QuantityBase: quantity}
			insertInventoryEventTest(t, tx, value, maxSafeInteger, "\x00Exact note", strings.Repeat("😀", 10000))
			if got, err := loadInventoryEvent(t.Context(), tx, value.ID); err != nil || !reflect.DeepEqual(got, value) {
				t.Fatal("event read repaired source or invented counter", err)
			}
			encoded, err := json.Marshal(value)
			var fields map[string]any
			if err != nil || json.Unmarshal(encoded, &fields) != nil || len(fields) != 8 || fields["revision"] != nil || fields["note"] != nil || fields["reason"] != nil || fields["mutation_event_id"] != nil {
				t.Fatal("offline movement payload changed shape", err)
			}
		})
	}
	// Nullable endpoints remain exact even for retained individual history. This
	// loader certifies representation; command-specific physical policy is separate.
	tx, err := fixture.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	value := InventoryEventData{ID: "retained-unit-movement", ItemID: fixture.individual.ID, ItemUnitID: testPointer(fixture.unit.ID), FromLocationID: testPointer("source"), ToLocationID: testPointer("target"), Action: "move", OccurredAt: "2026-11-04T12:00:00Z", QuantityBase: 1}
	insertInventoryEventTest(t, tx, value, 1, "", "")
	if got, err := loadInventoryEvent(t.Context(), tx, value.ID); err != nil || !reflect.DeepEqual(got, value) {
		t.Fatal("retained nullable identity/direction changed", err)
	}
}

func TestInventoryEventLoaderRejectsUnsupportedInstantsUnsafeCountsUnicodeAndMissingAuthority(t *testing.T) {
	fixture, _ := inventoryFixture(t)
	for _, invalid := range []string{"id empty", "id wide", "id UTF", "action empty", "action UTF", "action NUL bound", "date-only", "single hour", "comma fraction", "offset hour", "offset minute", "year zero", "bad calendar", "time UTF", "time suffix", "quantity below", "quantity above", "counter missing", "counter unsafe", "note UTF", "reason UTF", "note NUL bound", "reason NUL bound"} {
		t.Run(invalid, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			value := InventoryEventData{ID: "invalid-event", ItemID: fixture.quantity.ID, Action: "adjust", OccurredAt: "2026-11-04T12:00:00Z", QuantityBase: 0}
			revision, note, reason := int64(1), "", ""
			switch invalid {
			case "id empty":
				value.ID = ""
			case "id wide":
				value.ID = strings.Repeat("界", 129)
			case "id UTF":
				value.ID = string([]byte{0xff})
			case "action empty":
				value.Action = ""
			case "action UTF":
				value.Action = string([]byte{0xff})
			case "action NUL bound":
				value.Action = "\x00" + strings.Repeat("界", 10000)
			case "date-only":
				value.OccurredAt = "2026-11-04"
			case "single hour":
				value.OccurredAt = "2026-11-04T1:00:00Z"
			case "comma fraction":
				value.OccurredAt = "2026-11-04T12:00:00,1Z"
			case "offset hour":
				value.OccurredAt = "2026-11-04T12:00:00+24:00"
			case "offset minute":
				value.OccurredAt = "2026-11-04T12:00:00+00:60"
			case "year zero":
				value.OccurredAt = "0000-11-04T12:00:00Z"
			case "bad calendar":
				value.OccurredAt = "2026-02-30T12:00:00Z"
			case "time UTF":
				value.OccurredAt = string([]byte{0xff})
			case "time suffix":
				value.OccurredAt += "\n"
			case "quantity below":
				value.QuantityBase = -maxSafeInteger - 1
			case "quantity above":
				value.QuantityBase = maxSafeInteger + 1
			case "counter missing":
				revision = 0
			case "counter unsafe":
				revision = maxSafeInteger + 1
			case "note UTF":
				note = string([]byte{0xff})
			case "reason UTF":
				reason = string([]byte{0xff})
			case "note NUL bound":
				note = "\x00" + strings.Repeat("界", 10000)
			case "reason NUL bound":
				reason = "\x00" + strings.Repeat("界", 10000)
			}
			insertInventoryEventTest(t, tx, value, revision, note, reason)
			if got, err := loadInventoryEvent(t.Context(), tx, value.ID); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(got).IsZero() {
				t.Fatal("invalid immutable source escaped as a published movement", err)
			}
		})
	}
}

func TestInventoryEventLoaderRejectsMalformedReferencedIdentitiesAndWithholdsQueryFailures(t *testing.T) {
	fixture, _ := inventoryFixture(t)
	for _, field := range []string{"item", "unit", "from", "to"} {
		for _, invalid := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			value := InventoryEventData{ID: "bad-reference-event", ItemID: fixture.quantity.ID, Action: "retained", OccurredAt: "2026-11-04T12:00:00Z"}
			var statement string
			args := []any{invalid}
			switch field {
			case "item":
				statement = "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Invalid identity','individual','2026','2026')"
				value.ItemID = invalid
			case "unit":
				statement = "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,'INVALID-REFERENCE','2026','2026')"
				args = append(args, fixture.individual.ID)
				value.ItemID = fixture.individual.ID
				value.ItemUnitID = &invalid
			default:
				statement = "INSERT INTO locations(id,code,name,created_at,updated_at) VALUES(?,'INVALID-REFERENCE','Invalid identity','2026','2026')"
				if field == "from" {
					value.FromLocationID = &invalid
				} else {
					value.ToLocationID = &invalid
				}
			}
			if _, err = tx.ExecContext(t.Context(), statement, args...); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			insertInventoryEventTest(t, tx, value, 1, "", "")
			got, err := loadInventoryEvent(t.Context(), tx, value.ID)
			_ = tx.Rollback()
			if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(got).IsZero() {
				t.Fatal("invalid linked identity escaped source validation", field, err)
			}
		}
	}
	before := domainState(t, fixture.store)
	if got, err := loadInventoryEvent(t.Context(), fixture.store.db, "missing"); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(got).IsZero() {
		t.Fatal("missing movement exposed a partial payload", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := loadInventoryEvent(ctx, fixture.store.db, "missing"); !errors.Is(err, context.Canceled) || !reflect.ValueOf(got).IsZero() {
		t.Fatal("canceled source lookup exposed a payload", err)
	}
	requireDomainState(t, fixture.store, before)
	tx, err := fixture.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), "DROP TABLE inventory_events"); err != nil {
		t.Fatal(err)
	}
	if got, err := loadInventoryEvent(t.Context(), tx, "missing"); err == nil || !reflect.ValueOf(got).IsZero() {
		t.Fatal("failed source query exposed a partial payload")
	}
}

func TestInventoryImmutableHistoryCannotBeChangedAndOneAuditCanAssociateMultipleMovements(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	if _, err := inventory.Receive(t.Context(), fixture.token, "immutable-real-movement", fixture.quantity.Revision, StockReceive{ItemID: fixture.quantity.ID, TargetLocationID: "target", QuantityBase: 1}); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, fixture.store)
	for _, statement := range []string{"UPDATE inventory_events SET note='changed'", "UPDATE inventory_events SET quantity_base=99", "DELETE FROM inventory_events", "UPDATE mutation_events SET note='changed'", "DELETE FROM mutation_events"} {
		if _, err := fixture.store.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("immutable history was changed", statement)
		}
		requireDomainState(t, fixture.store, before)
	}
	changed := requireProjectionReplay(t, fixture.store, func() {
		_, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "test-multi-movement-association", "test_inventory_event_association", struct{}{}, func(m *mutationTx) (ActionAck, error) {
			for range 2 {
				if err := m.appendInventoryMovement(fixture.quantity.ID, nil, "retained_test", nil, testPointer("target"), 0, "", ""); err != nil {
					return ActionAck{}, err
				}
			}
			return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
		})
		if err != nil {
			t.Fatal("deferred association incorrectly enforces one event per domain audit", err)
		}
	})
	if len(changed) != 2 || scalar[int](t, fixture.store.db, `SELECT COUNT(*) FROM inventory_events e JOIN mutation_events m ON m.id=e.mutation_event_id WHERE m.operation='test_inventory_event_association'`) != 2 {
		t.Fatal("multiple exact event associations were not frozen independently")
	}
}
