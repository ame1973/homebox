package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestLocationSummaryReadsActualSubtreeAndKeepsUnitsSeparate(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := fixture.store.NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	summary := func(id string) LocationSummary {
		t.Helper()
		value, err := locations.Summary(ctx, fixture.token, id)
		if err != nil {
			t.Fatal(err)
		}
		location := getLocation(t, locations, fixture.token, id)
		if value.LocationID != id || value.Revision != location.Revision {
			t.Fatal("summary did not retain the actual aggregate location revision", value, location)
		}
		return value
	}

	root := summary("root")
	if root.DirectUnits != 0 || root.SubtreeUnits != 1 || root.DistinctItemCount != 2 ||
		!reflect.DeepEqual(root.DirectQuantities, []QuantityGroup{}) ||
		!reflect.DeepEqual(root.SubtreeQuantities, []QuantityGroup{{BaseUnit: "g", QuantityBase: 1500}}) {
		t.Fatal("initial summary counted zero stock as present or lost a direct/subtree holding", root)
	}
	if target := summary("target"); target.DirectUnits != 0 || target.SubtreeUnits != 0 || target.DistinctItemCount != 0 || len(target.DirectQuantities) != 0 || len(target.SubtreeQuantities) != 0 {
		t.Fatal("a retained zero Stock row became a present holding", target)
	}

	individual := fixture.individual
	first, err := inventory.CreateUnit(ctx, fixture.token, "summary-root-unit", individual.ID, individual.Revision, UnitCreate{LocationID: testPointer("root")})
	if err != nil {
		t.Fatal(err)
	}
	individual, err = fixture.catalog.Get(ctx, fixture.token, individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.CreateUnit(ctx, fixture.token, "summary-child-unit", individual.ID, individual.Revision, UnitCreate{LocationID: testPointer("target")}); err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.SetUnitStatus(ctx, fixture.token, "summary-dispose", fixture.unit.ID, fixture.unit.Revision, UnitStatus{Status: "disposed", DisposalKind: testPointer("recycled")}); err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.Transfer(ctx, fixture.token, "summary-transfer", fixture.quantity.Revision, StockTransfer{ItemID: fixture.quantity.ID, SourceLocationID: "source", TargetLocationID: "root", QuantityBase: 400}); err != nil {
		t.Fatal(err)
	}
	liquid := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Liquid", TrackingMode: testPointer("quantity"), UnitCode: testPointer("l"), BaseUnit: testPointer("ml"), ScaleFactor: testPointer(int64(1000))})
	if _, err = inventory.Receive(ctx, fixture.token, "summary-liquid", liquid.Revision, StockReceive{ItemID: liquid.ID, TargetLocationID: "target", QuantityBase: 1200}); err != nil {
		t.Fatal(err)
	}
	changes := scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes")
	root = summary("root")
	if root.DirectUnits != 1 || root.SubtreeUnits != 2 || root.DistinctItemCount != 3 ||
		!reflect.DeepEqual(root.DirectQuantities, []QuantityGroup{{BaseUnit: "g", QuantityBase: 400}}) ||
		!reflect.DeepEqual(root.SubtreeQuantities, []QuantityGroup{{BaseUnit: "g", QuantityBase: 1500}, {BaseUnit: "ml", QuantityBase: 1200}}) {
		t.Fatal("summary mixed base units, repeated a distinct item, or counted a disposed Unit", root)
	}
	if source := summary("source"); source.DirectUnits != 0 || source.SubtreeUnits != 0 || source.DistinctItemCount != 1 ||
		!reflect.DeepEqual(source.DirectQuantities, []QuantityGroup{{BaseUnit: "g", QuantityBase: 1100}}) {
		t.Fatal("direct source summary did not reflect the real transfer/disposal", source)
	}
	if target := summary("target"); target.DirectUnits != 1 || target.SubtreeUnits != 1 || target.DistinctItemCount != 2 ||
		!reflect.DeepEqual(target.DirectQuantities, []QuantityGroup{{BaseUnit: "ml", QuantityBase: 1200}}) {
		t.Fatal("target summary mixed retained zero stock or another branch", target)
	}
	if got := scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes"); got != changes {
		t.Fatal("read-side summary republished source or projections", changes, got)
	}
	encoded, err := json.Marshal(root)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(encoded, &fields) != nil || len(fields) != 6 || fields["revision"] != nil || fields["direct_quantities"] == nil || fields["subtree_quantities"] == nil {
		t.Fatal("summary exposed its internal proof or omitted required empty-capable arrays", string(encoded), err)
	}
	if _, err = inventory.SetUnitStatus(ctx, fixture.token, "summary-root-dispose", first.ID, first.Revision, UnitStatus{Status: "disposed", DisposalKind: testPointer("donated")}); err != nil {
		t.Fatal(err)
	}
	if changed := summary("root"); changed.DirectUnits != 0 || changed.SubtreeUnits != 1 || changed.Revision <= root.Revision {
		t.Fatal("a physical status change did not invalidate the summary's actual location revision", changed, root)
	}
}

func TestLocationSummaryRejectsUnsafeSourceAndDoesNotReturnPartialResults(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(*testing.T, holdingFixture)
	}{
		{"cyclic ancestry", func(t *testing.T, f holdingFixture) {
			execSQL(t, f.store.db, "UPDATE locations SET parent_id='source' WHERE id='root'")
		}},
		{"missing descendant authority", func(t *testing.T, f holdingFixture) {
			execSQL(t, f.store.db, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id='source'")
			execSQL(t, f.store.db, "DELETE FROM projection_revisions WHERE entity_type='location' AND entity_id='source'")
		}},
		{"missing disposed unit authority", func(t *testing.T, f holdingFixture) {
			execSQL(t, f.store.db, "UPDATE item_units SET status='disposed' WHERE id=?", f.unit.ID)
			execSQL(t, f.store.db, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", f.unit.ID)
			execSQL(t, f.store.db, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", f.unit.ID)
		}},
		{"missing zero stock authority", func(t *testing.T, f holdingFixture) {
			id := projectionPair(f.target.ItemID, f.target.LocationID)
			execSQL(t, f.store.db, "DELETE FROM current_projections WHERE entity_type='stock' AND entity_id=?", id)
			execSQL(t, f.store.db, "DELETE FROM projection_revisions WHERE entity_type='stock' AND entity_id=?", id)
		}},
		{"mismatched item tracking", func(t *testing.T, f holdingFixture) {
			execSQL(t, f.store.db, "UPDATE items SET tracking_mode='quantity' WHERE id=?", f.individual.ID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := holdingsFixture(t)
			locations, err := fixture.store.NewLocations(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			test.corrupt(t, fixture)
			value, err := locations.Summary(t.Context(), fixture.token, "root")
			if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(value, LocationSummary{}) {
				t.Fatal("invalid authoritative source returned a partial summary", value, err)
			}
		})
	}
}

func TestLocationSummarySupportsDeepSubtreesWithoutDoubleCounting(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(t.Context(), `WITH RECURSIVE chain(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM chain WHERE n<1100)
 INSERT INTO locations(id,parent_id,code,name,created_at,updated_at)
 SELECT 'deep-'||n,CASE WHEN n=1 THEN 'source' ELSE 'deep-'||(n-1) END,'DEEP-'||n,'Deep node','2026','2026' FROM chain`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions SELECT 'location',id,1 FROM locations WHERE id LIKE 'deep-%'"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'location',id,1,json_object('id',id,'parent_id',parent_id,'code',code,'name',name,'kind',kind,
 'status',status,'sort_order',sort_order,'description',description),updated_at FROM locations WHERE id LIKE 'deep-%'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	unit := UnitData{ID: "deep-unit", ItemID: fixture.individual.ID, LocationID: testPointer("deep-1100"), AssetCode: "AST-DEEP", Status: "stored"}
	execSQL(t, fixture.store.db, `INSERT INTO item_units(id,item_id,location_id,asset_code,status,created_at,updated_at) VALUES(?,?,?,?,?,'2026','2026')`, unit.ID, unit.ItemID, unit.LocationID, unit.AssetCode, unit.Status)
	seedCatalogProjection(t, fixture.store, "unit", unit.ID, unit)
	root, err := locations.Summary(t.Context(), fixture.token, "root")
	if err != nil || root.DirectUnits != 0 || root.SubtreeUnits != 2 || root.DistinctItemCount != 2 {
		t.Fatal("deep subtree was truncated or counted its item twice", root, err)
	}
	leaf, err := locations.Summary(t.Context(), fixture.token, "deep-1100")
	if err != nil || leaf.DirectUnits != 1 || leaf.SubtreeUnits != 1 || leaf.DistinctItemCount != 1 {
		t.Fatal("deep ancestor validation truncated the leaf", leaf, err)
	}
}

func TestLocationSummaryBoundsAndOrdersIndependentBaseUnitGroups(t *testing.T) {
	groups := make(map[string]int64)
	for index := maxLocationQuantityGroups - 1; index >= 0; index-- {
		if err := addLocationQuantity(groups, fmt.Sprintf("u%03d", index), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := addLocationQuantity(groups, "overflow", 1); !errors.Is(err, ErrDomainState) || len(groups) != maxLocationQuantityGroups {
		t.Fatal("group bound silently truncated or mutated the input", err)
	}
	ordered := orderedLocationQuantities(groups)
	if len(ordered) != maxLocationQuantityGroups || ordered[0].BaseUnit != "u000" || ordered[len(ordered)-1].BaseUnit != "u499" {
		t.Fatal("group order depends on map iteration", ordered)
	}
	groups["u000"] = maxSafeInteger
	for _, quantity := range []int64{-1, 0, 1} {
		if err := addLocationQuantity(groups, "u000", quantity); !errors.Is(err, ErrDomainState) || groups["u000"] != maxSafeInteger {
			t.Fatal("invalid or overflowing sum mutated the group", quantity, err)
		}
	}
	if empty := orderedLocationQuantities(nil); empty == nil || len(empty) != 0 {
		t.Fatal("empty groups must serialize as an array", empty)
	}
}

func TestLocationSummaryRejectsTooManyActualQuantityGroupsWithoutReturningAPrefix(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(t.Context(), `WITH RECURSIVE groups(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM groups WHERE n<501)
 INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,created_at,updated_at)
 SELECT 'group-item-'||n,'Fixture','quantity','u'||n,'u'||n,1,'2026','2026' FROM groups`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions SELECT 'item',id,1 FROM items WHERE id LIKE 'group-item-%'"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO stock_balances(item_id,location_id,quantity_base,updated_at) SELECT id,'root',1,'2026' FROM items WHERE id LIKE 'group-item-%'"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions SELECT 'stock',json_array(item_id,location_id),1 FROM stock_balances WHERE item_id LIKE 'group-item-%'"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	value, err := locations.Summary(t.Context(), fixture.token, "root")
	if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(value, LocationSummary{}) {
		t.Fatal("more than 500 actual units produced a partial or unbounded response", value, err)
	}
}

func TestLocationSummaryRechecksActivityAndCommitBeforeReturning(t *testing.T) {
	for _, test := range []struct {
		name, setup, trigger string
		want                 error
	}{
		{"ignored activity", "", `BEFORE UPDATE OF last_seen_at ON sessions BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"revoked during activity", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, ErrAuthentication},
		{"password change during activity", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE admin_accounts SET must_change_password=1; END`, ErrPasswordChangeRequired},
		{"epoch change during activity", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sync_state SET dataset_epoch='other'; END`, ErrAuthentication},
		{"deferred commit failure", `CREATE TABLE location_summary_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED)`, `AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO location_summary_guard VALUES('missing'); END`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := holdingsFixture(t)
			locations, err := fixture.store.NewLocations(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			if test.setup != "" {
				execSQL(t, fixture.store.db, test.setup)
			}
			execSQL(t, fixture.store.db, "CREATE TRIGGER location_summary_late "+test.trigger)
			defer execSQL(t, fixture.store.db, "DROP TRIGGER location_summary_late")
			before := domainState(t, fixture.store)
			value, err := locations.Summary(t.Context(), fixture.token, "root")
			if err == nil || test.want != nil && !errors.Is(err, test.want) || !reflect.DeepEqual(value, LocationSummary{}) {
				t.Fatal("activity or commit failure exposed a summary", value, err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestLocationSummaryReadsAfterSessionActivity(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, fixture.store.db, `CREATE TRIGGER location_summary_activity AFTER UPDATE OF last_seen_at ON sessions BEGIN
 UPDATE stock_balances SET quantity_base=1600 WHERE location_id='source'; END`)
	defer execSQL(t, fixture.store.db, "DROP TRIGGER location_summary_activity")
	value, err := locations.Summary(t.Context(), fixture.token, "root")
	if err != nil || !reflect.DeepEqual(value.SubtreeQuantities, []QuantityGroup{{BaseUnit: "g", QuantityBase: 1600}}) {
		t.Fatal("summary did not read the same post-activity transaction state", value, err)
	}
}

func TestLocationSummaryRejectsUnsafeGroupedTotalsAndRequiresCurrentSession(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := fixture.store.NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	other := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Large grams", TrackingMode: testPointer("quantity"), UnitCode: testPointer("g"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1))})
	if _, err = inventory.Receive(t.Context(), fixture.token, "summary-max-quantity", other.Revision, StockReceive{ItemID: other.ID, TargetLocationID: "target", QuantityBase: maxSafeInteger}); err != nil {
		t.Fatal(err)
	}
	if value, err := locations.Summary(t.Context(), fixture.token, "target"); err != nil || !reflect.DeepEqual(value.DirectQuantities, []QuantityGroup{{BaseUnit: "g", QuantityBase: maxSafeInteger}}) {
		t.Fatal("a representable exact maximum was rejected", value, err)
	}
	if value, err := locations.Summary(t.Context(), fixture.token, "root"); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(value, LocationSummary{}) {
		t.Fatal("a combined unsafe integer returned an overflowed or partial summary", value, err)
	}
	if _, err = locations.Summary(t.Context(), fixture.token, ""); !errors.Is(err, ErrDomainInput) {
		t.Fatal("empty identity was accepted", err)
	}
	if _, err = locations.Summary(t.Context(), fixture.token, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing identity was treated as an empty location", err)
	}
	if _, err = locations.Summary(nil, fixture.token, "target"); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil context was accepted", err)
	}
	var absent *Locations
	if _, err = absent.Summary(t.Context(), fixture.token, "target"); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil service was accepted", err)
	}
	execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
	if value, err := locations.Summary(t.Context(), fixture.token, "target"); !errors.Is(err, ErrPasswordChangeRequired) || !reflect.DeepEqual(value, LocationSummary{}) {
		t.Fatal("incomplete session received a summary", value, err)
	}
	execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=0")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = locations.Summary(cancelled, fixture.token, "target"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read was treated as an empty summary", err)
	}
	if _, err = fixture.auth.Logout(t.Context(), fixture.token, "summary-logout"); err != nil {
		t.Fatal(err)
	}
	if value, err := locations.Summary(t.Context(), fixture.token, "target"); !errors.Is(err, ErrAuthentication) || !reflect.DeepEqual(value, LocationSummary{}) {
		t.Fatal("revoked session received a summary", value, err)
	}
}
