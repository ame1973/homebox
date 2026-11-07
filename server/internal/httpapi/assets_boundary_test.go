package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"homebox/internal/store"
)

type assetHTTPSource struct {
	auth      *store.Auth
	handler   http.Handler
	cookie    *http.Cookie
	csrf      string
	catalog   *store.Catalog
	inventory *store.Inventory
	assets    *store.Assets
	item      store.Item
	unit      store.Unit
	asset     store.Asset
	input     store.AssetProfileWrite
}

func assetHTTPSourceFixture(t *testing.T, profile bool) assetHTTPSource {
	t.Helper()
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	catalog, err := store.NewCatalog(auth)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := store.NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := store.NewAssets(auth)
	if err != nil {
		t.Fatal(err)
	}
	item, err := catalog.Create(t.Context(), cookie.Value, "asset-boundary-owner", store.ItemCreate{Name: "Original owner"})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := inventory.CreateUnit(t.Context(), cookie.Value, "asset-boundary-unit", item.ID, item.Revision, store.UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	item, err = catalog.Get(t.Context(), cookie.Value, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	manual := int64(100)
	input := store.AssetProfileWrite{ItemRevision: item.Revision, CostBasis: "manual", Currency: "JPY", ManualAcquisitionMinor: &manual, UsageUnit: "times"}
	var asset store.Asset
	if profile {
		asset, err = assets.PutProfile(t.Context(), cookie.Value, "asset-boundary-profile", unit.ID, unit.Revision, input)
		if err != nil {
			t.Fatal(err)
		}
		item, err = catalog.Get(t.Context(), cookie.Value, item.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return assetHTTPSource{auth, handler, cookie, csrf, catalog, inventory, assets, item, unit, asset, input}
}

func TestAssetsHTTPEveryWriterRechecksSQLAuthorizationAfterReadingItsActualBody(t *testing.T) {
	for _, operation := range assetOperationsTest() {
		if operation.method == http.MethodGet {
			continue
		}
		t.Run(operation.path, func(t *testing.T) {
			f := assetHTTPSourceFixture(t, operation.method != http.MethodPut)
			body := operation.body
			if operation.method == http.MethodPut {
				encoded, err := json.Marshal(f.input)
				if err != nil {
					t.Fatal(err)
				}
				body = string(encoded)
			}
			path := strings.Replace(operation.path, "/unit/", "/"+f.unit.ID+"/", 1)
			r := domainRequest(operation.method, path, body, f.cookie, f.csrf, "asset-body-original-key", `"1"`)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "asset-revoke-during-body"); err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(f.handler, r), 401, "AUTHENTICATION_REQUIRED")
			fresh, csrf := testCredentials(t, f.auth, changedPassword)
			if item, err := f.catalog.Get(t.Context(), fresh.Value, f.item.ID); err != nil || !reflect.DeepEqual(item, f.item) {
				t.Fatal("body-time revocation changed Item financial authority", err)
			}
			if unit, err := f.inventory.GetUnit(t.Context(), fresh.Value, f.unit.ID); err != nil || !reflect.DeepEqual(unit, f.unit) {
				t.Fatal("body-time revocation changed physical source", err)
			}
			page, err := f.assets.Page(t.Context(), fresh.Value, "", 50, store.AssetFilters{})
			if err != nil || len(page.Data) != int(f.asset.Revision) || f.asset.Revision != 0 && !reflect.DeepEqual(page.Data[0], f.asset) {
				t.Fatal("body-time revocation created/changed a profile", err)
			}
			usage, err := f.assets.PageUsage(t.Context(), fresh.Value, f.unit.ID, "", 50)
			if f.asset.Revision != 0 && (err != nil || len(usage.Data) != 0) {
				t.Fatal("body-time revocation appended usage history", err)
			}
			costs, err := f.assets.PageCosts(t.Context(), fresh.Value, f.unit.ID, "", 50)
			if f.asset.Revision != 0 && (err != nil || len(costs.Data) != 0) {
				t.Fatal("body-time revocation appended cost history", err)
			}
			// Reuse the exact unsuccessful command, not a replacement key or proof.
			response := respond(f.handler, domainRequest(operation.method, path, body, fresh, csrf, "asset-body-original-key", `"1"`))
			if response.Code != 200 {
				t.Fatal("revocation left a partial claim or poisoned the original command", response.Code, response.Body.String())
			}
		})
	}
}

func TestAssetsHTTPOriginalFinancialAndPhysicalProofsAreRecheckedAfterBodyReading(t *testing.T) {
	for _, kind := range []string{"profile Item", "usage Asset", "cost Asset", "retire Unit", "dispose Unit"} {
		t.Run(kind, func(t *testing.T) {
			f := assetHTTPSourceFixture(t, kind != "profile Item")
			method, tail, body := http.MethodPut, "asset", ""
			if kind == "profile Item" {
				encoded, _ := json.Marshal(f.input)
				body = string(encoded)
			} else {
				index := map[string]int{"usage Asset": 1, "cost Asset": 2, "retire Unit": 3, "dispose Unit": 4}[kind]
				tail, body, method = []string{"", "usage", "costs", "retire", "disposal"}[index], assetJSONBodies[index].body, http.MethodPost
			}
			r := domainRequest(method, "/api/v1/units/"+f.unit.ID+"/"+tail, body, f.cookie, f.csrf, "asset-proof-before-body", `"1"`)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				var err error
				if kind == "profile Item" {
					name := "Concurrent Item interpretation"
					_, err = f.catalog.Patch(t.Context(), f.cookie.Value, "asset-body-changed-item", f.item.ID, f.item.Revision, store.ItemPatch{Name: &name})
				} else if kind == "usage Asset" || kind == "cost Asset" {
					input := f.input
					input.ItemRevision, input.AssetRevision = f.item.Revision, &f.asset.Revision
					residual := int64(10)
					input.EstimatedResidualMinor = &residual
					_, err = f.assets.PutProfile(t.Context(), f.cookie.Value, "asset-body-changed-profile", f.unit.ID, f.unit.Revision, input)
				} else {
					locations, createErr := store.NewLocations(f.auth)
					if createErr != nil {
						t.Fatal(createErr)
					}
					place, createErr := locations.Create(t.Context(), f.cookie.Value, "asset-body-new-placement", 0, store.LocationCreate{Code: "CONCURRENT", Name: "Concurrent placement"})
					if createErr != nil {
						t.Fatal(createErr)
					}
					_, err = f.inventory.MoveUnit(t.Context(), f.cookie.Value, "asset-body-moved-unit", f.unit.ID, f.unit.Revision, place.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(f.handler, r), 409, "REVISION_CONFLICT")
			page, err := f.assets.Page(t.Context(), f.cookie.Value, "", 50, store.AssetFilters{})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "profile Item" {
				if len(page.Data) != 0 {
					t.Fatal("stale Item proof still opted into finance")
				}
				return
			}
			if len(page.Data) != 1 {
				t.Fatal("stale original command changed profile membership")
			}
			if kind == "usage Asset" || kind == "cost Asset" {
				usage, usageErr := f.assets.PageUsage(t.Context(), f.cookie.Value, f.unit.ID, "", 50)
				costs, costErr := f.assets.PageCosts(t.Context(), f.cookie.Value, f.unit.ID, "", 50)
				if usageErr != nil || costErr != nil || len(usage.Data) != 0 || len(costs.Data) != 0 || page.Data[0].Revision != 2 || page.Data[0].EstimatedResidualMinor == nil || *page.Data[0].EstimatedResidualMinor != 10 {
					t.Fatal("stale Asset proof appended history or replaced concurrent interpretation", usageErr, costErr)
				}
			} else {
				unit, err := f.inventory.GetUnit(t.Context(), f.cookie.Value, f.unit.ID)
				if err != nil || unit.Revision != 2 || unit.Status != "stored" || unit.LocationID == nil || !reflect.DeepEqual(page.Data[0], f.asset) {
					t.Fatal("stale physical proof retired/disposed a concurrently moved Unit", err)
				}
			}
		})
	}
}

func TestAssetsHTTPEventWritersDoNotInventAnExtraUnitOrItemProof(t *testing.T) {
	for _, kind := range []string{"usage", "costs"} {
		t.Run(kind, func(t *testing.T) {
			f := assetHTTPSourceFixture(t, true)
			body, count := assetJSONBodies[1].body, 9
			if kind == "costs" {
				body, count = assetJSONBodies[2].body, 10
			}
			r := domainRequest(http.MethodPost, "/api/v1/units/"+f.unit.ID+"/"+kind, body, f.cookie, f.csrf, "asset-body-unchanged-finance", `"1"`)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				text := "Concurrent physical notes"
				if _, err := f.inventory.PatchUnit(t.Context(), f.cookie.Value, "asset-body-physical-notes", f.unit.ID, f.unit.Revision, store.UnitPatch{Notes: &text}); err != nil {
					t.Fatal(err)
				}
			}})
			response := respond(f.handler, r)
			if kind == "usage" {
				assetHTTPReply[store.Usage](t, response, count)
			} else {
				assetHTTPReply[store.Cost](t, response, count)
			}
			current, err := f.assets.Get(t.Context(), f.cookie.Value, f.unit.ID)
			if err != nil || current.Revision != 2 {
				t.Fatal("unrelated physical metadata became a financial precondition", err)
			}
			unit, err := f.inventory.GetUnit(t.Context(), f.cookie.Value, f.unit.ID)
			if err != nil || unit.Revision != 2 || unit.Notes != "Concurrent physical notes" {
				t.Fatal("event writer rebased or overwrote physical state", err)
			}
		})
	}
}

func TestAssetsHTTPIndependentOriginalProofScopesAndExplicitProfilePresenceAreNotInterchangeable(t *testing.T) {
	for _, kind := range []string{"asset", "usage", "costs", "retire", "disposal"} {
		t.Run(kind, func(t *testing.T) {
			f := assetHTTPSourceFixture(t, true)
			if _, err := f.assets.RecordUsage(t.Context(), f.cookie.Value, "asset-distinct-finance", f.unit.ID, f.asset.Revision, store.UsageWrite{Delta: 1, UsageUnit: "times", EventType: "use", OccurredAt: "2026-11-07T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			item, err := f.catalog.Get(t.Context(), f.cookie.Value, f.item.ID)
			if err != nil || item.Revision != 4 {
				t.Fatal("independent proof fixture failed", err)
			}
			path, method, proof := "/api/v1/units/"+f.unit.ID+"/"+kind, http.MethodPost, `"2"`
			var body string
			if kind == "asset" {
				method, proof = http.MethodPut, `"1"`
				input := f.input
				revision, residual := int64(2), int64(10)
				input.ItemRevision, input.AssetRevision, input.EstimatedResidualMinor = item.Revision, &revision, &residual
				encoded, _ := json.Marshal(input)
				body = string(encoded)
				for _, invalid := range []string{
					assetJSONMutation(t, body, "asset_revision", "null"),
					assetJSONMutation(t, body, "asset_revision", "1"),
					assetJSONMutation(t, body, "item_revision", "3"),
				} {
					expectError(t, respond(f.handler, domainRequest(method, path, invalid, f.cookie, f.csrf, "asset-independent-proof", proof)), 409, "REVISION_CONFLICT")
				}
				expectError(t, respond(f.handler, domainRequest(method, path, body, f.cookie, f.csrf, "asset-independent-proof", `"2"`)), 409, "REVISION_CONFLICT")
			} else {
				index := map[string]int{"usage": 1, "costs": 2, "retire": 3, "disposal": 4}[kind]
				body = assetJSONBodies[index].body
				for _, wrong := range []string{`"1"`, `"4"`} {
					expectError(t, respond(f.handler, domainRequest(method, path, body, f.cookie, f.csrf, "asset-independent-proof", wrong)), 409, "REVISION_CONFLICT")
				}
				if kind == "retire" || kind == "disposal" {
					wrongUnit := assetJSONMutation(t, body, "unit_revision", "2")
					expectError(t, respond(f.handler, domainRequest(method, path, wrongUnit, f.cookie, f.csrf, "asset-independent-proof", proof)), 409, "REVISION_CONFLICT")
				}
			}
			response := respond(f.handler, domainRequest(method, path, body, f.cookie, f.csrf, "asset-independent-proof", proof))
			if response.Code != 200 {
				t.Fatal("correct original independent proofs were rejected after failed proposals", kind, response.Code, response.Body.String())
			}
		})
	}
}

func TestAssetsHTTPExplicitKeysBindCompleteInputOriginalHeaderOperationAndOwner(t *testing.T) {
	for _, operation := range assetOperationsTest() {
		if operation.method == http.MethodGet {
			continue
		}
		t.Run(operation.path, func(t *testing.T) {
			f := assetHTTPSourceFixture(t, operation.method != http.MethodPut)
			path := strings.Replace(operation.path, "/unit/", "/"+f.unit.ID+"/", 1)
			body := operation.body
			field, raw := "note", `"Different exact intent"`
			if operation.method == http.MethodPut {
				encoded, _ := json.Marshal(f.input)
				body, field, raw = string(encoded), "estimated_residual_minor", "1"
			}
			original := respond(f.handler, domainRequest(operation.method, path, body, f.cookie, f.csrf, "asset-whole-original-key", `"1"`))
			if original.Code != 200 {
				t.Fatal("original actual command failed", original.Code, original.Body.String())
			}
			for _, changed := range []struct{ method, path, body, proof string }{
				{operation.method, path, assetJSONMutation(t, body, field, raw), `"1"`},
				{operation.method, path, body, `"2"`},
				{operation.method, strings.Replace(path, f.unit.ID, "another-owner", 1), body, `"1"`},
				{http.MethodPost, "/api/v1/units/" + f.unit.ID + "/usage", assetJSONMutation(t, assetJSONBodies[1].body, "delta", "2"), `"1"`},
			} {
				expectError(t, respond(f.handler, domainRequest(changed.method, changed.path, changed.body, f.cookie, f.csrf, "asset-whole-original-key", changed.proof)), 409, "IDEMPOTENCY_CONFLICT")
			}
			retried := respond(f.handler, domainRequest(operation.method, path, body, f.cookie, f.csrf, "asset-whole-original-key", `"1"`))
			if retried.Code != 200 || retried.Body.String() != original.Body.String() || retried.Header().Get("ETag") != original.Header().Get("ETag") {
				t.Fatal("conflicting attempts replaced the original frozen reply")
			}
		})
	}
}

func TestAssetsHTTPDefaultCollectionsCompleteActualProfilesAndIndependentOwnedHistory(t *testing.T) {
	f := assetHTTPSourceFixture(t, true)
	var otherID string
	// Real transaction services create >1 page without HTTP flooding, SQL exposure
	// or a production limiter override. Event IDs and counters are never invented.
	for index := range 50 {
		item, err := f.catalog.Get(t.Context(), f.cookie.Value, f.item.ID)
		if err != nil {
			t.Fatal(err)
		}
		unit, err := f.inventory.CreateUnit(t.Context(), f.cookie.Value, fmt.Sprintf("asset-default-unit-%d", index), item.ID, item.Revision, store.UnitCreate{})
		if err != nil {
			t.Fatal(err)
		}
		otherID = unit.ID
		item, err = f.catalog.Get(t.Context(), f.cookie.Value, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		input := f.input
		input.ItemRevision = item.Revision
		if _, err := f.assets.PutProfile(t.Context(), f.cookie.Value, fmt.Sprintf("asset-default-profile-%d", index), unit.ID, unit.Revision, input); err != nil {
			t.Fatal(err)
		}
	}
	for index := range 51 {
		asset, err := f.assets.Get(t.Context(), f.cookie.Value, f.unit.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.assets.RecordUsage(t.Context(), f.cookie.Value, fmt.Sprintf("asset-default-use-%d", index), f.unit.ID, asset.Revision, store.UsageWrite{Delta: 1, UsageUnit: "times", EventType: "use", OccurredAt: "2026-11-07T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		asset, err = f.assets.Get(t.Context(), f.cookie.Value, f.unit.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.assets.RecordCost(t.Context(), f.cookie.Value, fmt.Sprintf("asset-default-cost-%d", index), f.unit.ID, asset.Revision, store.CostWrite{AmountMinor: 1, Currency: "JPY", Kind: "repair", Direction: "expense", OccurredAt: "2026-11-07T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"/api/v1/assets", "/api/v1/units/" + f.unit.ID + "/usage", "/api/v1/units/" + f.unit.ID + "/costs"}
	cursors := make(map[string]string)
	for _, path := range paths {
		first := ownedPageReply(t, respond(f.handler, domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")))
		if len(first.Data) != 50 || first.NextCursor == nil {
			t.Fatal("Asset collection lost its actual default 50-row page", path)
		}
		cursors[path] = *first.NextCursor
		last := ownedPageReply(t, respond(f.handler, domainRequest(http.MethodGet, path+"?cursor="+url.QueryEscape(*first.NextCursor), "", f.cookie, f.csrf, "", "")))
		if len(last.Data) != 1 || last.NextCursor != nil {
			t.Fatal("Asset collection did not finish its remaining actual row", path)
		}
		seen := make(map[string]bool)
		for _, row := range append(first.Data, last.Data...) {
			field := "id"
			if path == "/api/v1/assets" {
				field = "item_unit_id"
			}
			var id, owner string
			if json.Unmarshal(row[field], &id) != nil || id == "" || seen[id] {
				t.Fatal("live pages repeated or fabricated an identity", path)
			}
			seen[id] = true
			if path != "/api/v1/assets" && (json.Unmarshal(row["item_unit_id"], &owner) != nil || owner != f.unit.ID) {
				t.Fatal("owned event pages borrowed another Unit's history")
			}
		}
		for _, query := range []string{"limit=1&cursor=", "limit=100&cursor="} {
			expectError(t, respond(f.handler, domainRequest(http.MethodGet, path+"?"+query+url.QueryEscape(*first.NextCursor), "", f.cookie, f.csrf, "", "")), 400, "INVALID_CURSOR")
		}
	}
	for _, path := range paths {
		for other, cursor := range cursors {
			if other != path {
				expectError(t, respond(f.handler, domainRequest(http.MethodGet, path+"?cursor="+url.QueryEscape(cursor), "", f.cookie, f.csrf, "", "")), 400, "INVALID_CURSOR")
			}
		}
	}
	for _, kind := range []string{"usage", "costs"} {
		expectError(t, respond(f.handler, domainRequest(http.MethodGet, "/api/v1/units/"+otherID+"/"+kind+"?cursor="+url.QueryEscape(cursors["/api/v1/units/"+f.unit.ID+"/"+kind]), "", f.cookie, f.csrf, "", "")), 400, "INVALID_CURSOR")
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, "/api/v1/assets?currency=JPY&cursor="+url.QueryEscape(cursors["/api/v1/assets"]), "", f.cookie, f.csrf, "", "")), 400, "INVALID_CURSOR")
	fresh, csrf := testCredentials(t, f.auth, changedPassword)
	for _, path := range paths {
		page := ownedPageReply(t, respond(f.handler, domainRequest(http.MethodGet, path+"?cursor="+url.QueryEscape(cursors[path]), "", fresh, csrf, "", "")))
		if len(page.Data) != 1 || page.NextCursor != nil {
			t.Fatal("same-principal fresh login invalidated a live collection continuation")
		}
	}
	asset := assetHTTPReply[store.Asset](t, respond(f.handler, domainRequest(http.MethodGet, "/api/v1/units/"+f.unit.ID+"/asset", "", fresh, csrf, "", "")), 9)
	if asset.Revision != 103 {
		t.Fatal("collection reads invented aggregate authority", strconv.FormatInt(asset.Revision, 10))
	}
}

func TestAssetsHTTPFinancialPolicyOverflowAndRecordedLifecycleConflictsRemainAuthoritative(t *testing.T) {
	t.Run("new currency", func(t *testing.T) {
		f := assetHTTPSourceFixture(t, false)
		input := f.input
		input.Currency = "ZZZ"
		body, _ := json.Marshal(input)
		expectError(t, respond(f.handler, domainRequest(http.MethodPut, "/api/v1/units/"+f.unit.ID+"/asset", string(body), f.cookie, f.csrf, "asset-unsupported-money", `"1"`)), 409, "ASSET_CURRENCY_CONFLICT")
		if page, err := f.assets.Page(t.Context(), f.cookie.Value, "", 50, store.AssetFilters{}); err != nil || len(page.Data) != 0 {
			t.Fatal("unsupported new money opted into a profile", err)
		}
	})
	t.Run("proposed source", func(t *testing.T) {
		f := assetHTTPSourceFixture(t, true)
		path := "/api/v1/units/" + f.unit.ID
		for _, test := range []struct {
			tail, body, code string
			status           int
		}{
			{"costs", assetJSONMutation(t, assetJSONBodies[2].body, "currency", `"ZZZ"`), "ASSET_CURRENCY_CONFLICT", 409},
			{"costs", assetJSONMutation(t, assetJSONBodies[2].body, "currency", `"USD"`), "VALIDATION_ERROR", 422},
			{"costs", assetJSONMutation(t, assetJSONBodies[2].body, "amount_minor", "9007199254740991"), "ASSET_TOTAL_OVERFLOW", 409},
			{"usage", assetJSONMutation(t, assetJSONBodies[1].body, "usage_unit", `"minutes"`), "VALIDATION_ERROR", 422},
			{"usage", `{"delta":-1,"usage_unit":"times","event_type":"correction","occurred_at":"2026-11-07T00:00:00Z","correction_of":"missing"}`, "NOT_FOUND", 404},
		} {
			expectError(t, respond(f.handler, domainRequest(http.MethodPost, path+"/"+test.tail, test.body, f.cookie, f.csrf, "asset-invalid-finance-source", `"1"`)), test.status, test.code)
		}
		usage, usageErr := f.assets.PageUsage(t.Context(), f.cookie.Value, f.unit.ID, "", 50)
		costs, costErr := f.assets.PageCosts(t.Context(), f.cookie.Value, f.unit.ID, "", 50)
		current, err := f.assets.Get(t.Context(), f.cookie.Value, f.unit.ID)
		if err != nil || usageErr != nil || costErr != nil || len(usage.Data) != 0 || len(costs.Data) != 0 || !reflect.DeepEqual(current, f.asset) {
			t.Fatal("invalid proposed source committed partial history/financial authority", err, usageErr, costErr)
		}
	})
	t.Run("recorded lifecycle", func(t *testing.T) {
		f := assetHTTPSourceFixture(t, true)
		path := "/api/v1/units/" + f.unit.ID
		retired := assetHTTPReply[store.Unit](t, respond(f.handler, domainRequest(http.MethodPost, path+"/retire", assetJSONBodies[3].body, f.cookie, f.csrf, "asset-recorded-retirement", `"1"`)), 8)
		if retired.Revision != 2 {
			t.Fatal("retirement fixture lost independent Unit proof")
		}
		changed := `{"unit_revision":2,"retired_at":"2026-11-08T00:00:00Z"}`
		expectError(t, respond(f.handler, domainRequest(http.MethodPost, path+"/retire", changed, f.cookie, f.csrf, "asset-overwrite-retirement", `"2"`)), 409, "ASSET_HISTORY_LOCKED")
		body := assetJSONMutation(t, assetJSONBodies[4].body, "unit_revision", "2")
		original := respond(f.handler, domainRequest(http.MethodPost, path+"/disposal", body, f.cookie, f.csrf, "asset-recorded-disposal", `"2"`))
		assetHTTPReply[store.Disposal](t, original, 7)
		changed = assetJSONMutation(t, assetJSONMutation(t, body, "unit_revision", "3"), "realized_proceeds_minor", "1")
		expectError(t, respond(f.handler, domainRequest(http.MethodPost, path+"/disposal", changed, f.cookie, f.csrf, "asset-overwrite-disposal", `"3"`)), 409, "ASSET_HISTORY_LOCKED")
		expectError(t, respond(f.handler, domainRequest(http.MethodPost, path+"/retire", `{"unit_revision":3,"retired_at":"2026-11-07T00:00:00Z"}`, f.cookie, f.csrf, "asset-revive-disposal", `"3"`)), 409, "UNIT_DISPOSED")
		actual := respond(f.handler, domainRequest(http.MethodGet, path+"/disposal", "", f.cookie, f.csrf, "", ""))
		if actual.Code != 200 || actual.Body.String() != original.Body.String() || actual.Header().Get("ETag") != original.Header().Get("ETag") {
			t.Fatal("rejected lifecycle changes overwrote immutable recorded source")
		}
	})
}
