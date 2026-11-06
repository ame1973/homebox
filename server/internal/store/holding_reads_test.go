package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func seedReadUnit(t *testing.T, fixture holdingFixture, id, status string) {
	t.Helper()
	value := UnitData{ID: id, ItemID: fixture.individual.ID, AssetCode: "ASSET-" + id, Status: status, SerialNumber: testPointer(""), Notes: "\x00Exact retained é\n"}
	execSQL(t, fixture.store.db, "INSERT INTO item_units(id,item_id,asset_code,status,serial_number,notes,created_at,updated_at) VALUES(?,?,?,?,?,?,'2026','2026')", value.ID, value.ItemID, value.AssetCode, value.Status, value.SerialNumber, value.Notes)
	seedCatalogProjection(t, fixture.store, "unit", id, value)
}

func seedReadStock(t *testing.T, fixture holdingFixture, itemID, locationID string, quantity int64) {
	t.Helper()
	location := LocationData{ID: locationID, Code: "C-" + authDigest(locationID), Name: "Read fixture", Kind: "other", Status: "active"}
	execSQL(t, fixture.store.db, "INSERT INTO locations(id,code,name,created_at,updated_at) VALUES(?,?,?,'2026','2026')", locationID, location.Code, location.Name)
	seedCatalogProjection(t, fixture.store, "location", locationID, location)
	execSQL(t, fixture.store.db, "INSERT INTO stock_balances VALUES(?,?,?,'2026')", itemID, locationID, quantity)
	seedCatalogProjection(t, fixture.store, "stock", projectionPair(itemID, locationID), StockData{ItemID: itemID, LocationID: locationID, QuantityBase: quantity, UnitCode: "kg", BaseUnit: "g", ScaleFactor: 1000})
}

func allReadStock(t *testing.T, inventory *Inventory, token string, filters StockFilters, size int) []Stock {
	t.Helper()
	var values []Stock
	cursor := ""
	seen := make(map[string]bool)
	for {
		page, err := inventory.PageStock(t.Context(), token, cursor, size, filters)
		if err != nil || page.Data == nil || len(page.Data) > size {
			t.Fatal("complete Stock page failed", err)
		}
		for _, value := range page.Data {
			if len(values) > 0 {
				previous := values[len(values)-1]
				if value.ItemID < previous.ItemID || value.ItemID == previous.ItemID && value.LocationID <= previous.LocationID {
					t.Fatal("Stock pages duplicated/reordered their real tuple")
				}
			}
			values = append(values, value)
		}
		if page.NextCursor == nil {
			return values
		}
		if len(page.Data) != size || seen[*page.NextCursor] {
			t.Fatal("incomplete/repeated Stock continuation")
		}
		seen[*page.NextCursor] = true
		cursor = *page.NextCursor
	}
}

func TestHoldingReadCompleteUnitAndStockCollectionsUseSourceAndNeverPublishOrFabricateTotals(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for index := range 205 {
		seedReadUnit(t, fixture, fmt.Sprintf("owned-%03d", index), []string{"stored", "in_use", "lent", "lost", "retired", "disposed"}[index%6])
		seedReadStock(t, fixture, fixture.quantity.ID, fmt.Sprintf("placement-%03d", index), int64(index))
	}
	execSQL(t, fixture.store.db, "UPDATE current_projections SET payload_json='{\"NeverReadCachedHolding\":true}' WHERE entity_type IN('unit','stock')")
	before := domainState(t, fixture.store)
	units, cursor := make([]Unit, 0), ""
	for {
		page, err := inventory.PageUnits(t.Context(), fixture.token, fixture.individual.ID, cursor, 50)
		if err != nil || page.Data == nil || len(page.Data) > 50 {
			t.Fatal(err)
		}
		for _, value := range page.Data {
			if value.ItemID != fixture.individual.ID || len(units) > 0 && value.ID <= units[len(units)-1].ID {
				t.Fatal("Unit collection escaped its owner/order")
			}
			got, err := inventory.GetUnit(t.Context(), fixture.token, value.ID)
			if err != nil || !reflect.DeepEqual(got, value) {
				t.Fatal("detail/list used different authoritative source", err)
			}
			units = append(units, value)
		}
		if page.NextCursor == nil {
			break
		}
		if len(page.Data) != 50 {
			t.Fatal("a nonterminal Unit page is not complete")
		}
		cursor = *page.NextCursor
	}
	stock := allReadStock(t, inventory, fixture.token, StockFilters{}, 50)
	if len(units) != 206 || len(stock) != 207 {
		t.Fatal("first-page truncation hid retained physical source", len(units), len(stock))
	}
	if empty, err := inventory.PageUnits(t.Context(), fixture.token, fixture.quantity.ID, "", 50); err != nil || empty.Data == nil || len(empty.Data) != 0 || empty.NextCursor != nil {
		t.Fatal("an existing quantity owner was confused with a missing Item", err)
	}
	for _, value := range stock {
		if value.UnitCode != "kg" || value.BaseUnit != "g" || value.ScaleFactor != 1000 || value.Revision != 1 {
			t.Fatal("Stock source was rescaled or assigned fake authority")
		}
	}
	encoded, _ := json.Marshal(StockList{Data: stock, NextCursor: nil})
	var fields map[string]any
	if json.Unmarshal(encoded, &fields) != nil || len(fields) != 2 || strings.Contains(string(encoded), "NeverReadCachedHolding") {
		t.Fatal("read returned cache text or a fabricated mixed-unit scalar")
	}
	requireDomainState(t, fixture.store, before)
}

func TestHoldingReadStockFiltersSelectActualBalanceRowsCategoriesAndItemStatusBeforePaging(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	taxonomy, err := NewTaxonomy(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	root := createCategory(t, taxonomy, fixture.token, "Stock root", nil)
	child := createCategory(t, taxonomy, fixture.token, "Stock child", &root.ID)
	if _, err := fixture.catalog.Patch(t.Context(), fixture.token, "classify-stock-filter", fixture.quantity.ID, fixture.quantity.Revision, ItemPatch{CategoryID: NullableID{Set: true, Value: &child.ID}}); err != nil {
		t.Fatal(err)
	}
	seedReadStock(t, fixture, fixture.quantity.ID, "elsewhere", 100)
	for _, test := range []struct {
		filters   StockFilters
		locations []string
	}{
		{StockFilters{}, []string{"elsewhere", "source", "target"}},
		{StockFilters{LocationID: "root"}, []string{"source", "target"}},
		{StockFilters{LocationID: "source", Scope: "direct"}, []string{"source"}},
		{StockFilters{LocationID: "target"}, []string{"target"}},
		{StockFilters{CategoryID: root.ID}, []string{"elsewhere", "source", "target"}},
		{StockFilters{CategoryID: root.ID, Scope: "direct"}, []string{}},
		{StockFilters{CategoryID: child.ID, Scope: "direct"}, []string{"elsewhere", "source", "target"}},
		{StockFilters{CategoryID: child.ID, LocationID: "root"}, []string{"source", "target"}},
		{StockFilters{Status: "active"}, []string{"elsewhere", "source", "target"}},
		{StockFilters{Status: "archived"}, []string{}},
		{StockFilters{LocationID: "absent"}, []string{}},
		{StockFilters{CategoryID: "absent"}, []string{}},
	} {
		got := make([]string, 0)
		for _, value := range allReadStock(t, inventory, fixture.token, test.filters, 1) {
			got = append(got, value.LocationID)
		}
		if !reflect.DeepEqual(got, test.locations) {
			t.Fatal("Stock filtering selected the Item's other balances or hid zero rows", test.filters, got, test.locations)
		}
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID)
	if _, err := fixture.catalog.Archive(t.Context(), fixture.token, "archive-stock-filter", item.ID, item.Revision, ""); err != nil {
		t.Fatal(err)
	}
	if got := allReadStock(t, inventory, fixture.token, StockFilters{Status: "archived"}, 1); len(got) != 3 {
		t.Fatal("archived catalog metadata erased readable balances")
	}
}

func TestHoldingReadStockPurchaseCriteriaMatchOneAssociatedPurchaseAndCivilDatesBeforeLookahead(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	seedCatalogPurchase(t, fixture.store, fixture.quantity.ID, "purchase-a", "2026-11-04T16:00:00Z", "TWD", "Alpha")
	seedCatalogPurchase(t, fixture.store, fixture.quantity.ID, "purchase-b", "2026-10-01T12:00:00Z", "USD", "Beta")
	seedCatalogPurchase(t, fixture.store, fixture.quantity.ID, "purchase-c", "2026-11-05T12:00:00Z", "USD", "Gamma")
	setCatalogTimezone(t, fixture.store, "Asia/Taipei")
	for _, test := range []struct {
		filters StockFilters
		count   int
	}{
		{StockFilters{From: "2026-11-05", To: "2026-11-05", Currency: "TWD", Merchant: "Alpha"}, 2},
		{StockFilters{From: "2026-11-04", To: "2026-11-04", Merchant: "Alpha"}, 0},
		{StockFilters{From: "2026-11-05", To: "2026-11-05", Currency: "USD", Merchant: "Beta"}, 0},
		{StockFilters{From: "2026-11-05", To: "2026-11-05", Currency: "USD", Merchant: "Gamma"}, 2},
		{StockFilters{Currency: "USD", Merchant: "Beta"}, 2},
		{StockFilters{Merchant: "alpha"}, 0}, {StockFilters{Merchant: " Alpha"}, 0},
	} {
		if got := allReadStock(t, inventory, fixture.token, test.filters, 1); len(got) != test.count {
			t.Fatal("date/currency/merchant matched separate purchases or a movement timestamp", test.filters, len(got))
		}
	}
	// Place an unmatched Item before the matching one. Its first SQL row must
	// not become an empty first page that silently hides later matching balances.
	seedUnmaterializedCatalogItem(t, fixture.store, "0-unmatched", "Unmatched")
	execSQL(t, fixture.store.db, "UPDATE items SET tracking_mode='quantity' WHERE id='0-unmatched'")
	seedReadStock(t, fixture, "0-unmatched", "unmatched-place", 0)
	if got := allReadStock(t, inventory, fixture.token, StockFilters{Merchant: "Alpha"}, 1); len(got) != 2 {
		t.Fatal("raw first-page slicing hid later associated-purchase matches")
	}
	before := domainState(t, fixture.store)
	execSQL(t, fixture.store.db, "UPDATE purchases SET purchased_at='2026-11-05' WHERE id='purchase-a'")
	if page, err := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{From: "2026-11-05", Merchant: "Alpha"}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("legacy date-only source acquired a guessed instant or leaked a prefix", err)
	}
	// Preserve the intentionally changed legacy source while checking that the
	// failed read never published/repaired any row or receipt.
	delete(before, "purchases")
	after := domainState(t, fixture.store)
	delete(after, "purchases")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed civil-date read changed authoritative state")
	}
}

func TestHoldingReadAdmissionMissingOwnersAndCorruptLookaheadNeverExposePrefixes(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if value, err := inventory.GetUnit(t.Context(), fixture.token, id); !errors.Is(err, ErrDomainInput) || value != (Unit{}) {
			t.Fatal("invalid Unit ID escaped", err)
		}
		if page, err := inventory.PageUnits(t.Context(), fixture.token, id, "", 1); !errors.Is(err, ErrDomainInput) || page.Data != nil {
			t.Fatal("invalid owner ID escaped", err)
		}
	}
	if value, err := inventory.GetUnit(t.Context(), fixture.token, "missing"); !errors.Is(err, ErrNotFound) || value != (Unit{}) {
		t.Fatal("missing Unit became a resource", err)
	}
	if page, err := inventory.PageUnits(t.Context(), fixture.token, "missing", "", 1); !errors.Is(err, ErrNotFound) || page.Data != nil {
		t.Fatal("missing owner became an empty collection", err)
	}
	for _, size := range []int{-1, 0, 101} {
		if page, err := inventory.PageUnits(t.Context(), fixture.token, fixture.individual.ID, "", size); !errors.Is(err, ErrDomainInput) || page.Data != nil {
			t.Fatal("invalid Unit page size escaped", err)
		}
		if page, err := inventory.PageStock(t.Context(), fixture.token, "", size, StockFilters{}); !errors.Is(err, ErrDomainInput) || page.Data != nil {
			t.Fatal("invalid Stock page size escaped", err)
		}
	}
	for _, filter := range []StockFilters{{From: "2026-02-30"}, {To: "0000-01-01"}, {From: "2026-11-06", To: "2026-11-05"}, {Currency: "twd"}, {CategoryID: strings.Repeat("x", 129)}, {LocationID: string([]byte{0xff})}, {Scope: "other"}, {Status: "stored"}, {Merchant: strings.Repeat("界", 301)}} {
		if page, err := inventory.PageStock(t.Context(), fixture.token, "", 1, filter); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid Stock criteria released a prefix", err)
		}
	}
	seedReadUnit(t, fixture, "zz-lookahead", "disposed")
	for _, key := range []projectionKey{{"unit", "zz-lookahead"}, {"stock", projectionPair(fixture.quantity.ID, "target")}} {
		execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
		execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
	}
	before := domainState(t, fixture.store)
	if page, err := inventory.PageUnits(t.Context(), fixture.token, fixture.individual.ID, "", 1); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("corrupt Unit lookahead leaked a valid first row", err)
	}
	if page, err := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("corrupt Stock lookahead leaked a valid first row", err)
	}
	requireDomainState(t, fixture.store, before)
}
