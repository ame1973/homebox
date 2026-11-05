package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func catalogFixture(t *testing.T) (*Store, *Catalog, *Auth, string) {
	t.Helper()
	s, auth, _, _, token := completedAuthFixture(t)
	catalog, err := NewCatalog(auth)
	if err != nil {
		t.Fatal(err)
	}
	return s, catalog, auth, token
}

func createCatalogItem(t *testing.T, catalog *Catalog, token string, input ItemCreate) Item {
	t.Helper()
	key, err := newDomainID()
	if err != nil {
		t.Fatal(err)
	}
	value, err := catalog.Create(t.Context(), token, "test-create-item-"+key, input)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func getCatalogItem(t *testing.T, catalog *Catalog, token, id string) Item {
	t.Helper()
	value, err := catalog.Get(t.Context(), token, id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCatalogCreationPublishesExactDefaultsWithoutImplicitHoldingsOrWorker(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	var item Item
	changed := requireProjectionReplay(t, s, func() {
		item = createCatalogItem(t, catalog, token, ItemCreate{Name: "  ManualCatalogTitle 😀  "})
	})
	want := ItemData{ID: item.ID, Name: "  ManualCatalogTitle 😀  ", TrackingMode: "individual", UnitCode: "pcs", BaseUnit: "pcs", ScaleFactor: 1, Status: "active"}
	if item.Revision != 1 || !reflect.DeepEqual(want, item.ItemData) || !reflect.DeepEqual(item, getCatalogItem(t, catalog, token, item.ID)) ||
		!regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(item.ID) ||
		len(changed) != 2 || !changed[projectionKey{"item", item.ID}] || !changed[projectionKey{"search_doc", projectionPair("item", item.ID)}] {
		t.Fatal("creation changed defaults, identity, or the atomic projection scope")
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var resource, payload map[string]any
	if json.Unmarshal(encoded, &resource) != nil || len(resource) != 13 || resource["brand"] != nil || resource["model"] != nil || resource["category_id"] != nil {
		t.Fatal("item response omitted nullable fields or exposed private fields")
	}
	frozen := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='item'")
	if json.Unmarshal([]byte(frozen), &payload) != nil || len(payload) != 12 || payload["revision"] != nil || payload["name"] != item.Name {
		t.Fatal("item publication changed the version-one offline payload")
	}
	for _, table := range []string{"item_units", "stock_balances", "purchases", "purchase_lines", "inventory_events", "media", "ai_jobs", "ai_review_entries"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table) != 0 {
			t.Fatal("catalog creation implicitly created holdings, finances, media or AI work", table)
		}
	}
	if approvedText(t, s, item.ID) != item.Name || scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'ManualCatalogTitle'") != 1 ||
		scalar[string](t, s.db, "SELECT response_json FROM api_idempotency WHERE operation='post_items'") != string(encoded) ||
		scalar[string](t, s.db, "SELECT result_json FROM mutation_events WHERE operation='post_items'") != string(encoded) {
		t.Fatal("creation did not freeze the approved text and original response together")
	}
}

func TestCatalogMeasurementDefaultsAreExplicitAndNeverRestrictedToIndividualPieces(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	for _, input := range []ItemCreate{
		{Name: "CanonicalSubset", UnitCode: testPointer("pcs")},
		{Name: "CanonicalScale", ScaleFactor: testPointer(int64(1))},
		{Name: "Coffee", TrackingMode: testPointer("quantity"), UnitCode: testPointer("kg"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000)), Brand: testPointer(""), Model: testPointer("Model")},
		{Name: "IndividualDisplay", UnitCode: testPointer("台"), BaseUnit: testPointer("pcs"), ScaleFactor: testPointer(int64(1)), Status: testPointer("archived")},
		{Name: "MaximumScale", TrackingMode: testPointer("quantity"), UnitCode: testPointer("custom"), BaseUnit: testPointer("custom/32"), ScaleFactor: testPointer(maxSafeInteger)},
	} {
		item := createCatalogItem(t, catalog, token, input)
		if item.Revision != 1 || input.ScaleFactor != nil && item.ScaleFactor != *input.ScaleFactor || input.UnitCode != nil && item.UnitCode != *input.UnitCode ||
			input.TrackingMode != nil && item.TrackingMode != *input.TrackingMode {
			t.Fatal("explicit metadata conversion was guessed or silently rewritten")
		}
	}
	before := domainState(t, s)
	for index, input := range []ItemCreate{
		{Name: "MissingBase", UnitCode: testPointer("kg")},
		{Name: "MissingDisplay", BaseUnit: testPointer("g")},
		{Name: "MissingUnits", ScaleFactor: testPointer(int64(1000))},
		{Name: "MissingScale", UnitCode: testPointer("kg"), BaseUnit: testPointer("g")},
		{Name: "MissingBase", UnitCode: testPointer("kg"), ScaleFactor: testPointer(int64(1000))},
		{Name: "MissingDisplay", BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000))},
	} {
		if value, err := catalog.Create(t.Context(), token, fmt.Sprintf("incomplete-measurement-%d", index), input); !errors.Is(err, ErrDomainInput) || value.ID != "" {
			t.Fatal("an incomplete custom tuple acquired an inferred conversion", err)
		}
	}
	requireDomainState(t, s, before)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Unused"})
	item, err := catalog.Patch(t.Context(), token, "explicit-unused-conversion", item.ID, 1, ItemPatch{
		TrackingMode: testPointer("quantity"), UnitCode: testPointer("kg"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000)),
	})
	if err != nil || item.Revision != 2 || item.TrackingMode != "quantity" || item.UnitCode != "kg" || item.BaseUnit != "g" || item.ScaleFactor != 1000 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM stock_balances") != 0 {
		t.Fatal("an unused item's explicit metadata conversion created or rescaled stock", err)
	}
}

func TestCatalogPartialNullablePatchesAndNoopReceiptsNeverRebase(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Original", Brand: testPointer("OriginalBrand"), Model: testPointer(""), Description: "Description", Notes: "Note"})
	first, err := catalog.Patch(t.Context(), token, "item-name-only", item.ID, 1, ItemPatch{Name: testPointer("Renamed")})
	if err != nil || first.Revision != 2 || first.Brand == nil || *first.Brand != "OriginalBrand" || first.Model == nil || *first.Model != "" || first.Description != "Description" || first.Notes != "Note" {
		t.Fatal("partial metadata edit overwrote omitted fields", err)
	}
	second, err := catalog.Patch(t.Context(), token, "item-explicit-null", item.ID, 2, ItemPatch{
		Brand: NullableString{Set: true}, Model: NullableString{Set: true, Value: testPointer("Model")}, Notes: testPointer(""), CategoryID: NullableID{Set: true},
	})
	if err != nil || second.Revision != 3 || second.Brand != nil || second.Model == nil || *second.Model != "Model" || second.Notes != "" || second.Name != "Renamed" || second.Description != "Description" {
		t.Fatal("nullable presence was conflated with omission or empty text", err)
	}
	noopInput := ItemPatch{Brand: NullableString{Set: true}, Model: NullableString{Set: true, Value: testPointer("Model")}}
	before := domainState(t, s)
	var noop Item
	changed := requireProjectionReplay(t, s, func() {
		noop, err = catalog.Patch(t.Context(), token, "item-value-noop", item.ID, 3, noopInput)
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 0 || !reflect.DeepEqual(noop, second) || domainState(t, s)["items"] != before["items"] ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='patch_items_id'") != 3 {
		t.Fatal("a value-equal nullable pointer invented source/projection changes or omitted its receipt")
	}
	latest, err := catalog.Patch(t.Context(), token, "later-item-edit", item.ID, 3, ItemPatch{Name: testPointer("Current")})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := catalog.Patch(t.Context(), token, "item-value-noop", item.ID, 3, noopInput)
	if err != nil || !reflect.DeepEqual(noop, frozen) || !reflect.DeepEqual(latest, getCatalogItem(t, catalog, token, item.ID)) {
		t.Fatal("a frozen noop reply was rebased or installed as current state", err)
	}
	before = domainState(t, s)
	if value, err := catalog.Patch(t.Context(), token, "novel-stale-item-key", item.ID, 3, noopInput); !errors.Is(err, ErrRevisionConflict) || value.ID != "" {
		t.Fatal("a new key silently adopted a current proof", err)
	}
	if _, err := catalog.Patch(t.Context(), token, "item-value-noop", item.ID, 3, ItemPatch{Model: noopInput.Model}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("omitted and explicitly cleared nullable fields shared a changed request", err)
	}
	requireDomainState(t, s, before)
}

func TestCatalogClassificationInvalidatesBothAncestorAggregatesOnlyOnce(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	root := createCategory(t, taxonomy, token, "Root", nil)
	left := createCategory(t, taxonomy, token, "Left", &root.ID)
	right := createCategory(t, taxonomy, token, "Right", &root.ID)
	unrelated := createCategory(t, taxonomy, token, "Unrelated", nil)
	var item Item
	changed := requireProjectionReplay(t, s, func() { item = createCatalogItem(t, catalog, token, ItemCreate{Name: "Catalog", CategoryID: &left.ID}) })
	if len(changed) != 4 || getCategory(t, taxonomy, token, root.ID).Revision != 4 || getCategory(t, taxonomy, token, left.ID).Revision != 2 {
		t.Fatal("creation did not invalidate classification membership")
	}
	changed = requireProjectionReplay(t, s, func() {
		item, err = catalog.Patch(t.Context(), token, "move-item-classification", item.ID, 1, ItemPatch{CategoryID: NullableID{Set: true, Value: &right.ID}})
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 5 || item.Revision != 2 || item.CategoryID == nil || *item.CategoryID != right.ID ||
		getCategory(t, taxonomy, token, root.ID).Revision != 5 || getCategory(t, taxonomy, token, left.ID).Revision != 3 ||
		getCategory(t, taxonomy, token, right.ID).Revision != 2 || changed[projectionKey{"category", unrelated.ID}] ||
		!strings.Contains(approvedText(t, s, item.ID), "Right") || strings.Contains(approvedText(t, s, item.ID), "Left") {
		t.Fatal("classification move missed old/new paths or incremented a shared ancestor twice")
	}
	before := domainState(t, s)
	if _, err := taxonomy.DeleteCategory(t.Context(), token, "old-classification-delete-proof", left.ID, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("classification membership did not invalidate an old category review", err)
	}
	if _, err := catalog.Create(t.Context(), token, "missing-create-category", ItemCreate{Name: "Missing", CategoryID: testPointer("missing")}); !errors.Is(err, ErrNotFound) {
		t.Fatal("creation silently removed or invented a missing classification", err)
	}
	if _, err := catalog.Patch(t.Context(), token, "missing-patch-category", item.ID, 2, ItemPatch{CategoryID: NullableID{Set: true, Value: testPointer("missing")}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("patch silently adopted another category", err)
	}
	requireDomainState(t, s, before)
	item, err = catalog.Patch(t.Context(), token, "clear-item-classification", item.ID, 2, ItemPatch{CategoryID: NullableID{Set: true}})
	if err != nil || item.CategoryID != nil || item.Revision != 3 || getCategory(t, taxonomy, token, root.ID).Revision != 6 ||
		getCategory(t, taxonomy, token, right.ID).Revision != 3 {
		t.Fatal("explicit classification removal missed its old aggregate", err)
	}
}

func TestCatalogHistoryGuardsIncludeDisposedZeroAndFinancialRowsButArchiveIsMetadata(t *testing.T) {
	for _, history := range []string{"stored", "retired", "disposed", "positive stock", "zero stock", "purchase", "movement"} {
		t.Run(history, func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			input := ItemCreate{Name: "HistoricalItem"}
			if strings.Contains(history, "stock") {
				input.TrackingMode = testPointer("quantity")
			}
			item := createCatalogItem(t, catalog, token, input)
			switch history {
			case "stored", "retired", "disposed":
				seedSearchUnit(t, s, "unit", item.ID, "ASSET", nil, "UnitNote")
				execSQL(t, s.db, "UPDATE item_units SET status=? WHERE id='unit'", history)
			case "positive stock", "zero stock":
				execSQL(t, s.db, "INSERT INTO locations(id,code,name,created_at,updated_at) VALUES('location','BOX','Box','2026','2026')")
				quantity := int64(0)
				if history == "positive stock" {
					quantity = 1500
				}
				execSQL(t, s.db, "INSERT INTO stock_balances VALUES(?,'location',?,'2026')", item.ID, quantity)
			case "purchase":
				execSQL(t, s.db, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,created_at) VALUES('purchase','Merchant','2026','TWD','2026')")
				execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor) VALUES('line','purchase',?,1,100)", item.ID)
			case "movement":
				execSQL(t, s.db, "INSERT INTO inventory_events(id,item_id,action,quantity_base,occurred_at) VALUES('movement',?,'adjust',0,'2026')", item.ID)
			}
			before := domainState(t, s)
			mode := "quantity"
			if item.TrackingMode == mode {
				mode = "individual"
			}
			for index, patch := range []ItemPatch{
				{TrackingMode: &mode}, {UnitCode: testPointer("changed")}, {BaseUnit: testPointer("changed")}, {ScaleFactor: testPointer(int64(2))},
			} {
				value, err := catalog.Patch(t.Context(), token, fmt.Sprintf("history-patch-%d", index), item.ID, 1, patch)
				if !errors.Is(err, ErrItemHistory) || value.ID != "" {
					t.Fatal("history was reinterpreted by a metadata patch", err)
				}
			}
			if ack, err := catalog.Delete(t.Context(), token, "delete-historical-item", item.ID, 1); !errors.Is(err, ErrItemHistory) || ack.OK {
				t.Fatal("hard deletion erased a historical reference", err)
			}
			requireDomainState(t, s, before)
			changed := requireProjectionReplay(t, s, func() {
				if value, err := catalog.Patch(t.Context(), token, "history-measurement-noop", item.ID, 1, ItemPatch{
					TrackingMode: &item.TrackingMode, UnitCode: &item.UnitCode, BaseUnit: &item.BaseUnit, ScaleFactor: &item.ScaleFactor,
				}); err != nil || value.Revision != 1 {
					t.Fatal("repeating unchanged metadata was treated as a precision migration", err)
				}
			})
			if len(changed) != 0 {
				t.Fatal("a historical item noop emitted new projections")
			}
			archived, err := catalog.Archive(t.Context(), token, "archive-historical-item", item.ID, 1, "  Keep history  ")
			if err != nil || archived.Status != "archived" || archived.Revision != 2 ||
				scalar[string](t, s.db, "SELECT reason FROM mutation_events WHERE operation='post_items_id_archive'") != "  Keep history  " {
				t.Fatal("catalog archival disposed holdings or lost the exact audit reason", err)
			}
			active, err := catalog.Patch(t.Context(), token, "restore-historical-item", item.ID, 2, ItemPatch{Status: testPointer("active")})
			if err != nil || active.Status != "active" || active.Revision != 3 {
				t.Fatal("metadata restoration changed or rejected existing holdings", err)
			}
			after := domainState(t, s)
			for _, table := range []string{"item_units", "stock_balances", "purchases", "purchase_lines", "inventory_events"} {
				if after[table] != before[table] {
					t.Fatal("metadata archival/restoration changed physical or financial history", table)
				}
			}
		})
	}
}

func TestCatalogImportedIdentitiesStayStableAndMissingCountersAreNotInvented(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	for index, id := range []string{"new", "tree", strings.Repeat("😀", 128), "<& \""} {
		execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Imported','individual','2026','2026')", id)
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", id)
		value := getCatalogItem(t, catalog, token, id)
		if value.ID != id || value.Revision != 1 {
			t.Fatal("imported identity or retained counter was normalized")
		}
		requireProjectionReplay(t, s, func() {
			value, err := catalog.Patch(t.Context(), token, fmt.Sprintf("imported-item-patch-%d", index), id, 1, ItemPatch{Name: testPointer("Renamed")})
			if err != nil || value.ID != id || value.Revision != 2 {
				t.Fatal("a valid imported identity could not be edited without rewriting it", err)
			}
		})
	}
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('uncounted','Invalid','individual','2026','2026')")
	if value, err := catalog.Get(t.Context(), token, "uncounted"); !errors.Is(err, ErrDomainState) || value.ID != "" {
		t.Fatal("a missing aggregate counter was synthesized into a current resource", err)
	}
	if page, err := catalog.List(t.Context(), token, "", 100); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextID != "" {
		t.Fatal("an invalid retained revision exposed a partial collection", err)
	}
}

func TestCatalogEveryMutationAndNoopHasExactFrozenProjectionReplay(t *testing.T) {
	for _, operation := range []string{"create", "patch", "archive", "restore", "delete", "noop", "archive noop"} {
		t.Run(operation, func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Before"})
			if operation == "restore" || operation == "archive noop" {
				var err error
				item, err = catalog.Archive(t.Context(), token, "before-catalog-replay", item.ID, 1, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			changed := requireProjectionReplay(t, s, func() {
				var err error
				switch operation {
				case "create":
					_, err = catalog.Create(t.Context(), token, "catalog-create-replay", ItemCreate{Name: "After"})
				case "patch":
					_, err = catalog.Patch(t.Context(), token, "catalog-patch-replay", item.ID, item.Revision, ItemPatch{Name: testPointer("After")})
				case "archive", "archive noop":
					_, err = catalog.Archive(t.Context(), token, "catalog-archive-replay", item.ID, item.Revision, "")
				case "restore":
					_, err = catalog.Patch(t.Context(), token, "catalog-restore-replay", item.ID, item.Revision, ItemPatch{Status: testPointer("active")})
				case "delete":
					_, err = catalog.Delete(t.Context(), token, "catalog-delete-replay", item.ID, item.Revision)
				case "noop":
					_, err = catalog.Patch(t.Context(), token, "catalog-noop-replay", item.ID, item.Revision, ItemPatch{Name: &item.Name})
				}
				if err != nil {
					t.Fatal(err)
				}
			})
			if operation == "noop" || operation == "archive noop" {
				if len(changed) != 0 {
					t.Fatal("a noop invented a current/outbox mutation")
				}
			} else if len(changed) != 2 {
				t.Fatal("a simple item mutation lost its independent item/search projection")
			}
		})
	}
}

func TestCatalogInternalPagesAreStableCompleteAndFailClosedOnCorruptRows(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	var want []string
	for index := range 5 {
		input := ItemCreate{Name: fmt.Sprintf("Item-%d", index)}
		if index == 0 {
			input.Status = testPointer("archived")
		}
		want = append(want, createCatalogItem(t, catalog, token, input).ID)
	}
	sort.Strings(want)
	var got []string
	after := ""
	for {
		page, err := catalog.List(t.Context(), token, after, 2)
		if err != nil || page.Data == nil || len(page.Data) > 2 {
			t.Fatal("internal item page was incomplete or malformed", err)
		}
		for _, item := range page.Data {
			got = append(got, item.ID)
		}
		if page.NextID == "" {
			break
		}
		after = page.NextID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("stable paging omitted/reordered an item, including archived metadata")
	}
	if page, err := catalog.List(t.Context(), token, want[len(want)-1], 100); err != nil || page.Data == nil || len(page.Data) != 0 || page.NextID != "" {
		t.Fatal("terminal internal page was not an explicit empty collection", err)
	}
	execSQL(t, s.db, "UPDATE items SET name='' WHERE id=?", want[2])
	if page, err := catalog.List(t.Context(), token, "", 2); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextID != "" {
		t.Fatal("a corrupt lookahead row exposed a valid prefix or continuation", err)
	}
	if value, err := catalog.Get(t.Context(), token, want[2]); !errors.Is(err, ErrDomainState) || value.ID != "" {
		t.Fatal("a corrupt item escaped resource validation", err)
	}
}
