package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

var testSyncCurrentKinds = []string{
	"location", "location_event", "category", "tag", "item", "unit", "stock", "barcode", "alias", "attribute",
	"item_tag", "inventory_event", "search_doc", "purchase", "purchase_line", "asset", "usage", "cost", "disposal",
	"media", "media_link", "ai_review",
}

// Media and review records are synthetic SQL claims; no files or AI workers are used.
// The other projection kinds are produced through the real domain mutation paths.
func syncAllCoverageFixture(t *testing.T) (*Store, *Auth, string) {
	t.Helper()
	s, catalog, auth, token := catalogFixture(t)
	locations, err := NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	purchases, err := NewPurchases(auth)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := NewAssets(auth)
	if err != nil {
		t.Fatal(err)
	}
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	root := createLocation(t, locations, token, "ALL-COVERAGE-ROOT", nil)
	child := createLocation(t, locations, token, "ALL-COVERAGE-CHILD", nil)
	if _, err = locations.Move(t.Context(), token, "all-coverage-location", child.ID, child.Revision, &root.ID); err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, token, "Coverage category", nil)
	tag := createTag(t, taxonomy, token, "Coverage tag")
	individual := createCatalogItem(t, catalog, token, ItemCreate{Name: "Coverage individual", CategoryID: &category.ID})
	if _, err = catalog.CreateBarcode(t.Context(), token, "all-coverage-barcode", individual.ID, individual.Revision, BarcodeWrite{"OTHER", "Shared code"}); err != nil {
		t.Fatal(err)
	}
	owner := getCatalogItem(t, catalog, token, individual.ID)
	if _, err = catalog.CreateAlias(t.Context(), token, "all-coverage-alias", individual.ID, owner.Revision, "Coverage alias"); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	if _, err = catalog.CreateAttribute(t.Context(), token, "all-coverage-attribute", individual.ID, owner.Revision, AttributeWrite{AttributeKey: "Color", AttributeValue: "Blue"}); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	if _, err = catalog.ReplaceTags(t.Context(), token, "all-coverage-tag", individual.ID, owner.Revision, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	unit, err := inventory.CreateUnit(t.Context(), token, "all-coverage-unit", individual.ID, owner.Revision, UnitCreate{LocationID: &child.ID})
	if err != nil {
		t.Fatal(err)
	}
	quantity := createCatalogItem(t, catalog, token, ItemCreate{Name: "Coverage quantity", TrackingMode: testPointer("quantity"), UnitCode: testPointer("kg"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000))})
	if _, err = inventory.Receive(t.Context(), token, "all-coverage-stock", quantity.Revision, StockReceive{ItemID: quantity.ID, TargetLocationID: child.ID, QuantityBase: 1234}); err != nil {
		t.Fatal(err)
	}
	purchase := createPurchaseCommand(t, purchases, token, "all-coverage-purchase", "JPY")
	quantity = getCatalogItem(t, catalog, token, quantity.ID)
	if _, err = purchases.CreateLine(t.Context(), token, "all-coverage-line", purchase.ID, purchase.Revision, PurchaseLineCreate{ItemID: quantity.ID, ItemRevision: quantity.Revision, QuantityBase: 1234, PaidMinor: 100}); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	profile, err := assets.PutProfile(t.Context(), token, "all-coverage-profile", unit.ID, unit.Revision, manualProfileInput(owner.Revision, 100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = assets.RecordUsage(t.Context(), token, "all-coverage-usage", unit.ID, profile.Revision, usageInput(2)); err != nil {
		t.Fatal(err)
	}
	if _, err = assets.RecordCost(t.Context(), token, "all-coverage-cost", unit.ID, profile.Revision+1, costInput(30)); err != nil {
		t.Fatal(err)
	}
	if _, err = assets.Dispose(t.Context(), token, "all-coverage-disposal", unit.ID, profile.Revision+2, disposalInput(unit.Revision, 25)); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at)
 VALUES('all-coverage-media','claim',?,'image/png',10,10,100,'2026')`, strings.Repeat("a", 64))
	seedMediaProjection(t, s, "all-coverage-media")
	owner = getCatalogItem(t, catalog, token, individual.ID)
	if _, err = library.Attach(t.Context(), token, "all-coverage-link", "all-coverage-media", 1, MediaLinkCreate{ItemID: &individual.ID, OwnerRevision: owner.Revision}); err != nil {
		t.Fatal(err)
	}
	seedSyncReviewSource(t, s, individual.ID, "all-coverage-review", "keyword", "needs_review", "PrivateUnreviewed", nil, nil, testPointer("all-coverage-media"), nil)
	return s, auth, token
}

func TestSyncCurrentCoverageChecksAllKindsWithoutWriting(t *testing.T) {
	s, auth, token := syncAllCoverageFixture(t)
	before := domainState(t, s)
	var total int64
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		var err error
		total, err = verifySyncCurrentCoverage(t.Context(), tx)
		if err != nil {
			t.Fatal("valid all-kind source was rejected", err)
		}
	})
	var actual int64
	for _, kind := range testSyncCurrentKinds {
		count := scalar[int64](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type=?", kind)
		if count == 0 {
			t.Fatal("fixture does not contain the expected projection kind", kind)
		}
		actual += count
	}
	if len(testSyncCurrentKinds) != 22 || total != actual || actual != scalar[int64](t, s.db, "SELECT COUNT(*) FROM current_projections") {
		t.Fatal("all-kind source count does not match actual current rows", total, actual)
	}
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("source observation enabled public synchronization", err)
	}
}

func TestSyncCurrentCoverageRejectsSourceMismatchInEveryKind(t *testing.T) {
	s, _, _ := syncAllCoverageFixture(t)
	before := domainState(t, s)
	for _, kind := range testSyncCurrentKinds {
		t.Run(kind, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var id, original string
			var revision int64
			if err = tx.QueryRowContext(t.Context(), "SELECT entity_id,entity_revision,payload_json FROM current_projections WHERE entity_type=? ORDER BY entity_id LIMIT 1", kind).Scan(&id, &revision, &original); err != nil {
				t.Fatal(err)
			}
			if kind == "item_tag" {
				parts, ok := projectionPairParts(id)
				if !ok {
					t.Fatal("fixture has invalid membership identity")
				}
				execSyncCoverageSQL(t, tx, "DELETE FROM item_tags WHERE item_id=? AND tag_id=?", parts[0], parts[1])
			} else {
				var payload []byte
				if kind == "ai_review" {
					var fields map[string]any
					if err = json.Unmarshal([]byte(original), &fields); err != nil {
						t.Fatal(err)
					}
					fields["state"], fields["effective_value"] = "accepted", "PrivateUnreviewed"
					payload, err = json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					payload = differentProjectionPayload(t, kind, []byte(original))
				}
				if err = validateSyncRecord(kind, id, "upsert", revision, 1, payload); err != nil {
					t.Fatal("fixture did not produce a schema-valid mismatch", kind, err)
				}
				execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type=? AND entity_id=?", string(payload), kind, id)
			}
			count, err := verifySyncCurrentCoverage(t.Context(), tx)
			if err == nil || count != 0 {
				t.Fatal("schema-valid source mismatch returned coverage", kind, count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncCurrentCoverageRejectsMissingAndUnknownKinds(t *testing.T) {
	s, _, _ := syncAllCoverageFixture(t)
	before := domainState(t, s)
	for _, kind := range testSyncCurrentKinds {
		t.Run("missing/"+kind, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=(SELECT entity_id FROM current_projections WHERE entity_type=? ORDER BY entity_id LIMIT 1)", kind, kind)
			count, err := verifySyncCurrentCoverage(t.Context(), tx)
			if err == nil || count != 0 {
				t.Fatal("missing current row returned coverage", kind, count, err)
			}
		})
	}
	for _, kind := range []string{"future_kind", "", strings.Repeat("x", 33), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("unknown-bytes-%d", len(kind)), func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?, 'unknown',1)", kind)
			execSyncCoverageSQL(t, tx, "INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?, 'unknown',1,'{}','2026')", kind)
			count, err := verifySyncCurrentCoverage(t.Context(), tx)
			if !errors.Is(err, ErrDomainState) || count != 0 {
				t.Fatal("unknown projection kind returned coverage", count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncCurrentCoverageCountsOnlyPresentDecisionsAndRetainsZeroStock(t *testing.T) {
	s, auth, token := syncAllCoverageFixture(t)
	purchases, err := NewPurchases(auth)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := NewAssets(auth)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	locations, err := NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	lineID := scalar[string](t, s.db, "SELECT id FROM purchase_lines LIMIT 1")
	line, err := loadPurchaseLine(t.Context(), s.db, lineID)
	if err != nil {
		t.Fatal(err)
	}
	purchase, err := loadPurchase(t.Context(), s.db, line.PurchaseID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := loadItem(t.Context(), s.db, line.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = purchases.CorrectLine(t.Context(), token, "all-coverage-void-line", purchase.ID, purchase.Revision, PurchaseLineCorrectionCreate{
		LineID: line.ID, LineRevision: line.Revision, ItemRevision: item.Revision, Action: "void", Reason: "Reviewed void",
	}); err != nil {
		t.Fatal(err)
	}
	unitID := scalar[string](t, s.db, "SELECT item_unit_id FROM asset_disposals LIMIT 1")
	asset, err := loadAsset(t.Context(), s.db, unitID)
	if err != nil {
		t.Fatal(err)
	}
	disposal, err := loadDisposalRecord(t.Context(), s.db, unitID)
	if err != nil {
		t.Fatal(err)
	}
	correction := disposalCorrectionInput(asset.Revision, 0)
	correction.Action, correction.Replacement = "void", nil
	if _, err = assets.CorrectDisposal(t.Context(), token, "all-coverage-void-disposal", unitID, disposal.Revision, correction); err != nil {
		t.Fatal(err)
	}
	item, err = loadItem(t.Context(), s.db, line.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	locationID := scalar[string](t, s.db, "SELECT location_id FROM stock_balances WHERE item_id=? LIMIT 1", item.ID)
	if _, err = inventory.Adjust(t.Context(), token, "all-coverage-zero-stock", item.Revision, StockAdjust{
		ItemID: item.ID, TargetLocationID: locationID, QuantityBase: 0, Reason: "Reviewed zero balance",
	}); err != nil {
		t.Fatal(err)
	}
	removed := createLocation(t, locations, token, "ALL-COVERAGE-REMOVED", nil)
	if _, err = locations.Delete(t.Context(), token, "all-coverage-delete-location", removed.ID, removed.Revision); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_line_corrections WHERE action='void'") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_disposal_corrections WHERE action='void'") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM stock_balances WHERE quantity_base=0") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM locations WHERE deleted_at IS NOT NULL") != 1 {
		t.Fatal("fixture did not preserve voided, zero and deleted source")
	}
	before := domainState(t, s)
	actual := scalar[int64](t, s.db, "SELECT COUNT(*) FROM current_projections")
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncCurrentCoverage(t.Context(), tx)
		if err != nil || count != actual {
			t.Fatal("retained voids, deletion or zero Stock changed the current total", count, actual, err)
		}
	})
	requireDomainState(t, s, before)
}

func TestSyncCurrentCoverageEmptyAndInvalidContext(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncCurrentCoverage(t.Context(), tx); err != nil || count != 0 {
		t.Fatal("empty dataset failed coverage", count, err)
	}
	if count, err := verifySyncCurrentCoverage(nil, tx); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil context returned coverage", count, err)
	}
	if count, err := verifySyncCurrentCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil transaction returned coverage", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if count, err := verifySyncCurrentCoverage(ctx, tx); !errors.Is(err, context.Canceled) || count != 0 {
		t.Fatal("canceled context returned coverage", count, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncCurrentCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || count != 0 {
		t.Fatal("closed transaction returned coverage", count, err)
	}
}
