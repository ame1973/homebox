package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLocationContentsPagesActualDirectAndSubtreeRecords(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := fixture.store.NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	grandchild := createLocation(t, locations, fixture.token, "CONTENTS-NESTED", testPointer("source"))
	additional, err := inventory.CreateUnit(t.Context(), fixture.token, "contents-nested-unit", fixture.individual.ID, fixture.individual.Revision, UnitCreate{LocationID: &grandchild.ID})
	if err != nil {
		t.Fatal(err)
	}
	changes := scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes")
	read := func(id, scope string) (units, stock, children []string) {
		t.Helper()
		cursor, seen := "", map[string]bool{}
		for {
			page, err := locations.PageContents(t.Context(), fixture.token, id, scope, cursor, 1)
			if err != nil {
				t.Fatal(err)
			}
			if page.LocationID != id || page.Scope != scope || page.Revision != getLocation(t, locations, fixture.token, id).Revision ||
				page.Units == nil || page.Stock == nil || page.SubLocations == nil || len(page.Units)+len(page.Stock)+len(page.SubLocations) != 1 {
				t.Fatal("page lost its exact scope, revision, required arrays, or row bound", page)
			}
			for _, unit := range page.Units {
				units = append(units, unit.ID)
			}
			for _, balance := range page.Stock {
				stock = append(stock, balance.LocationID)
			}
			for _, child := range page.SubLocations {
				children = append(children, child.ID)
			}
			body, err := json.Marshal(page)
			var fields map[string]json.RawMessage
			if err != nil || json.Unmarshal(body, &fields) != nil || len(fields) != 6 || fields["revision"] != nil || fields["next_cursor"] == nil || strings.Contains(string(body), fixture.token) {
				t.Fatal("page exposed its internal proof/credentials or omitted the cursor", string(body), err)
			}
			if page.NextCursor == nil {
				break
			}
			if seen[*page.NextCursor] {
				t.Fatal("page repeated its continuation")
			}
			seen[*page.NextCursor], cursor = true, *page.NextCursor
		}
		return
	}
	units, stock, children := read("root", "direct")
	if len(units) != 0 || len(stock) != 0 || !reflect.DeepEqual(children, []string{"source", "target"}) {
		t.Fatal("direct contents included indirect holdings or lost children", units, stock, children)
	}
	units, stock, children = read("root", "subtree")
	wantUnits, wantChildren := []string{"unit", additional.ID}, []string{"source", "target", grandchild.ID}
	sort.Strings(wantUnits)
	sort.Strings(wantChildren)
	if !reflect.DeepEqual(units, wantUnits) || !reflect.DeepEqual(stock, []string{"source", "target"}) || !reflect.DeepEqual(children, wantChildren) {
		t.Fatal("subtree pages lost a row, descendant or retained zero balance", units, stock, children)
	}
	units, stock, children = read("target", "direct")
	if len(units) != 0 || !reflect.DeepEqual(stock, []string{"target"}) || len(children) != 0 {
		t.Fatal("a retained zero balance was omitted or invented additional records", units, stock, children)
	}
	if got := scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes"); got != changes {
		t.Fatal("a contents read published projections", changes, got)
	}
}

func TestLocationContentsCompletesLargeMixedPagesWithoutRepeatingRows(t *testing.T) {
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
	for _, query := range []string{
		`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<205)
 INSERT INTO locations(id,parent_id,code,name,created_at,updated_at)
 SELECT printf('bulk-location-%03d',x),'root',printf('BULK-%03d',x),'Bulk location','2026','2026' FROM n`,
		`INSERT INTO projection_revisions SELECT 'location',id,1 FROM locations WHERE id LIKE 'bulk-location-%'`,
		`INSERT INTO item_units(id,item_id,location_id,asset_code,status,created_at,updated_at)
 SELECT replace(id,'location','unit'),?,id,replace(code,'BULK','AST'),CASE WHEN sort_order=0 THEN 'disposed' ELSE 'stored' END,'2026','2026'
 FROM locations WHERE id LIKE 'bulk-location-%'`,
		`INSERT INTO projection_revisions SELECT 'unit',id,1 FROM item_units WHERE id LIKE 'bulk-unit-%'`,
		`INSERT INTO stock_balances(item_id,location_id,quantity_base,updated_at)
 SELECT ?,id,0,'2026' FROM locations WHERE id LIKE 'bulk-location-%'`,
		`INSERT INTO projection_revisions SELECT 'stock',json_array(item_id,location_id),1 FROM stock_balances WHERE location_id LIKE 'bulk-location-%'`,
	} {
		var args []any
		if strings.Contains(query, "SELECT replace(id,'location','unit'),?") {
			args = []any{fixture.individual.ID}
		} else if strings.Contains(query, "SELECT ?,id,0") {
			args = []any{fixture.quantity.ID}
		}
		if _, err = tx.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cursor, pages := "", 0
	seenUnits, seenStock, seenLocations := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for {
		page, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Units)+len(page.Stock)+len(page.SubLocations) > 100 {
			t.Fatal("a mixed page exceeded its shared row bound", page)
		}
		for _, unit := range page.Units {
			if seenUnits[unit.ID] {
				t.Fatal("a Unit was repeated", unit.ID)
			}
			if strings.HasPrefix(unit.ID, "bulk-unit-") && unit.Status != "disposed" {
				t.Fatal("retained disposed Unit state was changed", unit)
			}
			seenUnits[unit.ID] = true
		}
		for _, stock := range page.Stock {
			key := projectionPair(stock.ItemID, stock.LocationID)
			if seenStock[key] {
				t.Fatal("a Stock identity was repeated", key)
			}
			if strings.HasPrefix(stock.LocationID, "bulk-location-") && stock.QuantityBase != 0 {
				t.Fatal("retained zero Stock was changed", stock)
			}
			seenStock[key] = true
		}
		for _, location := range page.SubLocations {
			if seenLocations[location.ID] {
				t.Fatal("a descendant was repeated", location.ID)
			}
			seenLocations[location.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == cursor || pages > 7 {
			t.Fatal("pagination failed to advance")
		}
		cursor = *page.NextCursor
	}
	if pages != 7 || len(seenUnits) != 206 || len(seenStock) != 207 || len(seenLocations) != 207 {
		t.Fatal("mixed pages truncated a retained Unit, Stock or location", pages, len(seenUnits), len(seenStock), len(seenLocations))
	}
}

func TestLocationContentsPreservesMaximumExactLegacyCursorIdentities(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	root := "root"
	locationIDs := []string{strings.Repeat("\x00", 128), strings.Repeat("\x01", 128)}
	itemIDs := []string{strings.Repeat("\x00", 128), strings.Repeat("\x01", 128)}
	for index, id := range locationIDs {
		location := LocationData{ID: id, ParentID: &root, Code: "LEGACY-" + string(rune('A'+index)), Name: "Legacy location", Kind: "other", Status: "active"}
		execSQL(t, fixture.store.db, "INSERT INTO locations(id,parent_id,code,name,created_at,updated_at) VALUES(?,?,?,?,'2026','2026')", id, root, location.Code, location.Name)
		seedCatalogProjection(t, fixture.store, "location", id, location)
	}
	for _, id := range itemIDs {
		item := ItemData{ID: id, Name: "Legacy quantity", TrackingMode: "quantity", UnitCode: "pcs", BaseUnit: "pcs", ScaleFactor: 1, Status: "active"}
		execSQL(t, fixture.store.db, `INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,created_at,updated_at)
 VALUES(?,?,?,'pcs','pcs',1,'2026','2026')`, id, item.Name, item.TrackingMode)
		seedCatalogProjection(t, fixture.store, "item", id, item)
		stock := StockData{ItemID: id, LocationID: locationIDs[0], QuantityBase: 1, UnitCode: "pcs", BaseUnit: "pcs", ScaleFactor: 1}
		execSQL(t, fixture.store.db, "INSERT INTO stock_balances VALUES(?,?,1,'2026')", id, locationIDs[0])
		seedCatalogProjection(t, fixture.store, "stock", projectionPair(id, locationIDs[0]), stock)
	}
	for index, id := range itemIDs {
		unit := UnitData{ID: id, ItemID: fixture.individual.ID, LocationID: &locationIDs[0], AssetCode: "AST-LEGACY-" + string(rune('A'+index)), Status: "disposed"}
		execSQL(t, fixture.store.db, `INSERT INTO item_units(id,item_id,location_id,asset_code,status,created_at,updated_at)
 VALUES(?,?,?,?,?,'2026','2026')`, id, unit.ItemID, unit.LocationID, unit.AssetCode, unit.Status)
		seedCatalogProjection(t, fixture.store, "unit", id, unit)
	}
	cursor, sawUnit, sawStock, sawLocation := "", false, false, false
	for {
		page, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Units) == 1 && page.Units[0].ID == itemIDs[0] {
			if page.NextCursor == nil || len(*page.NextCursor) > locationContentsCursorBytes {
				t.Fatal("maximum Unit identity could not produce a bounded continuation", page)
			}
			next, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", *page.NextCursor, 1)
			if err != nil || len(next.Units) != 1 || next.Units[0].ID != itemIDs[1] {
				t.Fatal("Unit continuation did not preserve exact identity order", next, err)
			}
			sawUnit = true
		}
		if len(page.Stock) == 1 && page.Stock[0].ItemID == itemIDs[0] {
			if page.Stock[0].LocationID != locationIDs[0] || page.NextCursor == nil || len(*page.NextCursor) > locationContentsCursorBytes {
				t.Fatal("maximum stock tuple was changed or could not produce a bounded continuation", page)
			}
			next, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", *page.NextCursor, 1)
			if err != nil || len(next.Stock) != 1 || next.Stock[0].ItemID != itemIDs[1] {
				t.Fatal("stock continuation did not preserve exact tuple order", next, err)
			}
			sawStock = true
		}
		if len(page.SubLocations) == 1 && page.SubLocations[0].ID == locationIDs[0] {
			if page.NextCursor == nil {
				t.Fatal("maximum location identity had no continuation")
			}
			next, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", *page.NextCursor, 1)
			if err != nil || len(next.SubLocations) != 1 || next.SubLocations[0].ID != locationIDs[1] {
				t.Fatal("location continuation did not preserve exact identity order", next, err)
			}
			sawLocation = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if !sawUnit || !sawStock || !sawLocation {
		t.Fatal("the maximum legacy identities were omitted", sawUnit, sawStock, sawLocation)
	}
}

func TestLocationContentsCursorBindsScopeSizeAndAggregateRevision(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	first, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", "", 1)
	if err != nil || first.NextCursor == nil {
		t.Fatal("first contents page failed", first, err)
	}
	for _, test := range []struct {
		id, scope, cursor string
		limit             int
	}{
		{"root", "direct", *first.NextCursor, 1},
		{"source", "subtree", *first.NextCursor, 1},
		{"root", "subtree", *first.NextCursor, 2},
		{"root", "subtree", *first.NextCursor + "x", 1},
	} {
		if value, err := locations.PageContents(t.Context(), fixture.token, test.id, test.scope, test.cursor, test.limit); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(value, LocationContents{}) {
			t.Fatal("a changed or tampered continuation returned a page", test, value, err)
		}
	}
	if _, err = fixture.auth.Logout(t.Context(), fixture.token, "contents-logout"); err != nil {
		t.Fatal(err)
	}
	if _, err = locations.PageContents(t.Context(), fixture.token, "root", "subtree", *first.NextCursor, 1); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a continuation authorized a revoked session", err)
	}
	_, fresh := login(t, fixture.auth)
	if _, err = locations.PageContents(t.Context(), fresh, "root", "subtree", *first.NextCursor, 1); err != nil {
		t.Fatal("a fresh session for the same principal could not continue", err)
	}
	createLocation(t, locations, fresh, "CONTENTS-NEW", testPointer("root"))
	if value, err := locations.PageContents(t.Context(), fresh, "root", "subtree", *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(value, LocationContents{}) {
		t.Fatal("a continuation crossed a changed Location aggregate revision", value, err)
	}
}

func TestLocationContentsCursorExpiryEpochAndPasswordGate(t *testing.T) {
	for _, state := range []string{"expired", "epoch", "mandatory"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			fixture := holdingsFixture(t)
			locations, err := fixture.store.NewLocations(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			first, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", "", 1)
			if err != nil || first.NextCursor == nil {
				t.Fatal("first contents page failed", first, err)
			}
			want, token := ErrInvalidCursor, fixture.token
			switch state {
			case "expired":
				expired := fixture.auth.now().Add(metadataCursorLifetime)
				fixture.auth.now = func() time.Time { return expired }
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='restored-epoch'")
				if _, err = locations.PageContents(t.Context(), token, "root", "subtree", *first.NextCursor, 1); !errors.Is(err, ErrAuthentication) {
					t.Fatal("old-epoch session remained valid", err)
				}
				func() {
					completedFixtureAuthSetup.Lock()
					defer completedFixtureAuthSetup.Unlock()
					_, token = login(t, fixture.auth)
				}()
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			}
			value, err := locations.PageContents(t.Context(), token, "root", "subtree", *first.NextCursor, 1)
			if !errors.Is(err, want) || !reflect.DeepEqual(value, LocationContents{}) {
				t.Fatal("expired, wrong-epoch or restricted session returned contents", value, err)
			}
		})
	}
}

func TestLocationContentsRejectsSignedNoncanonicalPositions(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	session, err := fixture.auth.Authenticate(t.Context(), fixture.token)
	if err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, fixture.store.db, "SELECT dataset_epoch FROM sync_state")
	root := getLocation(t, locations, fixture.token, "root")
	purpose := "location-contents-cursor-v1\x00" + projectionPair("root", "subtree") + ":" + strconv.FormatInt(root.Revision, 10)
	now := fixture.auth.now().UTC()
	for _, position := range []string{
		`{}`, `{"kind":"unit","id":""}`, `{"kind":"stock","id":"item"}`,
		`{"kind":"unit","id":"unit","location_id":"location"}`, `{"kind":"location","id":"id","extra":1}`,
		`{"kind":"unit","id":"id","id":"id"}`, `{"id":"id","kind":"unit"}`,
		`{"kind":"unit","id":"id","location_id":""}`, `{"kind":"unit","id":"\ud800"}`,
	} {
		cursor, err := encodeMetadataCursor(fixture.auth, purpose, metadataCursor{AdminID: session.Admin.ID, Epoch: epoch, AfterID: position,
			Limit: 1, Issued: now.Unix(), Expires: now.Add(metadataCursorLifetime).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		if value, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", cursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(value, LocationContents{}) {
			t.Fatal("a signed noncanonical position returned a page", position, value, err)
		}
	}
}

func TestLocationContentsRejectsCorruptScopeAndLookaheadAuthority(t *testing.T) {
	for _, test := range []struct {
		name, scope string
		corrupt     func(*testing.T, holdingFixture, *Locations) string
	}{
		{"cyclic ancestry", "direct", func(t *testing.T, f holdingFixture, _ *Locations) string {
			execSQL(t, f.store.db, "UPDATE locations SET parent_id='source' WHERE id='root'")
			return ""
		}},
		{"missing subtree authority", "subtree", func(t *testing.T, f holdingFixture, _ *Locations) string {
			execSQL(t, f.store.db, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id='target'")
			execSQL(t, f.store.db, "DELETE FROM projection_revisions WHERE entity_type='location' AND entity_id='target'")
			return ""
		}},
		{"missing direct-location lookahead", "direct", func(t *testing.T, f holdingFixture, _ *Locations) string {
			execSQL(t, f.store.db, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id='target'")
			execSQL(t, f.store.db, "DELETE FROM projection_revisions WHERE entity_type='location' AND entity_id='target'")
			return ""
		}},
		{"missing zero-stock lookahead", "subtree", func(t *testing.T, f holdingFixture, locations *Locations) string {
			first, err := locations.PageContents(t.Context(), f.token, "root", "subtree", "", 1)
			if err != nil || first.NextCursor == nil {
				t.Fatal("could not reach Stock phase", first, err)
			}
			id := projectionPair(f.target.ItemID, f.target.LocationID)
			execSQL(t, f.store.db, "DELETE FROM current_projections WHERE entity_type='stock' AND entity_id=?", id)
			execSQL(t, f.store.db, "DELETE FROM projection_revisions WHERE entity_type='stock' AND entity_id=?", id)
			return *first.NextCursor
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := holdingsFixture(t)
			locations, err := fixture.store.NewLocations(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			cursor := test.corrupt(t, fixture, locations)
			value, err := locations.PageContents(t.Context(), fixture.token, "root", test.scope, cursor, 1)
			if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(value, LocationContents{}) {
				t.Fatal("invalid source or lookahead returned a prefix", value, err)
			}
		})
	}
}

func TestLocationContentsRechecksSessionActivityAndCommitWithoutReturningAPrefix(t *testing.T) {
	for _, test := range []struct {
		name, setup, trigger string
		want                 error
	}{
		{"ignored activity", "", `BEFORE UPDATE OF last_seen_at ON sessions BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"revoked during activity", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, ErrAuthentication},
		{"password change during activity", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE admin_accounts SET must_change_password=1; END`, ErrPasswordChangeRequired},
		{"epoch change during activity", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sync_state SET dataset_epoch='other'; END`, ErrAuthentication},
		{"deferred commit failure", `CREATE TABLE location_contents_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED)`, `AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO location_contents_guard VALUES('missing'); END`, nil},
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
			execSQL(t, fixture.store.db, "CREATE TRIGGER location_contents_late "+test.trigger)
			defer execSQL(t, fixture.store.db, "DROP TRIGGER location_contents_late")
			before := domainState(t, fixture.store)
			value, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", "", 2)
			if err == nil || test.want != nil && !errors.Is(err, test.want) || !reflect.DeepEqual(value, LocationContents{}) {
				t.Fatal("activity or commit failure returned a contents prefix", value, err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestLocationContentsReadsActualSourceAfterSessionActivity(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, fixture.store.db, `CREATE TRIGGER location_contents_activity AFTER UPDATE OF last_seen_at ON sessions BEGIN
 UPDATE stock_balances SET quantity_base=1600 WHERE location_id='source'; END`)
	defer execSQL(t, fixture.store.db, "DROP TRIGGER location_contents_activity")
	value, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", "", 100)
	if err != nil || len(value.Stock) != 2 || value.Stock[0].LocationID != "source" || value.Stock[0].QuantityBase != 1600 {
		t.Fatal("contents did not read the post-activity transaction state", value, err)
	}
}

func TestLocationContentsRejectsInvalidLookaheadAndInputWithoutPrefix(t *testing.T) {
	fixture := holdingsFixture(t)
	locations, err := fixture.store.NewLocations(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, fixture.store.db, `INSERT INTO item_units(id,item_id,location_id,asset_code,status,created_at,updated_at)
 VALUES('zz-invalid',?,'source','AST-CONTENTS-INVALID','disposed','2026','2026')`, fixture.individual.ID)
	if value, err := locations.PageContents(t.Context(), fixture.token, "root", "subtree", "", 1); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(value, LocationContents{}) {
		t.Fatal("an invalid lookahead yielded a partial contents page", value, err)
	}
	for _, test := range []struct {
		id, scope, cursor string
		limit             int
	}{
		{"", "direct", "", 1}, {"root", "", "", 1}, {"root", "unknown", "", 1},
		{"root", "direct", "", 0}, {"root", "direct", "", 101}, {"root", "direct", strings.Repeat("x", 4097), 1},
	} {
		if _, err := locations.PageContents(t.Context(), fixture.token, test.id, test.scope, test.cursor, test.limit); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid contents input was accepted", test, err)
		}
	}
	if _, err := locations.PageContents(nil, fixture.token, "root", "direct", "", 1); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil context was accepted", err)
	}
	var absent *Locations
	if _, err := absent.PageContents(t.Context(), fixture.token, "root", "direct", "", 1); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil service was accepted", err)
	}
	if _, err := locations.PageContents(t.Context(), fixture.token, "missing", "direct", "", 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("a missing Location was treated as an empty page", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := locations.PageContents(ctx, fixture.token, "root", "direct", "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled context was accepted", err)
	}
}
