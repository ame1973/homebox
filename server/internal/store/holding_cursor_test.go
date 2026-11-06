package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readStockPurposeTest(t *testing.T, filters StockFilters, zone string) string {
	t.Helper()
	normalized, _, _, err := normalizeItemFilters(ItemFilters{From: filters.From, To: filters.To, Currency: filters.Currency, CategoryID: filters.CategoryID, LocationID: filters.LocationID, Scope: filters.Scope, Status: filters.Status, Merchant: filters.Merchant})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(struct {
		Filters  ItemFilters `json:"filters"`
		Timezone string      `json:"timezone"`
		ItemID   string      `json:"item_id,omitempty"`
	}{normalized, zone, filters.ItemID})
	if err != nil {
		t.Fatal(err)
	}
	return stockCursorPurpose + ":" + authDigest(string(encoded))
}

func TestHoldingReadStockCursorPreservesMaximumControlUnicodePairsWithinItsOwnLargerBudget(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	itemID := strings.Repeat("\x03", 128)
	execSQL(t, fixture.store.db, "INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,created_at,updated_at) VALUES(?,'Imported','quantity','kg','g',1000,'2026','2026')", itemID)
	seedCatalogProjection(t, fixture.store, "item", itemID, ItemData{ID: itemID, Name: "Imported", TrackingMode: "quantity", UnitCode: "kg", BaseUnit: "g", ScaleFactor: 1000, Status: "active"})
	for _, location := range []string{strings.Repeat("\x01", 128), strings.Repeat("\x02", 128)} {
		seedReadStock(t, fixture, itemID, location, 0)
	}
	page, err := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{})
	if err != nil || len(page.Data) != 1 || page.NextCursor == nil || page.Data[0].ItemID != itemID || len(*page.NextCursor) <= 2048 || len(*page.NextCursor) > 4096 {
		t.Fatal("full canonical pair was truncated, rejected or escaped beyond its explicit budget", err)
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.Split(*page.NextCursor, ".")[0])
	var value metadataCursor
	if err != nil || json.Unmarshal(body, &value) != nil || value.AfterID != projectionPair(itemID, strings.Repeat("\x01", 128)) {
		t.Fatal("cursor did not preserve both original identity components", err)
	}
	if next, err := inventory.PageStock(t.Context(), fixture.token, *page.NextCursor, 1, StockFilters{}); err != nil || len(next.Data) != 1 || next.Data[0].ItemID != itemID || next.Data[0].LocationID != strings.Repeat("\x02", 128) {
		t.Fatal("Stock continuation compared an escaped JSON string instead of the real SQL tuple", err)
	}
	selected := StockFilters{ItemID: itemID}
	filtered, err := inventory.PageStock(t.Context(), fixture.token, "", 1, selected)
	if err != nil || filtered.NextCursor == nil || len(*filtered.NextCursor) <= 2048 || len(*filtered.NextCursor) > 4096 {
		t.Fatal("Item filter enlarged or truncated the full composite continuation", err)
	}
	if next, err := inventory.PageStock(t.Context(), fixture.token, *filtered.NextCursor, 1, selected); err != nil || len(next.Data) != 1 || next.Data[0].ItemID != itemID {
		t.Fatal("Item selection did not retain full control-code-point identity", err)
	}
	if _, err := decodeMetadataCursor(fixture.auth, readStockPurposeTest(t, StockFilters{}, "UTC"), *page.NextCursor, value.AdminID, value.Epoch, 1, fixture.auth.now()); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("the existing single-ID codec silently acquired a larger/composite identity policy", err)
	}
	for _, test := range []struct {
		size  int
		stock bool
	}{{2049, false}, {4097, true}} {
		if test.stock {
			if page, err := inventory.PageStock(t.Context(), fixture.token, strings.Repeat("x", test.size), 1, StockFilters{}); !errors.Is(err, ErrDomainInput) || page.Data != nil {
				t.Fatal("oversized Stock cursor escaped admission", err)
			}
		} else if page, err := inventory.PageUnits(t.Context(), fixture.token, fixture.individual.ID, strings.Repeat("x", test.size), 1); !errors.Is(err, ErrDomainInput) || page.Data != nil {
			t.Fatal("Unit cursor lost its old budget", err)
		}
	}
}

func TestHoldingReadStockCursorsBindEveryEffectiveFilterTimezoneAndPageSizeWithoutPrivateFilterText(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	taxonomy, err := NewTaxonomy(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, fixture.token, "Private category", nil)
	if _, err := fixture.catalog.Patch(t.Context(), fixture.token, "classify-stock-cursor", fixture.quantity.ID, fixture.quantity.Revision, ItemPatch{CategoryID: NullableID{Set: true, Value: &category.ID}}); err != nil {
		t.Fatal(err)
	}
	seedCatalogPurchase(t, fixture.store, fixture.quantity.ID, "scope-purchase", "2026-11-05T12:00:00Z", "TWD", "PrivateMerchant")
	filters := StockFilters{From: "2026-11-05", To: "2026-11-06", Currency: "TWD", CategoryID: category.ID, LocationID: "root", Status: "active", Merchant: "PrivateMerchant"}
	first, err := inventory.PageStock(t.Context(), fixture.token, "", 1, filters)
	if err != nil || first.NextCursor == nil {
		t.Fatal(err)
	}
	body, _ := base64.RawURLEncoding.DecodeString(strings.Split(*first.NextCursor, ".")[0])
	if strings.Contains(string(body), "PrivateMerchant") || strings.Contains(string(body), category.ID) || strings.Contains(string(body), fixture.token) {
		t.Fatal("cursor exposed filter/private credential text")
	}
	for _, mutate := range []func(*StockFilters){
		func(v *StockFilters) { v.From = "2026-11-04" }, func(v *StockFilters) { v.To = "2026-11-07" }, func(v *StockFilters) { v.Currency = "USD" },
		func(v *StockFilters) { v.CategoryID = "other" }, func(v *StockFilters) { v.LocationID = "other" }, func(v *StockFilters) { v.Scope = "direct" },
		func(v *StockFilters) { v.ItemID = "other" },
		func(v *StockFilters) { v.Status = "archived" }, func(v *StockFilters) { v.Merchant = "privatemerchant" },
	} {
		changed := filters
		mutate(&changed)
		if page, err := inventory.PageStock(t.Context(), fixture.token, *first.NextCursor, 1, changed); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("Stock continuation adopted a changed filter", err)
		}
	}
	if _, err := inventory.PageStock(t.Context(), fixture.token, *first.NextCursor, 2, filters); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("Stock cursor adopted a changed page size", err)
	}
	equivalent := filters
	equivalent.Scope = "subtree"
	setCatalogTimezone(t, fixture.store, "UTC")
	if page, err := inventory.PageStock(t.Context(), fixture.token, *first.NextCursor, 1, equivalent); err != nil || len(page.Data) != 1 {
		t.Fatal("equivalent defaults changed continuation scope", err)
	}
	setCatalogTimezone(t, fixture.store, "Asia/Taipei")
	if _, err := inventory.PageStock(t.Context(), fixture.token, *first.NextCursor, 1, filters); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("Stock cursor adopted a different effective civil timezone", err)
	}
}

func TestHoldingReadCursorsRejectOtherOwnersCollectionsPurposesAndMalformedSignedPositions(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	seedReadUnit(t, fixture, "zz-unit", "lost")
	units, err := inventory.PageUnits(t.Context(), fixture.token, fixture.individual.ID, "", 1)
	if err != nil || units.NextCursor == nil {
		t.Fatal(err)
	}
	stock, err := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{})
	if err != nil || stock.NextCursor == nil {
		t.Fatal(err)
	}
	if _, err := inventory.PageUnits(t.Context(), fixture.token, fixture.quantity.ID, *units.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("Unit cursor escaped its original owner", err)
	}
	if _, err := inventory.PageUnits(t.Context(), fixture.token, fixture.individual.ID, *stock.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("Stock cursor entered Unit scope", err)
	}
	if _, err := inventory.PageStock(t.Context(), fixture.token, *units.NextCursor, 1, StockFilters{}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("Unit cursor entered Stock scope", err)
	}
	locations, _ := NewLocations(fixture.auth)
	locationPage, err := locations.Page(t.Context(), fixture.token, "", 1)
	if err != nil || locationPage.NextCursor == nil {
		t.Fatal(err)
	}
	catalogPage, err := fixture.catalog.Page(t.Context(), fixture.token, "", 1, ItemFilters{})
	if err != nil || catalogPage.NextCursor == nil {
		t.Fatal(err)
	}
	for _, cursor := range []string{*locationPage.NextCursor, *catalogPage.NextCursor, "malformed", "a.b.c", *stock.NextCursor + "="} {
		if page, err := inventory.PageStock(t.Context(), fixture.token, cursor, 1, StockFilters{}); !errors.Is(err, ErrInvalidCursor) || page.Data != nil {
			t.Fatal("foreign/malformed cursor returned Stock data", err)
		}
	}
	body, _ := base64.RawURLEncoding.DecodeString(strings.Split(*stock.NextCursor, ".")[0])
	var value metadataCursor
	if json.Unmarshal(body, &value) != nil {
		t.Fatal("invalid test cursor fixture")
	}
	purpose := readStockPurposeTest(t, StockFilters{}, "UTC")
	for _, position := range []string{"source", `[ "item", "source" ]`, `["item"]`, `["item",""]`, `["item","` + strings.Repeat("x", 129) + `"]`} {
		changed := value
		changed.AfterID = position
		cursor, err := encodeMetadataCursor(fixture.auth, purpose, changed)
		if err != nil {
			t.Fatal(err)
		}
		if page, err := inventory.PageStock(t.Context(), fixture.token, cursor, 1, StockFilters{}); !errors.Is(err, ErrInvalidCursor) || page.Data != nil {
			t.Fatal("signed malformed/composite position bypassed original component bounds", err)
		}
	}
}

func TestHoldingReadCursorsAreLiveButNotCredentialsOrFrozenSnapshots(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	seedReadUnit(t, fixture, "zz-unit", "disposed")
	units, _ := inventory.PageUnits(t.Context(), fixture.token, fixture.individual.ID, "", 1)
	stock, _ := inventory.PageStock(t.Context(), fixture.token, "", 1, StockFilters{})
	if units.NextCursor == nil || stock.NextCursor == nil {
		t.Fatal("cursor fixture is incomplete")
	}
	execSQL(t, fixture.store.db, "DELETE FROM item_units WHERE id=?", units.Data[0].ID)
	execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", units.Data[0].ID)
	execSQL(t, fixture.store.db, "DELETE FROM stock_balances WHERE item_id=? AND location_id=?", stock.Data[0].ItemID, stock.Data[0].LocationID)
	execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='stock' AND entity_id=?", projectionPair(stock.Data[0].ItemID, stock.Data[0].LocationID))
	_, fresh := login(t, fixture.auth)
	if next, err := inventory.PageUnits(t.Context(), fresh, fixture.individual.ID, *units.NextCursor, 1); err != nil || len(next.Data) != 1 || next.Data[0].ID != "zz-unit" {
		t.Fatal("live Unit continuation required its last source row to survive", err)
	}
	if next, err := inventory.PageStock(t.Context(), fresh, *stock.NextCursor, 1, StockFilters{}); err != nil || len(next.Data) != 1 || next.Data[0].LocationID != "target" {
		t.Fatal("live Stock continuation required its last pair source to survive", err)
	}
	if page, err := inventory.PageStock(t.Context(), *stock.NextCursor, "", 1, StockFilters{}); !errors.Is(err, ErrAuthentication) || page.Data != nil {
		t.Fatal("read cursor became a credential", err)
	}
	now := fixture.auth.now().Add(metadataCursorLifetime)
	fixture.auth.now = func() time.Time { return now }
	if _, err := inventory.PageStock(t.Context(), fresh, *stock.NextCursor, 1, StockFilters{}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("expired Stock continuation was accepted", err)
	}
}

func TestHoldingReadAuthorizationCancellationFinalCommitAndSigningFailuresWithholdAllResults(t *testing.T) {
	for _, failure := range []string{"mandatory", "revoked", "epoch", "cancelled", "session write", "deferred commit", "late expiry", "signing key"} {
		t.Run(failure, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			seedReadUnit(t, fixture, "zz-unit", "stored")
			want := ErrAuthentication
			ctx := t.Context()
			switch failure {
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, fixture.store.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='other-read-epoch'")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "session write":
				execSQL(t, fixture.store.db, "CREATE TRIGGER fail_holding_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'holding read failure'); END")
				want = nil
			case "deferred commit":
				execSQL(t, fixture.store.db, "CREATE TABLE holding_read_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, fixture.store.db, "CREATE TRIGGER fail_holding_read AFTER UPDATE ON sessions BEGIN INSERT INTO holding_read_guard VALUES('missing'); END")
				want = nil
			case "signing key":
				fixture.auth.serverSecret = nil
				want = ErrDomainState
			}
			before := domainState(t, fixture.store)
			base := fixture.auth.now()
			for _, kind := range []string{"detail", "units", "stock"} {
				if failure == "signing key" && kind == "detail" {
					continue
				}
				if failure == "late expiry" {
					calls, allowed := 0, 2
					if kind == "detail" {
						allowed = 1
					}
					fixture.auth.now = func() time.Time {
						calls++
						if calls > allowed {
							return base.Add(31 * 24 * time.Hour)
						}
						return base
					}
				}
				var err error
				var result any
				switch kind {
				case "detail":
					result, err = inventory.GetUnit(ctx, fixture.token, fixture.unit.ID)
				case "units":
					result, err = inventory.PageUnits(ctx, fixture.token, fixture.individual.ID, "", 1)
				case "stock":
					result, err = inventory.PageStock(ctx, fixture.token, "", 1, StockFilters{})
				}
				if err == nil || want != nil && !errors.Is(err, want) || !reflect.ValueOf(result).IsZero() {
					t.Fatal("failed final authorization/commit/signing released a physical read or prefix", kind, err)
				}
				requireDomainState(t, fixture.store, before)
			}
		})
	}
}
