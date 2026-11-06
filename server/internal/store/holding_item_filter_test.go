package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestHoldingReadStockItemFilterIsExactAndAppliedBeforeUnrelatedAuthorityAndPaging(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	unicodeID, maximumID := "  品項 +?# é  ", strings.Repeat("😀", 128)
	for index, id := range []string{"!unrelated", unicodeID, maximumID} {
		seedUnmaterializedCatalogItem(t, fixture.store, id, "Retained quantity item")
		execSQL(t, fixture.store.db, "UPDATE items SET tracking_mode='quantity',unit_code='kg',base_unit='g',scale_factor=1000 WHERE id=?", id)
		seedReadStock(t, fixture, id, "other-place-"+string(rune('a'+index)), 0)
	}
	before := domainState(t, fixture.store)
	for _, test := range []struct {
		filters StockFilters
		count   int
	}{
		{StockFilters{ItemID: fixture.quantity.ID}, 2},
		{StockFilters{ItemID: fixture.quantity.ID, LocationID: "root"}, 2},
		{StockFilters{ItemID: fixture.quantity.ID, LocationID: "source", Scope: "direct"}, 1},
		{StockFilters{ItemID: fixture.quantity.ID, CategoryID: "missing"}, 0},
		{StockFilters{ItemID: fixture.quantity.ID, Status: "archived"}, 0},
		{StockFilters{ItemID: fixture.quantity.ID, Merchant: "missing"}, 0},
		{StockFilters{ItemID: fixture.individual.ID}, 0},
		{StockFilters{ItemID: "missing"}, 0},
		{StockFilters{ItemID: "' OR 1=1--"}, 0},
		{StockFilters{ItemID: unicodeID}, 1},
		{StockFilters{ItemID: strings.TrimSpace(unicodeID)}, 0},
		{StockFilters{ItemID: maximumID}, 1},
	} {
		rows := allReadStock(t, inventory, fixture.token, test.filters, 1)
		if len(rows) != test.count {
			t.Fatal("Item selection was sliced, widened, trimmed or used as a source-existence claim", test.filters, len(rows))
		}
		for _, row := range rows {
			if row.ItemID != test.filters.ItemID {
				t.Fatal("Item filter returned another owner's balance")
			}
		}
	}
	requireDomainState(t, fixture.store, before)

	// An unrelated invalid row must not force an Item-scoped editor to scan the
	// whole inventory. The same row must still fail a truly unfiltered read.
	execSQL(t, fixture.store.db, "UPDATE items SET tracking_mode='individual' WHERE id='!unrelated'")
	before = domainState(t, fixture.store)
	if page, err := inventory.PageStock(t.Context(), fixture.token, "", 100, StockFilters{}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("unfiltered read hid corrupt source", err)
	}
	if rows := allReadStock(t, inventory, fixture.token, StockFilters{ItemID: fixture.quantity.ID}, 1); len(rows) != 2 {
		t.Fatal("Item filter scanned unrelated invalid source")
	}
	var order, parent, unused int
	var detail string
	err := fixture.store.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN "+itemFilterCTEs+stockSelect+" WHERE (b.item_id,b.location_id)>(?,?) AND b.item_id=? ORDER BY b.item_id,b.location_id", "", "", "", "", fixture.quantity.ID).Scan(&order, &parent, &unused, &detail)
	if err != nil || !strings.Contains(detail, "SEARCH b USING PRIMARY KEY") || !strings.Contains(detail, "item_id=?") {
		t.Fatal("Item-scoped stock lookup no longer uses its actual pair primary key", detail, err)
	}
	if page, err := inventory.PageStock(t.Context(), "", "", 1, StockFilters{ItemID: "missing"}); !errors.Is(err, ErrAuthentication) || page.Data != nil {
		t.Fatal("an empty Item selection bypassed authorization", err)
	}
	requireDomainState(t, fixture.store, before)
	identity := projectionPair(fixture.quantity.ID, "source")
	execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='stock' AND entity_id=?", identity)
	execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type='stock' AND entity_id=?", identity)
	before = domainState(t, fixture.store)
	if page, err := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{ItemID: fixture.quantity.ID}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("Item filtering bypassed its selected balance's actual authority", err)
	}
	requireDomainState(t, fixture.store, before)
}

func TestHoldingReadStockItemCursorsBindSelectionAndPreservePreexistingUnfilteredMACs(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	selected := StockFilters{ItemID: fixture.quantity.ID}
	filtered, err := inventory.PageStock(t.Context(), fixture.token, "", 1, selected)
	if err != nil || filtered.NextCursor == nil {
		t.Fatal("missing Item-scoped continuation", err)
	}
	unfiltered, err := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{})
	if err != nil || unfiltered.NextCursor == nil {
		t.Fatal("missing unfiltered continuation", err)
	}
	for _, test := range []struct {
		cursor  string
		filters StockFilters
	}{
		{*filtered.NextCursor, StockFilters{}},
		{*filtered.NextCursor, StockFilters{ItemID: "another"}},
		{*unfiltered.NextCursor, selected},
	} {
		if page, err := inventory.PageStock(t.Context(), fixture.token, test.cursor, 1, test.filters); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("Stock continuation silently changed its Item selection", err)
		}
	}
	equivalent := selected
	equivalent.Scope = "subtree"
	if page, err := inventory.PageStock(t.Context(), fixture.token, *filtered.NextCursor, 1, equivalent); err != nil || len(page.Data) != 1 || page.NextCursor != nil || page.Data[0].ItemID != fixture.quantity.ID {
		t.Fatal("equivalent defaults changed the Item scope", err)
	}

	// Re-sign using the exact old two-field scope. An absent new criterion must
	// not invalidate otherwise live tokens issued before the additive filter.
	normalized, _, _, err := normalizeItemFilters(ItemFilters{})
	if err != nil {
		t.Fatal(err)
	}
	legacyScope, err := json.Marshal(struct {
		Filters  ItemFilters `json:"filters"`
		Timezone string      `json:"timezone"`
	}{normalized, "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.Split(*unfiltered.NextCursor, ".")[0])
	var position metadataCursor
	if err != nil || json.Unmarshal(body, &position) != nil {
		t.Fatal("invalid continuation fixture", err)
	}
	legacyToken, err := encodeMetadataCursor(fixture.auth, stockCursorPurpose+":"+authDigest(string(legacyScope)), position)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := inventory.PageStock(t.Context(), fixture.token, legacyToken, 1, StockFilters{}); err != nil || len(page.Data) != 1 || page.NextCursor != nil {
		t.Fatal("the omitted Item criterion changed existing unfiltered cursor spelling/MAC", err)
	}
}
