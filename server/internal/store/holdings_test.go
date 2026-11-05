package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type holdingFixture struct {
	store                *Store
	catalog              *Catalog
	auth                 *Auth
	token                string
	individual, quantity Item
	unit                 Unit
	stock, target        Stock
}

// Existing source/projection fixture only, not an invented public holding API.
// Actual catalog/auth services still establish their real authority and receipts.
func holdingsFixture(t *testing.T) holdingFixture {
	t.Helper()
	s, catalog, auth, token := catalogFixture(t)
	individual := createCatalogItem(t, catalog, token, ItemCreate{Name: "Physical owner"})
	quantity := createCatalogItem(t, catalog, token, ItemCreate{Name: "Quantity owner", TrackingMode: testPointer("quantity"), UnitCode: testPointer("kg"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000))})
	for _, location := range []LocationData{
		{ID: "root", Code: "ROOT", Name: "Root", Kind: "other", Status: "active"},
		{ID: "source", ParentID: testPointer("root"), Code: "SOURCE", Name: "Source", Kind: "other", Status: "active"},
		{ID: "target", ParentID: testPointer("root"), Code: "TARGET", Name: "Target", Kind: "other", Status: "active"},
	} {
		execSQL(t, s.db, "INSERT INTO locations(id,parent_id,code,name,created_at,updated_at) VALUES(?,?,?,?, '2026','2026')", location.ID, location.ParentID, location.Code, location.Name)
		seedCatalogProjection(t, s, "location", location.ID, location)
	}
	unit := Unit{UnitData{ID: "unit", ItemID: individual.ID, LocationID: testPointer("source"), AssetCode: "  AST-001 é  ", Status: "stored", SerialNumber: testPointer(""), Notes: "Original unit notes"}, 1}
	stock := Stock{StockData{ItemID: quantity.ID, LocationID: "source", QuantityBase: 1500, UnitCode: "kg", BaseUnit: "g", ScaleFactor: 1000}, 1}
	target := Stock{StockData{ItemID: quantity.ID, LocationID: "target", QuantityBase: 0, UnitCode: "kg", BaseUnit: "g", ScaleFactor: 1000}, 1}
	execSQL(t, s.db, `INSERT INTO item_units(id,item_id,location_id,asset_code,status,serial_number,notes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'2026','2026')`, unit.ID, unit.ItemID, unit.LocationID, unit.AssetCode, unit.Status, unit.SerialNumber, unit.Notes)
	seedCatalogProjection(t, s, "unit", unit.ID, unit.UnitData)
	for _, balance := range []Stock{stock, target} {
		execSQL(t, s.db, "INSERT INTO stock_balances VALUES(?,?,?,'2026')", balance.ItemID, balance.LocationID, balance.QuantityBase)
		seedCatalogProjection(t, s, "stock", projectionPair(balance.ItemID, balance.LocationID), balance.StockData)
	}
	return holdingFixture{s, catalog, auth, token, individual, quantity, unit, stock, target}
}

func TestHoldingResultsPreserveBoundedUnicodeNullEmptyLifecycleAndSafeBaseQuantities(t *testing.T) {
	id := strings.Repeat("😀", 128)
	unit := Unit{UnitData{ID: id, ItemID: id, LocationID: &id, AssetCode: strings.Repeat("😀", 10000), SerialNumber: testPointer(strings.Repeat("😀", 10000)), Notes: strings.Repeat("😀", 10000), Status: "disposed"}, maxSafeInteger}
	stock := Stock{StockData{ItemID: id, LocationID: id, QuantityBase: maxSafeInteger, UnitCode: strings.Repeat("😀", 32), BaseUnit: strings.Repeat("a", 32), ScaleFactor: maxSafeInteger}, maxSafeInteger}
	for _, value := range []domainResult{unit, stock} {
		if err := value.validateDomainResult(); err != nil {
			t.Fatal("valid Unicode/safe-integer boundary was rejected", err)
		}
	}
	for _, status := range []string{"stored", "in_use", "lent", "lost", "retired", "disposed"} {
		value := unit
		value.Status, value.LocationID, value.SerialNumber, value.Notes = status, nil, nil, ""
		if err := value.validateDomainResult(); err != nil {
			t.Fatal("retained lifecycle/null/empty representation was rejected", err)
		}
		value.SerialNumber = testPointer("")
		value.AssetCode, value.Notes = "\x00Exact é", "\x00Manual\nnotes"
		if err := value.validateDomainResult(); err != nil {
			t.Fatal("exact native controls or supplied-empty serial were repaired", err)
		}
	}
	invalidUTF := string([]byte{0xff})
	for _, field := range []string{"id", "item_id", "location_id", "asset_code", "serial_number", "notes", "status"} {
		for _, text := range []string{invalidUTF, strings.Repeat("界", 10001), ""} {
			if text == "" && (field == "serial_number" || field == "notes") {
				continue
			}
			value := unit
			switch field {
			case "id":
				value.ID = text
			case "item_id":
				value.ItemID = text
			case "location_id":
				value.LocationID = &text
			case "asset_code":
				value.AssetCode = text
			case "serial_number":
				value.SerialNumber = &text
			case "notes":
				value.Notes = text
			case "status":
				value.Status = text
			}
			if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid physical representation escaped validation", field, err)
			}
		}
	}
	for _, field := range []string{"item_id", "location_id", "unit_code", "base_unit"} {
		for _, text := range []string{invalidUTF, "", strings.Repeat("界", 129)} {
			value := stock
			switch field {
			case "item_id":
				value.ItemID = text
			case "location_id":
				value.LocationID = text
			case "unit_code":
				value.UnitCode = text
			case "base_unit":
				value.BaseUnit = text
			}
			if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid balance identity/measurement escaped validation", field, err)
			}
		}
	}
	for _, base := range []string{"1g", " g", "g ", "g\n", "g\x00", strings.Repeat("a", 33)} {
		value := stock
		value.BaseUnit = base
		if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
			t.Fatal("invalid base identifier escaped validation", err)
		}
	}
	for _, revision := range []int64{-1, 0, maxSafeInteger + 1} {
		u, b := unit, stock
		u.Revision, b.Revision = revision, revision
		if u.validateDomainResult() == nil || b.validateDomainResult() == nil {
			t.Fatal("unsafe independent revision was accepted")
		}
	}
	for _, count := range []int64{-1, maxSafeInteger + 1} {
		value := stock
		value.QuantityBase = count
		if value.validateDomainResult() == nil {
			t.Fatal("unsafe/fractional base quantity was accepted")
		}
	}
	for _, scale := range []int64{-1, 0, maxSafeInteger + 1} {
		value := stock
		value.ScaleFactor = scale
		if value.validateDomainResult() == nil {
			t.Fatal("unsafe measurement scale was accepted")
		}
	}
	stock.QuantityBase = 0
	if err := stock.validateDomainResult(); err != nil {
		t.Fatal("retained zero balance was treated as absent", err)
	}
}

func TestHoldingLoadersUseSourceAndRealIndependentAuthorityWithoutReadSidePublication(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	for _, check := range []struct {
		kind, identity, owner string
		load                  func(context.Context, queryer, bool) (domainResult, error)
		scan                  func(rowScanner) (domainResult, error)
		want                  domainResult
	}{
		{"unit", fixture.unit.ID, fixture.individual.ID, func(ctx context.Context, q queryer, missing bool) (domainResult, error) {
			id := fixture.unit.ID
			if missing {
				id = "missing"
			}
			return loadUnit(ctx, q, id)
		}, func(row rowScanner) (domainResult, error) { return scanUnit(row) }, fixture.unit},
		{"stock", projectionPair(fixture.stock.ItemID, fixture.stock.LocationID), fixture.quantity.ID, func(ctx context.Context, q queryer, missing bool) (domainResult, error) {
			location := fixture.stock.LocationID
			if missing {
				location = "missing"
			}
			return loadStock(ctx, q, fixture.stock.ItemID, location)
		}, func(row rowScanner) (domainResult, error) { return scanStock(row) }, fixture.stock},
	} {
		t.Run(check.kind, func(t *testing.T) {
			execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"private_cache\":true}' WHERE entity_type=? AND entity_id=?", check.kind, check.identity)
			before := domainState(t, s)
			value, err := check.load(t.Context(), s.db, false)
			if err != nil || !reflect.DeepEqual(value, check.want) {
				t.Fatal("loader read cached payload or invented a revision", err)
			}
			requireDomainState(t, s, before)
			if value, err = check.load(t.Context(), s.db, true); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("missing source exposed a partial resource", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if value, err = check.load(ctx, s.db, false); !errors.Is(err, context.Canceled) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("canceled query exposed source", err)
			}
			if value, err = check.scan(s.db.QueryRowContext(t.Context(), "SELECT 1")); err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("failed scan exposed source", err)
			}
			for _, scope := range []projectionKey{{check.kind, check.identity}, {"item", check.owner}} {
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", scope.kind, scope.id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", scope.kind, scope.id)
				before = domainState(t, s)
				if value, err = check.load(t.Context(), s.db, false); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("missing row/owner counter was synthesized", err)
				}
				requireDomainState(t, s, before)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES(?,?,?)", scope.kind, scope.id, maxSafeInteger+1)
				if value, err = check.load(t.Context(), s.db, false); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("unsafe row/owner authority was accepted", err)
				}
				execSQL(t, s.db, "UPDATE projection_revisions SET revision=1 WHERE entity_type=? AND entity_id=?", scope.kind, scope.id)
			}
			encoded, err := json.Marshal(check.want)
			if err != nil || strings.Contains(string(encoded), "private_cache") {
				t.Fatal("physical resource exposed private cache data", err)
			}
		})
	}
}

func TestHoldingLoadersRejectModeCorruptionAndReadStockMeasurementFromItsActualOwner(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	execSQL(t, s.db, "UPDATE item_units SET item_id=? WHERE id=?", fixture.quantity.ID, fixture.unit.ID)
	if value, err := loadUnit(t.Context(), s.db, fixture.unit.ID); !errors.Is(err, ErrDomainState) || value != (Unit{}) {
		t.Fatal("quantity Item was treated as an individual owner", err)
	}
	execSQL(t, s.db, "UPDATE item_units SET item_id=? WHERE id=?", fixture.individual.ID, fixture.unit.ID)
	execSQL(t, s.db, "UPDATE items SET tracking_mode='individual' WHERE id=?", fixture.quantity.ID)
	if value, err := loadStock(t.Context(), s.db, fixture.stock.ItemID, fixture.stock.LocationID); !errors.Is(err, ErrDomainState) || value != (Stock{}) {
		t.Fatal("individual Item was treated as quantity stock", err)
	}
	execSQL(t, s.db, "UPDATE items SET tracking_mode='quantity',unit_code='L',base_unit='ml',scale_factor=1000 WHERE id=?", fixture.quantity.ID)
	before := domainState(t, s)
	value, err := loadStock(t.Context(), s.db, fixture.stock.ItemID, fixture.stock.LocationID)
	if err != nil || value.QuantityBase != 1500 || value.UnitCode != "L" || value.BaseUnit != "ml" || value.ScaleFactor != 1000 {
		t.Fatal("loader copied stale measurement or rescaled source", err)
	}
	requireDomainState(t, s, before)
}
