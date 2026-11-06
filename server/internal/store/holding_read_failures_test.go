package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHoldingReadSQLFailuresAndInvalidTimezoneWithholdResourcesWithoutSourceRepair(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for _, table := range []string{"item_units", "stock_balances"} {
		before := domainState(t, fixture.store)
		execSQL(t, fixture.store.db, "ALTER TABLE "+table+" RENAME TO unreadable_holding_source")
		if table == "item_units" {
			if value, err := inventory.GetUnit(t.Context(), fixture.token, fixture.unit.ID); err == nil || value != (Unit{}) {
				t.Fatal("failed detail query released a resource", err)
			}
			if page, err := inventory.PageUnits(t.Context(), fixture.token, fixture.individual.ID, "", 1); err == nil || page.Data != nil || page.NextCursor != nil {
				t.Fatal("failed Unit query released a collection", err)
			}
		} else if page, err := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{}); err == nil || page.Data != nil || page.NextCursor != nil {
			t.Fatal("failed Stock query released a collection", err)
		}
		execSQL(t, fixture.store.db, "ALTER TABLE unreadable_holding_source RENAME TO "+table)
		requireDomainState(t, fixture.store, before)
	}
	for _, encoded := range []string{`null`, `[]`, `123`, `"Local"`, `"Unknown/Zone"`} {
		execSQL(t, fixture.store.db, "INSERT INTO app_settings(key,value_json,updated_at) VALUES('preferences.timezone',?,'2026') ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json", encoded)
		before := domainState(t, fixture.store)
		if page, err := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid home timezone was silently replaced with UTC", err)
		}
		requireDomainState(t, fixture.store, before)
	}
}

func TestHoldingReadCursorPublicationRejectsInvalidOrUnencodableInternalPositionsWithoutPrefixes(t *testing.T) {
	fixture, _ := inventoryFixture(t)
	for _, position := range []string{`[ "item", "source" ]`, projectionPair(strings.Repeat("\x01", 128), strings.Repeat("\x02", 128))} {
		before := domainState(t, fixture.store)
		page, err := readDomain(t.Context(), fixture.auth, fixture.token, func(tx *sql.Tx) (StockList, error) {
			return readMetadataIdentityPageTx(t.Context(), tx, fixture.auth, fixture.token, "", 1, "test-stock-position-budget", 2048,
				func(id string) bool { return validProjectionIdentity("stock", id) },
				func(_ context.Context, _ *sql.Tx, _ string, _ int) (metadataPage[Stock], error) {
					return metadataPage[Stock]{Data: []Stock{fixture.stock}, NextID: position}, nil
				})
		})
		if !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid/oversized next position leaked its otherwise valid data prefix", err)
		}
		requireDomainState(t, fixture.store, before)
	}
}

func TestHoldingReadMixedMeasurementRowsRetainActualValuesAfterARealQuantityCommand(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	other := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Other base", TrackingMode: testPointer("quantity"), UnitCode: testPointer("L"), BaseUnit: testPointer("ml"), ScaleFactor: testPointer(int64(1000))})
	written, err := inventory.Receive(t.Context(), fixture.token, "real-other-measurement", other.Revision, StockReceive{ItemID: other.ID, TargetLocationID: "target", QuantityBase: 17})
	if err != nil {
		t.Fatal(err)
	}
	before := domainState(t, fixture.store)
	rows := allReadStock(t, inventory, fixture.token, StockFilters{}, 1)
	if len(rows) != 3 {
		t.Fatal("mixed measurements collapsed independent rows")
	}
	found := false
	for _, row := range rows {
		if row.ItemID == other.ID {
			if !reflect.DeepEqual(row, written) || row.UnitCode != "L" || row.BaseUnit != "ml" || row.QuantityBase != 17 {
				t.Fatal("mixed row reused another Item's conversion")
			}
			found = true
		} else if row.UnitCode != "kg" || row.BaseUnit != "g" || row.ScaleFactor != 1000 {
			t.Fatal("original Item's measurement was replaced")
		}
	}
	if !found {
		t.Fatal("real newly received Stock was absent from complete reads")
	}
	requireDomainState(t, fixture.store, before)
}
