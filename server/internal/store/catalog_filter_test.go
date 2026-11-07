package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func readAllCatalogPages(t *testing.T, catalog *Catalog, token string, filters ItemFilters, limit int) []Item {
	t.Helper()
	var values []Item
	seenIDs, seenCursors := make(map[string]bool), make(map[string]bool)
	cursor := ""
	for {
		page, err := catalog.Page(t.Context(), token, cursor, limit, filters)
		if err != nil || page.Data == nil || len(page.Data) > limit {
			t.Fatal("filtered catalog page failed", err)
		}
		for _, value := range page.Data {
			if seenIDs[value.ID] || len(values) > 0 && value.ID <= values[len(values)-1].ID {
				t.Fatal("filtered pages repeated or reordered a stable identity")
			}
			seenIDs[value.ID] = true
			values = append(values, value)
		}
		if page.NextCursor == nil {
			return values
		}
		if seenCursors[*page.NextCursor] || len(page.Data) != limit {
			t.Fatal("filtered page repeated a cursor or truncated a nonterminal page")
		}
		seenCursors[*page.NextCursor] = true
		cursor = *page.NextCursor
	}
}

func requireCatalogNames(t *testing.T, values []Item, want ...string) {
	t.Helper()
	got := make([]string, 0, len(values))
	for _, value := range values {
		got = append(got, value.Name)
	}
	sort.Strings(got)
	if want == nil {
		want = []string{}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered catalog names differ: got %v, want %v", got, want)
	}
}

func setCatalogTimezone(t *testing.T, s *Store, name string) {
	t.Helper()
	encoded, err := json.Marshal(name)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, `INSERT INTO app_settings(key,value_json,updated_at) VALUES('preferences.timezone',?,'2026')
 ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, string(encoded))
}

func seedCatalogPurchase(t *testing.T, s *Store, itemID, id, purchased, currency, merchant string) {
	t.Helper()
	seedReadPurchase(t, s, PurchaseData{ID: id, MerchantName: merchant, PurchasedAt: purchased, Currency: currency})
	seedReadPurchaseLine(t, s, PurchaseLineData{ID: id, PurchaseID: id, ItemID: itemID, QuantityBase: 1, PaidMinor: 100})
}

func seedUnmaterializedCatalogItem(t *testing.T, s *Store, id, name string) {
	t.Helper()
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,'individual','2026','2026')", id, name)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", id)
}

func TestCatalogFilteredPagesApplyHierarchyStatusAndCurrentHoldingsWithoutDuplicates(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	locations, err := NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, token, "CategoryRoot", nil)
	child := createCategory(t, taxonomy, token, "CategoryChild", &category.ID)
	grandchild := createCategory(t, taxonomy, token, "CategoryGrandchild", &child.ID)
	otherCategory := createCategory(t, taxonomy, token, "CategoryOther", nil)
	box := createLocation(t, locations, token, "BOX", nil)
	inside := createLocation(t, locations, token, "INSIDE", &box.ID)
	elsewhere := createLocation(t, locations, token, "ELSEWHERE", nil)
	for _, input := range []struct {
		name, mode, status, holding, location string
		category                              *string
	}{
		{"Direct", "individual", "active", "stored", box.ID, &category.ID},
		{"Nested", "quantity", "active", "positive", inside.ID, &grandchild.ID},
		{"Zero", "quantity", "active", "zero", inside.ID, &child.ID},
		{"Disposed", "individual", "active", "disposed", inside.ID, &child.ID},
		{"Retired", "individual", "active", "retired", inside.ID, &child.ID},
		{"Unlocated", "individual", "active", "stored", "", &category.ID},
		{"Archived", "individual", "archived", "stored", box.ID, &category.ID},
		{"Other", "individual", "active", "stored", elsewhere.ID, &otherCategory.ID},
	} {
		item := createCatalogItem(t, catalog, token, ItemCreate{Name: input.name, TrackingMode: &input.mode, Status: &input.status, CategoryID: input.category})
		if input.mode == "quantity" {
			quantity := 0
			if input.holding == "positive" {
				quantity = 1500
			}
			execSQL(t, s.db, "INSERT INTO stock_balances VALUES(?,?,?,'2026')", item.ID, input.location, quantity)
		} else {
			seedSearchUnit(t, s, input.name, item.ID, "ASSET-"+input.name, nil, "")
			var location *string
			if input.location != "" {
				location = &input.location
			}
			execSQL(t, s.db, "UPDATE item_units SET location_id=?,status=? WHERE id=?", location, input.holding, input.name)
			if input.name == "Direct" {
				seedSearchUnit(t, s, "another-direct-unit", item.ID, "ASSET-EXTRA", nil, "")
				execSQL(t, s.db, "UPDATE item_units SET location_id=? WHERE id='another-direct-unit'", inside.ID)
			}
		}
	}
	for _, test := range []struct {
		name    string
		filters ItemFilters
		want    []string
	}{
		{"all", ItemFilters{}, []string{"Direct", "Nested", "Zero", "Disposed", "Retired", "Unlocated", "Archived", "Other"}},
		{"active", ItemFilters{Status: "active"}, []string{"Direct", "Nested", "Zero", "Disposed", "Retired", "Unlocated", "Other"}},
		{"archived", ItemFilters{Status: "archived"}, []string{"Archived"}},
		{"category direct", ItemFilters{CategoryID: category.ID, Scope: "direct"}, []string{"Direct", "Unlocated", "Archived"}},
		{"category subtree", ItemFilters{CategoryID: category.ID}, []string{"Direct", "Nested", "Zero", "Disposed", "Retired", "Unlocated", "Archived"}},
		{"category child subtree", ItemFilters{CategoryID: child.ID}, []string{"Nested", "Zero", "Disposed", "Retired"}},
		{"location direct", ItemFilters{LocationID: box.ID, Scope: "direct"}, []string{"Direct", "Archived"}},
		{"location subtree", ItemFilters{LocationID: box.ID}, []string{"Direct", "Nested", "Retired", "Archived"}},
		{"location child direct", ItemFilters{LocationID: inside.ID, Scope: "direct"}, []string{"Direct", "Nested", "Retired"}},
		{"combined subtree", ItemFilters{CategoryID: child.ID, LocationID: box.ID}, []string{"Nested", "Retired"}},
		{"combined direct", ItemFilters{CategoryID: child.ID, LocationID: inside.ID, Scope: "direct"}, []string{"Retired"}},
		{"combined status", ItemFilters{LocationID: box.ID, Status: "active"}, []string{"Direct", "Nested", "Retired"}},
		{"unknown category", ItemFilters{CategoryID: "missing"}, nil},
		{"unknown location", ItemFilters{LocationID: "missing"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireCatalogNames(t, readAllCatalogPages(t, catalog, token, test.filters, 2), test.want...)
		})
	}
}

func TestCatalogPurchaseDatesUseInclusiveHomeCivilDaysAcrossDSTAndSkippedDates(t *testing.T) {
	for _, test := range []struct {
		zone, from, to string
		instants       []string
		want           []string
	}{
		{"UTC", "2026-11-05", "2026-11-05", []string{"2026-11-04T23:59:59.999999999Z", "2026-11-05T00:00:00Z", "2026-11-05T23:59:59.999999999Z", "2026-11-06T00:00:00Z"}, []string{"Purchase1", "Purchase2"}},
		{"Asia/Taipei", "2026-11-05", "2026-11-05", []string{"2026-11-04T15:59:59.999999999Z", "2026-11-04T16:00:00Z", "2026-11-05T23:59:59.999999999+08:00", "2026-11-05T16:00:00Z"}, []string{"Purchase1", "Purchase2"}},
		{"America/New_York", "2026-03-08", "2026-03-08", []string{"2026-03-08T04:59:59.999999999Z", "2026-03-08T05:00:00Z", "2026-03-09T03:59:59.999999999Z", "2026-03-09T04:00:00Z"}, []string{"Purchase1", "Purchase2"}},
		{"America/New_York", "2026-11-01", "2026-11-01", []string{"2026-11-01T03:59:59.999999999Z", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z", "2026-11-02T04:59:59.999999999Z", "2026-11-02T05:00:00Z"}, []string{"Purchase1", "Purchase2", "Purchase3"}},
		{"Pacific/Apia", "2011-12-30", "2011-12-30", []string{"2011-12-30T09:59:59.999999999Z", "2011-12-30T10:00:00Z"}, nil},
		{"Pacific/Apia", "2011-12-29", "2011-12-31", []string{"2011-12-30T09:59:59.999999999Z", "2011-12-30T10:00:00Z"}, []string{"Purchase0", "Purchase1"}},
	} {
		t.Run(test.zone+test.from+test.to, func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			if test.zone != "UTC" {
				setCatalogTimezone(t, s, test.zone)
			}
			for index, instant := range test.instants {
				name := fmt.Sprintf("Purchase%d", index)
				item := createCatalogItem(t, catalog, token, ItemCreate{Name: name})
				seedCatalogPurchase(t, s, item.ID, name, instant, "TWD", "Merchant")
			}
			requireCatalogNames(t, readAllCatalogPages(t, catalog, token, ItemFilters{From: test.from, To: test.to}, 1), test.want...)
		})
	}
}

func TestCatalogDateCurrencyAndExactMerchantMustMatchOneAssociatedPurchase(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Multiple"})
	seedCatalogPurchase(t, s, item.ID, "a", "2026-11-05T12:00:00Z", "TWD", "Alpha")
	seedCatalogPurchase(t, s, item.ID, "b", "2026-10-05T12:00:00Z", "USD", "Beta")
	seedCatalogPurchase(t, s, item.ID, "c", "2026-11-05T12:00:00Z", "USD", "Gamma")
	seedReadPurchaseLine(t, s, PurchaseLineData{ID: "another-c", PurchaseID: "c", ItemID: item.ID, QuantityBase: 1, PaidMinor: 100})
	other := createCatalogItem(t, catalog, token, ItemCreate{Name: "Other"})
	seedCatalogPurchase(t, s, other.ID, "d", "2026-11-05T12:00:00Z", "USD", "Beta")
	for _, test := range []struct {
		filters ItemFilters
		want    []string
	}{
		{ItemFilters{From: "2026-11-05", To: "2026-11-05", Currency: "USD", Merchant: "Beta"}, []string{"Other"}},
		{ItemFilters{From: "2026-11-05", To: "2026-11-05", Currency: "USD", Merchant: "Gamma"}, []string{"Multiple"}},
		{ItemFilters{Currency: "USD", Merchant: "Beta"}, []string{"Multiple", "Other"}},
		{ItemFilters{Currency: "TWD"}, []string{"Multiple"}},
		{ItemFilters{Merchant: "beta"}, nil}, {ItemFilters{Merchant: " Beta"}, nil},
		{ItemFilters{From: "2026-11-06"}, nil}, {ItemFilters{To: "2026-10-05"}, []string{"Multiple"}},
	} {
		requireCatalogNames(t, readAllCatalogPages(t, catalog, token, test.filters, 1), test.want...)
	}
}

func TestCatalogTextFilterUsesOnlyPublishedOrApprovedSourceAndNeverMutatesLegacyCaches(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "不鏽鋼濾芯 Ōmega"})
	execSQL(t, s.db, "INSERT INTO item_aliases VALUES('alias',?,'AliasIdentifier','2026')", item.ID)
	execSQL(t, s.db, "INSERT INTO item_barcodes VALUES('barcode',?,'CODE_128','000012','000012','2026')", item.ID)
	execSQL(t, s.db, "INSERT INTO item_attributes VALUES('attribute',?,'ManualKey','ManualValue','2026')", item.ID)
	seedUnmaterializedCatalogItem(t, s, "legacy", "ManualLegacyTitle 濾芯")
	execSQL(t, s.db, "INSERT INTO search_documents(item_id,title,reviewed_ai,revision,updated_at) VALUES('legacy','LegacyUnreviewedCache','LegacyRawCache',99,'2026')")
	for _, review := range []struct{ id, state, field, raw string }{
		{"accepted", "accepted", "keyword", "LegacyApproved"}, {"pending", "needs_review", "keyword", "LegacyCandidate"},
		{"rejected", "rejected", "keyword", "LegacyRejected"}, {"historical", "accepted", "title", "HistoricalTitle"},
	} {
		var reviewed *string
		if review.state != "needs_review" {
			reviewed = testPointer("2026-11-05T00:00:00Z")
		}
		execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,reviewed_at,created_at,updated_at)
 VALUES(?,'legacy',?,?,?,?, '2026','2026')`, review.id, review.field, review.raw, review.state, reviewed)
	}
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	publishCatalogSearch(t, taxonomy, token, "publish-text-filter-fields", item.ID)
	execSQL(t, s.db, "UPDATE search_documents SET title='PoisonedPrivateCache',revision=99 WHERE item_id=?", item.ID)
	before := domainState(t, s)
	for _, test := range []struct {
		query string
		want  []string
	}{
		{"濾", []string{item.Name, "ManualLegacyTitle 濾芯"}}, {"  ōMEGA  ", []string{item.Name}},
		{"aliasidentifier", []string{item.Name}}, {"000012", []string{item.Name}}, {"manualvalue", []string{item.Name}},
		{"LegacyApproved", []string{"ManualLegacyTitle 濾芯"}},
		{"PoisonedPrivateCache", nil}, {"LegacyUnreviewedCache", nil}, {"LegacyRawCache", nil}, {"LegacyCandidate", nil},
		{"LegacyRejected", nil}, {"HistoricalTitle", nil}, {"' OR 1=1 --", nil},
	} {
		requireCatalogNames(t, readAllCatalogPages(t, catalog, token, ItemFilters{Query: test.query}, 1), test.want...)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", projectionPair("item", "legacy")) != 0 {
		t.Fatal("read-side fallback silently published a legacy search snapshot")
	}
}

func TestCatalogFilteringFinishesAllCandidatesBeforeSlicingPages(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	for index := range 35 {
		name := "DoesNotMatch"
		if index >= 30 {
			name = fmt.Sprintf("Needle%d", index)
		}
		seedUnmaterializedCatalogItem(t, s, fmt.Sprintf("item-%03d", index), name)
	}
	values := readAllCatalogPages(t, catalog, token, ItemFilters{Query: "needle"}, 2)
	if len(values) != 5 || values[0].ID != "item-030" || values[4].ID != "item-034" {
		t.Fatal("filtering only a raw first page fabricated incomplete/empty results")
	}
}

func TestCatalogCivilDateFilterRejectsUnknownLegacyInstantsWithoutPartialPages(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	seedUnmaterializedCatalogItem(t, s, "a", "Valid")
	seedUnmaterializedCatalogItem(t, s, "b", "Unknown")
	seedCatalogPurchase(t, s, "a", "valid-purchase", "2026-11-05T12:00:00Z", "TWD", "Merchant")
	seedCatalogPurchase(t, s, "b", "unknown-purchase", "2026-11-05", "TWD", "Merchant")
	for _, invalid := range []string{"2026-11-05", "2026-11-05T1:00:00Z", "2026-11-05T12:00:00,1Z", "2026-11-05T12:00:00+24:00", "2026-11-05T12:00:00+00:60", "0000-11-05T12:00:00Z"} {
		execSQL(t, s.db, "UPDATE purchases SET purchased_at=? WHERE id='unknown-purchase'", invalid)
		before := domainState(t, s)
		if page, err := catalog.Page(t.Context(), token, "", 2, ItemFilters{From: "2026-11-05", To: "2026-11-05"}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
			t.Errorf("unsupported legacy instant %q was guessed or returned a valid prefix: %v", invalid, err)
		}
		requireDomainState(t, s, before)
	}
}

func TestCatalogFilteredReadScansCompleteLegacyDatasetsWithoutPublishingOrTruncating(t *testing.T) {
	for _, size := range []int{500, 2500, 5000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			items, err := tx.PrepareContext(t.Context(), "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,'individual','2026','2026')")
			if err != nil {
				t.Fatal(err)
			}
			defer items.Close()
			counters, err := tx.PrepareContext(t.Context(), "INSERT INTO projection_revisions VALUES('item',?,1)")
			if err != nil {
				t.Fatal(err)
			}
			defer counters.Close()
			for index := range size {
				name := "DoesNotMatch"
				if index == size-1 {
					name = "LastNeedle"
				}
				id := fmt.Sprintf("item-%05d", index)
				if _, err := items.ExecContext(t.Context(), id, name); err != nil {
					t.Fatal(err)
				}
				if _, err := counters.ExecContext(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			values := readAllCatalogPages(t, catalog, token, ItemFilters{Query: "needle"}, 50)
			t.Logf("%d legacy candidates, approved-source fallback, one matching item: %s", size, time.Since(started))
			if len(values) != 1 || values[0].ID != fmt.Sprintf("item-%05d", size-1) ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM search_documents") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 0 {
				t.Fatal("a complete filtered read was truncated or mutated authoritative publication")
			}
		})
	}
}

func TestCatalogFilteredPagesRejectInvalidQueriesAndCorruptTimezoneWithoutPrefixes(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	createCatalogItem(t, catalog, token, ItemCreate{Name: "Visible"})
	before := domainState(t, s)
	for _, filters := range []ItemFilters{
		{From: "2026-02-29"}, {From: "0000-01-01"}, {To: "2026-1-01"}, {From: "2026-11-06", To: "2026-11-05"},
		{Currency: "usd"}, {Currency: "US"}, {Currency: "USDX"}, {Currency: "U😀"}, {Scope: "all"}, {Status: "disposed"},
		{CategoryID: strings.Repeat("x", 129)}, {LocationID: string([]byte{0xff})}, {Merchant: strings.Repeat("x", 301)},
		{Query: strings.Repeat("x", 501)}, {Query: string([]byte{0xff})},
	} {
		if page, err := catalog.Page(t.Context(), token, "", 50, filters); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid filtered query exposed a page/continuation", err)
		}
	}
	for _, limit := range []int{0, 101} {
		if _, err := catalog.Page(t.Context(), token, "", limit, ItemFilters{}); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid filtered page size was admitted", err)
		}
	}
	if _, err := catalog.Page(t.Context(), token, strings.Repeat("x", 2049), 50, ItemFilters{}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("oversized filtered cursor was admitted", err)
	}
	requireDomainState(t, s, before)
	for _, encoded := range []string{`null`, `true`, `10`, `{}`, `[]`, `""`, `"Local"`, `"Unknown/Timezone"`, `"../private"`, `"UTC\n"`} {
		execSQL(t, s.db, `INSERT INTO app_settings(key,value_json,updated_at) VALUES('preferences.timezone',?,'2026')
 ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, encoded)
		if page, err := catalog.Page(t.Context(), token, "", 1, ItemFilters{}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("corrupt timezone was silently replaced with host-local/default time", err)
		}
	}
}
