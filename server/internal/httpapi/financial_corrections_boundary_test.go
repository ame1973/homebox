package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"homebox/internal/store"
)

type correctionPurchaseFixture struct {
	auth      *store.Auth
	handler   http.Handler
	cookie    *http.Cookie
	csrf      string
	purchases *store.Purchases
	catalog   *store.Catalog
	purchase  store.Purchase
	item      store.Item
	line      store.PurchaseLine
}

func newCorrectionPurchaseFixture(t *testing.T) correctionPurchaseFixture {
	t.Helper()
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	purchases, err := store.NewPurchases(auth)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := store.NewCatalog(auth)
	if err != nil {
		t.Fatal(err)
	}
	item, err := catalog.Create(t.Context(), cookie.Value, "correction-proof-owner", store.ItemCreate{Name: "Original owner"})
	if err != nil {
		t.Fatal(err)
	}
	purchase, err := purchases.Create(t.Context(), cookie.Value, "correction-proof-purchase", store.PurchaseCreate{MerchantName: "Original", PurchasedAt: "2026-11-07T00:00:00Z", Currency: "JPY"})
	if err != nil {
		t.Fatal(err)
	}
	line, err := purchases.CreateLine(t.Context(), cookie.Value, "correction-proof-line", purchase.ID, purchase.Revision, store.PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, QuantityBase: 1, PaidMinor: 0})
	if err != nil {
		t.Fatal(err)
	}
	purchase, err = purchases.Get(t.Context(), cookie.Value, purchase.ID)
	if err != nil {
		t.Fatal(err)
	}
	item, err = catalog.Get(t.Context(), cookie.Value, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	return correctionPurchaseFixture{auth, handler, cookie, csrf, purchases, catalog, purchase, item, line}
}

func purchaseCorrectionVoid(f correctionPurchaseFixture) store.PurchaseLineCorrectionCreate {
	return store.PurchaseLineCorrectionCreate{LineID: f.line.ID, LineRevision: f.line.Revision, ItemRevision: f.item.Revision, Action: "void", Reason: "Exact reviewed void"}
}

func newCorrectionDisposalFixture(t *testing.T) (assetHTTPSource, store.DisposalRecord) {
	t.Helper()
	f := assetHTTPSourceFixture(t, true)
	_, err := f.assets.Dispose(t.Context(), f.cookie.Value, "correction-proof-disposal", f.unit.ID, f.asset.Revision, store.AssetDisposalWrite{UnitRevision: f.unit.Revision, Kind: "sold", DisposedAt: "2026-11-07T00:00:00Z", RealizedProceedsMinor: 200, Currency: "JPY"})
	if err != nil {
		t.Fatal(err)
	}
	f.asset, err = f.assets.Get(t.Context(), f.cookie.Value, f.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.unit, err = f.inventory.GetUnit(t.Context(), f.cookie.Value, f.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := f.assets.GetDisposalRecord(t.Context(), f.cookie.Value, f.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, record
}

func marshalCorrection(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestFinancialCorrectionsHTTPRechecksAuthorizationAfterBodyForBothActualWriters(t *testing.T) {
	t.Run("purchase", func(t *testing.T) {
		f := newCorrectionPurchaseFixture(t)
		input := purchaseCorrectionVoid(f)
		body := marshalCorrection(t, input)
		path := "/api/v1/purchases/" + f.purchase.ID + "/line-corrections"
		r := domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, "revoked-purchase-correction", fmt.Sprintf(`"%d"`, f.purchase.Revision))
		r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
			if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "revoke-correction-during-body"); err != nil {
				t.Fatal(err)
			}
		}})
		expectError(t, respond(f.handler, r), 401, "AUTHENTICATION_REQUIRED")
		fresh, csrf := testCredentials(t, f.auth, changedPassword)
		page, err := f.purchases.PageLineCorrections(t.Context(), fresh.Value, f.purchase.ID, "", 50)
		if err != nil || len(page.Data) != 0 {
			t.Fatal("revoked body-time command created correction history", err)
		}
		reply := respond(f.handler, domainRequest(http.MethodPost, path, body, fresh, csrf, "revoked-purchase-correction", fmt.Sprintf(`"%d"`, f.purchase.Revision)))
		if value := assetHTTPReply[store.PurchaseLineCorrection](t, reply, 8); value.After != nil || value.Before.ID != f.line.ID {
			t.Fatal("failed original key/proof could not be explicitly retried")
		}
	})
	t.Run("disposal", func(t *testing.T) {
		f, record := newCorrectionDisposalFixture(t)
		input := store.DisposalCorrectionWrite{AssetRevision: f.asset.Revision, Action: "void", Reason: "Exact reviewed void"}
		body := marshalCorrection(t, input)
		path := "/api/v1/units/" + f.unit.ID + "/disposal-corrections"
		r := domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, "revoked-disposal-correction", fmt.Sprintf(`"%d"`, record.Revision))
		r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
			if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "revoke-disposal-during-body"); err != nil {
				t.Fatal(err)
			}
		}})
		expectError(t, respond(f.handler, r), 401, "AUTHENTICATION_REQUIRED")
		fresh, csrf := testCredentials(t, f.auth, changedPassword)
		current, err := f.assets.GetDisposalRecord(t.Context(), fresh.Value, f.unit.ID)
		if err != nil || !reflect.DeepEqual(current, record) {
			t.Fatal("revoked body-time command changed retained disposal", err)
		}
		reply := respond(f.handler, domainRequest(http.MethodPost, path, body, fresh, csrf, "revoked-disposal-correction", fmt.Sprintf(`"%d"`, record.Revision)))
		if value := assetHTTPReply[store.DisposalCorrection](t, reply, 8); value.After.Current != nil || value.After.Original != record.Original {
			t.Fatal("failed original key/proof could not be explicitly retried")
		}
	})
}

func TestFinancialCorrectionsHTTPRechecksAuthorizationAfterReadBodyForEveryNewRead(t *testing.T) {
	for _, scope := range []string{"purchase history", "disposal record", "disposal history"} {
		t.Run(scope, func(t *testing.T) {
			var auth *store.Auth
			var handler http.Handler
			var cookie *http.Cookie
			var csrf, path string
			if scope == "purchase history" {
				f := newCorrectionPurchaseFixture(t)
				auth, handler, cookie, csrf, path = f.auth, f.handler, f.cookie, f.csrf, "/api/v1/purchases/"+f.purchase.ID+"/line-corrections"
			} else {
				f, _ := newCorrectionDisposalFixture(t)
				tail := "disposal-record"
				if scope == "disposal history" {
					tail = "disposal-corrections"
				}
				auth, handler, cookie, csrf, path = f.auth, f.handler, f.cookie, f.csrf, "/api/v1/units/"+f.unit.ID+"/"+tail
			}
			r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-correction-read-during-body"); err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
			fresh, csrf := testCredentials(t, auth, changedPassword)
			reply := respond(handler, domainRequest(http.MethodGet, path, "", fresh, csrf, "", ""))
			if reply.Code != 200 {
				t.Fatal("revocation corrupted a later authorized source read", reply.Code, reply.Body.String())
			}
		})
	}
}

func TestPurchaseCorrectionHTTPRejectsStaleIndependentLineProof(t *testing.T) {
	f := newCorrectionPurchaseFixture(t)
	input := purchaseCorrectionVoid(f)
	input.LineRevision++
	path := "/api/v1/purchases/" + f.purchase.ID + "/line-corrections"
	expectError(t, respond(f.handler, domainRequest(http.MethodPost, path, marshalCorrection(t, input), f.cookie, f.csrf, "stale-independent-line-proof", fmt.Sprintf(`"%d"`, f.purchase.Revision))), 409, "REVISION_CONFLICT")
	history, err := f.purchases.PageLineCorrections(t.Context(), f.cookie.Value, f.purchase.ID, "", 50)
	if err != nil || len(history.Data) != 0 {
		t.Fatal("stale independent Line proof posted a correction", err)
	}
}

func TestPurchaseCorrectionHTTPRechecksEachActualProofAfterBodyReading(t *testing.T) {
	for _, scope := range []string{"Purchase", "original Item", "replacement Item"} {
		t.Run(scope, func(t *testing.T) {
			f := newCorrectionPurchaseFixture(t)
			input := purchaseCorrectionVoid(f)
			var target store.Item
			if scope == "replacement Item" {
				var err error
				target, err = f.catalog.Create(t.Context(), f.cookie.Value, "correction-replacement-owner", store.ItemCreate{Name: "Replacement owner"})
				if err != nil {
					t.Fatal(err)
				}
				input.Action, input.Replacement = "replace", &store.PurchaseLineCreate{ItemID: target.ID, ItemRevision: target.Revision, QuantityBase: 1, PaidMinor: 0}
			}
			path := "/api/v1/purchases/" + f.purchase.ID + "/line-corrections"
			r := domainRequest(http.MethodPost, path, marshalCorrection(t, input), f.cookie, f.csrf, "reviewed-correction-before-body", fmt.Sprintf(`"%d"`, f.purchase.Revision))
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				var err error
				if scope == "Purchase" {
					note := "Concurrent header"
					_, err = f.purchases.Patch(t.Context(), f.cookie.Value, "changed-header-during-correction-body", f.purchase.ID, f.purchase.Revision, store.PurchasePatch{ReceiptNote: &note})
				} else {
					item := f.item
					if scope == "replacement Item" {
						item = target
					}
					name := "Concurrent Item"
					_, err = f.catalog.Patch(t.Context(), f.cookie.Value, "changed-item-during-correction-body", item.ID, item.Revision, store.ItemPatch{Name: &name})
				}
				if err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(f.handler, r), 409, "REVISION_CONFLICT")
			history, err := f.purchases.PageLineCorrections(t.Context(), f.cookie.Value, f.purchase.ID, "", 50)
			if err != nil || len(history.Data) != 0 {
				t.Fatal("stale body-time proof posted a financial correction", err)
			}
			lines, err := f.purchases.PageLines(t.Context(), f.cookie.Value, f.purchase.ID, "", 50)
			if err != nil || len(lines.Data) != 1 || !reflect.DeepEqual(lines.Data[0], f.line) {
				t.Fatal("stale body-time proof voided or replaced original attribution", err)
			}
		})
	}
}

func TestDisposalCorrectionHTTPRechecksOriginalSubjectAndAssetButNotPhysicalOrItemProofs(t *testing.T) {
	for _, scope := range []string{"subject", "Asset", "Unit metadata", "Item metadata"} {
		t.Run(scope, func(t *testing.T) {
			f, record := newCorrectionDisposalFixture(t)
			input := store.DisposalCorrectionWrite{AssetRevision: f.asset.Revision, Action: "replace", Replacement: &store.DisposalFinancialWrite{Kind: "sold", DisposedAt: "2026-11-07T00:00:00Z", RealizedProceedsMinor: 0, Currency: "JPY"}, Reason: "Exact reviewed correction"}
			path := "/api/v1/units/" + f.unit.ID + "/disposal-corrections"
			r := domainRequest(http.MethodPost, path, marshalCorrection(t, input), f.cookie, f.csrf, "reviewed-disposal-before-body", fmt.Sprintf(`"%d"`, record.Revision))
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				var err error
				switch scope {
				case "subject":
					_, err = f.assets.CorrectDisposal(t.Context(), f.cookie.Value, "changed-disposal-during-body", f.unit.ID, record.Revision, store.DisposalCorrectionWrite{AssetRevision: f.asset.Revision, Action: "void", Reason: "Concurrent void"})
				case "Asset":
					_, err = f.assets.RecordUsage(t.Context(), f.cookie.Value, "changed-asset-during-body", f.unit.ID, f.asset.Revision, store.UsageWrite{Delta: 1, UsageUnit: "times", EventType: "use", OccurredAt: "2026-11-07T00:00:00Z"})
				case "Unit metadata":
					notes := "Concurrent physical metadata"
					_, err = f.inventory.PatchUnit(t.Context(), f.cookie.Value, "changed-unit-during-body", f.unit.ID, f.unit.Revision, store.UnitPatch{Notes: &notes})
				case "Item metadata":
					item, readErr := f.catalog.Get(t.Context(), f.cookie.Value, f.item.ID)
					if readErr != nil {
						t.Fatal(readErr)
					}
					name := "Concurrent owner metadata"
					_, err = f.catalog.Patch(t.Context(), f.cookie.Value, "changed-owner-during-body", item.ID, item.Revision, store.ItemPatch{Name: &name})
				}
				if err != nil {
					t.Fatal(err)
				}
			}})
			reply := respond(f.handler, r)
			if scope == "subject" || scope == "Asset" {
				expectError(t, reply, 409, "REVISION_CONFLICT")
			} else if value := assetHTTPReply[store.DisposalCorrection](t, reply, 8); value.After.Current == nil || value.After.Current.RealizedProceedsMinor != 0 {
				t.Fatal("unchanged Unit/Item proofs became a new financial precondition")
			}
			history, err := f.assets.PageDisposalCorrections(t.Context(), f.cookie.Value, f.unit.ID, "", 50)
			if err != nil || len(history.Data) != 1 && scope != "Asset" || len(history.Data) != 0 && scope == "Asset" {
				t.Fatal("body-time proof change duplicated or omitted real correction history", scope, err)
			}
		})
	}
}

func TestFinancialCorrectionsHTTPDefaultOwnedPagesUseActualCorrectionsAndIndependentCursors(t *testing.T) {
	f := newCorrectionPurchaseFixture(t)
	for index := range 50 {
		item, err := f.catalog.Get(t.Context(), f.cookie.Value, f.item.ID)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := f.purchases.Get(t.Context(), f.cookie.Value, f.purchase.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.purchases.CreateLine(t.Context(), f.cookie.Value, fmt.Sprintf("correction-default-source-%d", index), parent.ID, parent.Revision, store.PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, QuantityBase: 1, PaidMinor: 0}); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := f.purchases.PageLines(t.Context(), f.cookie.Value, f.purchase.ID, "", 100)
	if err != nil || len(lines.Data) != 51 {
		t.Fatal("actual correction source was not complete", err)
	}
	for index, line := range lines.Data {
		item, err := f.catalog.Get(t.Context(), f.cookie.Value, f.item.ID)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := f.purchases.Get(t.Context(), f.cookie.Value, f.purchase.ID)
		if err != nil {
			t.Fatal(err)
		}
		input := store.PurchaseLineCorrectionCreate{LineID: line.ID, LineRevision: line.Revision, ItemRevision: item.Revision, Action: "void", Reason: "Reviewed actual history"}
		if _, err = f.purchases.CorrectLine(t.Context(), f.cookie.Value, fmt.Sprintf("correction-default-void-%d", index), parent.ID, parent.Revision, input); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := store.NewInventory(f.auth)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := store.NewAssets(f.auth)
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.catalog.Create(t.Context(), f.cookie.Value, "correction-default-asset-owner", store.ItemCreate{Name: "Asset owner"})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := inventory.CreateUnit(t.Context(), f.cookie.Value, "correction-default-asset-unit", item.ID, item.Revision, store.UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	item, err = f.catalog.Get(t.Context(), f.cookie.Value, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	manual := int64(100)
	asset, err := assets.PutProfile(t.Context(), f.cookie.Value, "correction-default-asset-profile", unit.ID, unit.Revision, store.AssetProfileWrite{ItemRevision: item.Revision, CostBasis: "manual", Currency: "JPY", ManualAcquisitionMinor: &manual, UsageUnit: "times"})
	if err != nil {
		t.Fatal(err)
	}
	disposal, err := assets.Dispose(t.Context(), f.cookie.Value, "correction-default-asset-disposal", unit.ID, asset.Revision, store.AssetDisposalWrite{UnitRevision: unit.Revision, Kind: "sold", DisposedAt: "2026-11-07T00:00:00Z", RealizedProceedsMinor: 0, Currency: "JPY"})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 51 {
		input := store.DisposalCorrectionWrite{AssetRevision: asset.Revision + 1 + int64(index), Action: "replace", Replacement: &store.DisposalFinancialWrite{Kind: "sold", DisposedAt: "2026-11-07T00:00:00Z", RealizedProceedsMinor: 0, Currency: "JPY"}, Reason: "Reviewed actual decision"}
		if _, err = assets.CorrectDisposal(t.Context(), f.cookie.Value, fmt.Sprintf("correction-default-finance-%d", index), unit.ID, disposal.Revision+int64(index), input); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"/api/v1/purchases/" + f.purchase.ID + "/line-corrections", "/api/v1/units/" + unit.ID + "/disposal-corrections"}
	cursors := make(map[string]string)
	for _, path := range paths {
		page := ownedPageReply(t, respond(f.handler, domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")))
		if len(page.Data) != 50 || page.NextCursor == nil {
			t.Fatal("correction collection lost its actual default 50-row page", path)
		}
		cursors[path] = *page.NextCursor
		last := ownedPageReply(t, respond(f.handler, domainRequest(http.MethodGet, path+"?cursor="+url.QueryEscape(*page.NextCursor), "", f.cookie, f.csrf, "", "")))
		if len(last.Data) != 1 || last.NextCursor != nil {
			t.Fatal("correction collection lost its actual final row", path)
		}
		expectError(t, respond(f.handler, domainRequest(http.MethodGet, path+"?limit=49&cursor="+url.QueryEscape(*page.NextCursor), "", f.cookie, f.csrf, "", "")), 400, "INVALID_CURSOR")
	}
	for _, path := range paths {
		for other, cursor := range cursors {
			if other != path {
				expectError(t, respond(f.handler, domainRequest(http.MethodGet, path+"?cursor="+url.QueryEscape(cursor), "", f.cookie, f.csrf, "", "")), 400, "INVALID_CURSOR")
			}
		}
	}
	otherOwner := strings.Replace(paths[1], unit.ID, "other-actual-identity", 1)
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, otherOwner+"?cursor="+url.QueryEscape(cursors[paths[1]]), "", f.cookie, f.csrf, "", "")), 400, "INVALID_CURSOR")
}
