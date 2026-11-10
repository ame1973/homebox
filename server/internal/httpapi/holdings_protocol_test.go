package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"homebox/internal/store"
)

func TestHoldingsHTTPRevalidatesSessionAfterBodyReadingForEveryWriter(t *testing.T) {
	for _, operation := range holdingOperationsTest() {
		if operation.method == http.MethodGet {
			continue
		}
		t.Run(operation.method+operation.path, func(t *testing.T) {
			auth, handler, cookie, csrf := domainHTTPFixture(t)
			r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "holding-late-revocation", `"1"`)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				if _, err := auth.Logout(t.Context(), cookie.Value, "holding-revoke-during-body"); err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
		})
	}
}

func TestHoldingsHTTPOriginalItemAndUnitProofsAreRecheckedAfterBodyReading(t *testing.T) {
	for _, kind := range []string{"unit creation", "unit patch", "stock receive"} {
		t.Run(kind, func(t *testing.T) {
			auth, handler, cookie, csrf := domainHTTPFixture(t)
			catalog, _ := store.NewCatalog(auth)
			inventory, _ := store.NewInventory(auth)
			mode := "individual"
			if kind == "stock receive" {
				mode = "quantity"
			}
			item, err := catalog.Create(t.Context(), cookie.Value, "holding-late-proof-owner", store.ItemCreate{Name: "Original owner", TrackingMode: &mode})
			if err != nil {
				t.Fatal(err)
			}
			method, path, body := http.MethodPost, "/api/v1/items/"+item.ID+"/units", `{"location_id":null}`
			var unit store.Unit
			if kind == "unit patch" {
				unit, err = inventory.CreateUnit(t.Context(), cookie.Value, "holding-late-proof-unit", item.ID, item.Revision, store.UnitCreate{})
				if err != nil {
					t.Fatal(err)
				}
				method, path, body = http.MethodPatch, "/api/v1/units/"+unit.ID, `{"notes":"Must not rebase"}`
			} else if kind == "stock receive" {
				locations, _ := store.NewLocations(auth)
				location, err := locations.Create(t.Context(), cookie.Value, "holding-late-proof-location", 0, store.LocationCreate{Code: "TARGET", Name: "Target"})
				if err != nil {
					t.Fatal(err)
				}
				path, body = "/api/v1/stock/receive", fmt.Sprintf(`{"item_id":%q,"target_location_id":%q,"quantity_base":1}`, item.ID, location.ID)
			}
			r := domainRequest(method, path, body, cookie, csrf, "holding-body-original-proof", `"1"`)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				text := "Concurrent source"
				if kind == "unit patch" {
					_, err = inventory.PatchUnit(t.Context(), cookie.Value, "holding-body-concurrent-unit", unit.ID, 1, store.UnitPatch{Notes: &text})
				} else {
					_, err = catalog.Patch(t.Context(), cookie.Value, "holding-body-concurrent-item", item.ID, 1, store.ItemPatch{Name: &text})
				}
				if err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(handler, r), 409, "REVISION_CONFLICT")
			if kind == "unit patch" {
				current, err := inventory.GetUnit(t.Context(), cookie.Value, unit.ID)
				if err != nil || current.Revision != 2 || current.Notes != "Concurrent source" {
					t.Fatal("Unit HTTP writer silently rebased after body reading", err)
				}
			} else {
				current, err := catalog.Get(t.Context(), cookie.Value, item.ID)
				if err != nil || current.Revision != 2 || current.Name != "Concurrent source" {
					t.Fatal("holding writer overwrote changed Item scope", err)
				}
				page, err := inventory.PageUnits(t.Context(), cookie.Value, item.ID, "", 50)
				if err != nil || len(page.Data) != 0 {
					t.Fatal("stale Item proof still created a Unit", err)
				}
				stock, err := inventory.PageStock(t.Context(), cookie.Value, "", 50, store.StockFilters{})
				if err != nil || len(stock.Data) != 0 {
					t.Fatal("stale Item proof still received stock", err)
				}
			}
		})
	}
}

func TestHoldingsHTTPDefaultPagesCompleteOnlyTheirRealOwnerAndCollection(t *testing.T) {
	var databasePath string
	auth, handler, cookie, csrf := domainHTTPFixture(t, &databasePath)
	var started, ended time.Time
	defer func() {
		if t.Failed() {
			logSessionFailure(t, databasePath, cookie, started, ended)
		}
	}()
	catalog, _ := store.NewCatalog(auth)
	inventory, _ := store.NewInventory(auth)
	locations, _ := store.NewLocations(auth)
	started = time.Now()
	individual, err := catalog.Create(t.Context(), cookie.Value, "holding-default-individual", store.ItemCreate{Name: "Individual"})
	ended = time.Now()
	if err != nil {
		t.Fatal(err)
	}
	mode := "quantity"
	started = time.Now()
	quantity, err := catalog.Create(t.Context(), cookie.Value, "holding-default-quantity", store.ItemCreate{Name: "Quantity", TrackingMode: &mode})
	ended = time.Now()
	if err != nil {
		t.Fatal(err)
	}
	// Seed through real transaction services, not HTTP flooding, SQL exposure or a
	// production rate-limit override. Each signed collection still has >1 page.
	for index := range 51 {
		started = time.Now()
		_, err = inventory.CreateUnit(t.Context(), cookie.Value, fmt.Sprintf("holding-default-unit-%d", index), individual.ID, int64(index+1), store.UnitCreate{})
		ended = time.Now()
		if err != nil {
			t.Fatal(err)
		}
		started = time.Now()
		location, err := locations.Create(t.Context(), cookie.Value, fmt.Sprintf("holding-default-place-%d", index), 0, store.LocationCreate{Code: fmt.Sprintf("PLACE-%d", index), Name: "Placement"})
		ended = time.Now()
		if err != nil {
			t.Fatal(err)
		}
		started = time.Now()
		_, err = inventory.Receive(t.Context(), cookie.Value, fmt.Sprintf("holding-default-stock-%d", index), int64(index+1), store.StockReceive{ItemID: quantity.ID, TargetLocationID: location.ID, QuantityBase: 1})
		ended = time.Now()
		if err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string) *httptest.ResponseRecorder {
		started = time.Now()
		response := respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", ""))
		ended = time.Now()
		return response
	}
	call := func(path string) {
		first := ownedPageReply(t, get(path))
		if len(first.Data) != 50 || first.NextCursor == nil {
			t.Fatal("holding collection did not preserve its default 50-row page", path)
		}
		last := ownedPageReply(t, get(path+"?cursor="+url.QueryEscape(*first.NextCursor)))
		if len(last.Data) != 1 || last.NextCursor != nil {
			t.Fatal("holding collection did not finish the remaining source", path)
		}
	}
	call("/api/v1/items/" + individual.ID + "/units")
	call("/api/v1/inventory")
	units := ownedPageReply(t, get("/api/v1/items/"+individual.ID+"/units?limit=1"))
	if units.NextCursor == nil {
		t.Fatal("Unit cursor fixture failed")
	}
	expectError(t, get("/api/v1/items/"+quantity.ID+"/units?limit=1&cursor="+url.QueryEscape(*units.NextCursor)), 400, "INVALID_CURSOR")
	stock := ownedPageReply(t, get("/api/v1/inventory?limit=1"))
	if stock.NextCursor == nil {
		t.Fatal("Stock cursor fixture failed")
	}
	for _, path := range []string{"/api/v1/inventory?limit=2&cursor=" + url.QueryEscape(*stock.NextCursor), "/api/v1/inventory?limit=1&status=active&cursor=" + url.QueryEscape(*stock.NextCursor)} {
		expectError(t, get(path), 400, "INVALID_CURSOR")
	}
}

func TestHoldingsHTTPTrackingSafeArithmeticAndRuntimeTransferGuardsStayAuthoritative(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	catalog, _ := store.NewCatalog(auth)
	inventory, _ := store.NewInventory(auth)
	locations, _ := store.NewLocations(auth)
	mode := "quantity"
	quantity, err := catalog.Create(t.Context(), cookie.Value, "holding-conflict-quantity", store.ItemCreate{Name: "Quantity", TrackingMode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	individual, err := catalog.Create(t.Context(), cookie.Value, "holding-conflict-individual", store.ItemCreate{Name: "Individual"})
	if err != nil {
		t.Fatal(err)
	}
	location, err := locations.Create(t.Context(), cookie.Value, "holding-conflict-place", 0, store.LocationCreate{Code: "TARGET", Name: "Target"})
	if err != nil {
		t.Fatal(err)
	}
	expectError(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/items/"+quantity.ID+"/units", `{"location_id":null}`, cookie, csrf, "holding-wrong-unit-tracking", `"1"`)), 409, "TRACKING_MODE_CONFLICT")
	expectError(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/stock/receive", fmt.Sprintf(`{"item_id":%q,"target_location_id":%q,"quantity_base":1}`, individual.ID, location.ID), cookie, csrf, "holding-wrong-stock-tracking", `"1"`)), 409, "TRACKING_MODE_CONFLICT")
	if _, err := inventory.Receive(t.Context(), cookie.Value, "holding-prepare-safe-maximum", 1, store.StockReceive{ItemID: quantity.ID, TargetLocationID: location.ID, QuantityBase: maxJSONInteger}); err != nil {
		t.Fatal(err)
	}
	expectError(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/stock/receive", fmt.Sprintf(`{"item_id":%q,"target_location_id":%q,"quantity_base":1}`, quantity.ID, location.ID), cookie, csrf, "holding-safe-add-overflow", `"2"`)), 409, "QUANTITY_OVERFLOW")
	expectError(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/stock/transfer", fmt.Sprintf(`{"item_id":%q,"source_location_id":%q,"target_location_id":%q,"quantity_base":1}`, quantity.ID, location.ID, location.ID), cookie, csrf, "holding-same-place-transfer", `"2"`)), 422, "VALIDATION_ERROR")
	page, err := inventory.PageStock(t.Context(), cookie.Value, "", 50, store.StockFilters{})
	if err != nil || len(page.Data) != 1 || page.Data[0].QuantityBase != maxJSONInteger || page.Data[0].Revision != 1 {
		t.Fatal("rejected HTTP command changed a safe maximum balance", err)
	}
}

func TestHoldingsHTTPUsesTheUnmodifiedGeneralPeerBudget(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for index := range 60 {
		response := respond(handler, domainRequest(http.MethodGet, "/api/v1/inventory", "", cookie, csrf, "", ""))
		if response.Code != 200 {
			t.Fatal("holding read bypassed or prematurely exhausted normal household admission", index, response.Code)
		}
	}
	response := respond(handler, domainRequest(http.MethodGet, "/api/v1/inventory", "", cookie, csrf, "", ""))
	expectError(t, response, 429, "RATE_LIMITED")
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("holding peer limit omitted retry guidance")
	}
}
