package store

import (
	"context"
	"database/sql"
	"errors"
)

// Observe all version-one current kinds in one caller-owned transaction. This
// is not authorization, historical provenance, retention or a snapshot seal.
func verifySyncCurrentCoverage(ctx context.Context, tx *sql.Tx) (int64, error) {
	if ctx == nil || tx == nil {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	locations, err := verifySyncLocationCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	locationEvents, err := verifySyncLocationEventCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	taxonomy, err := verifySyncTaxonomyCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	items, err := verifySyncItemCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	units, err := verifySyncUnitCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	stock, err := verifySyncStockCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	owned, err := verifySyncOwnedCatalogCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	memberships, err := verifySyncItemTagCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	inventoryEvents, err := verifySyncInventoryEventCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	search, err := verifySyncApprovedSearchCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	purchases, err := verifySyncPurchaseCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	assets, err := verifySyncAssetCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	disposals, err := verifySyncDisposalCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	media, err := verifySyncMediaMetadataCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	reviews, err := verifySyncReviewCoverage(ctx, tx)
	if err != nil {
		return 0, err
	}
	if search.Items != items || search.Units != units || search.Items > maxSafeInteger-search.Units ||
		stock.Nonzero > maxSafeInteger-stock.Zero {
		return 0, ErrDomainState
	}
	expected := map[string]int64{
		"location": locations.Live, "location_event": locationEvents, "category": taxonomy.Categories, "tag": taxonomy.Tags,
		"item": items, "unit": units, "stock": stock.Nonzero + stock.Zero,
		"barcode": owned.Barcodes, "alias": owned.Aliases, "attribute": owned.Attributes, "item_tag": memberships,
		"inventory_event": inventoryEvents, "search_doc": search.Items + search.Units,
		"purchase": purchases.Headers, "purchase_line": purchases.ActiveLines,
		"asset": assets.Profiles, "usage": assets.Usage, "cost": assets.Costs, "disposal": disposals.Active,
		"media": media.Media, "media_link": media.Links, "ai_review": reviews,
	}
	var total int64
	for _, count := range expected {
		if count < 0 || count > maxSafeInteger-total {
			return 0, ErrDomainState
		}
		total += count
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(entity_type AS BLOB))<=32 THEN entity_type END,COUNT(*)
 FROM current_projections GROUP BY entity_type`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var kind sql.NullString
		var count int64
		if err = rows.Scan(&kind, &count); err != nil {
			return 0, errors.Join(err, rows.Close())
		}
		want, known := expected[kind.String]
		if !kind.Valid || !known || count < 1 || count != want {
			return 0, errors.Join(ErrDomainState, rows.Close())
		}
		delete(expected, kind.String)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	for _, count := range expected {
		if count != 0 {
			return 0, ErrDomainState
		}
	}
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	return total, nil
}
