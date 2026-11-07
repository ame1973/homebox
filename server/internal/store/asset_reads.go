package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

const assetCursorPurpose = "assets-list-cursor-v1"

type AssetList = metadataList[Asset]
type UsageList = metadataList[Usage]
type CostList = metadataList[Cost]

// Currency selects the profile; merchant/dates select one purchase naming this
// exact Unit. Neither sibling nor unallocated Item spend establishes a match.
type AssetFilters struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Currency   string `json:"currency"`
	CategoryID string `json:"category_id"`
	LocationID string `json:"location_id"`
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	Merchant   string `json:"merchant"`
}

func (a *Assets) Page(ctx context.Context, token, cursor string, limit int, filters AssetFilters) (AssetList, error) {
	if limit < 1 || limit > 100 || len(cursor) > 2048 {
		return AssetList{}, ErrDomainInput
	}
	normalized, from, to, err := normalizeItemFilters(ItemFilters{From: filters.From, To: filters.To, Currency: filters.Currency,
		CategoryID: filters.CategoryID, LocationID: filters.LocationID, Scope: filters.Scope, Status: filters.Status, Merchant: filters.Merchant})
	if err != nil {
		return AssetList{}, err
	}
	return readDomain(ctx, a.auth, token, func(tx *sql.Tx) (AssetList, error) {
		zoneName, zone, err := readHomeTimezone(ctx, tx)
		if err != nil {
			return AssetList{}, err
		}
		scope, err := json.Marshal(struct {
			Filters  ItemFilters `json:"filters"`
			Timezone string      `json:"timezone"`
		}{normalized, zoneName})
		if err != nil {
			return AssetList{}, err
		}
		return readMetadataPageTx(ctx, tx, a.auth, token, cursor, limit, assetCursorPurpose+":"+authDigest(string(scope)),
			func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[Asset], error) {
				return listAssets(ctx, tx, after, size, normalized, from, to, zone)
			})
	})
}

func listAssets(ctx context.Context, tx *sql.Tx, after string, limit int, filters ItemFilters, from, to int, zone *time.Location) (metadataPage[Asset], error) {
	clauses, args := []string{"a.item_unit_id>?"}, []any{filters.CategoryID, filters.LocationID, after}
	if filters.Currency != "" {
		clauses = append(clauses, "a.currency=?")
		args = append(args, filters.Currency)
	}
	if filters.CategoryID != "" {
		ids := "SELECT id FROM filter_categories"
		if filters.Scope == "direct" {
			ids += " WHERE id=?"
			args = append(args, filters.CategoryID)
		}
		clauses = append(clauses, "i.category_id IN("+ids+")")
	}
	if filters.LocationID != "" {
		ids := "SELECT id FROM filter_locations"
		if filters.Scope == "direct" {
			ids += " WHERE id=?"
			args = append(args, filters.LocationID)
		}
		clauses = append(clauses, "u.status<>'disposed' AND u.location_id IN("+ids+")")
	}
	if filters.Status != "" {
		clauses = append(clauses, "i.status=?")
		args = append(args, filters.Status)
	}
	rows, err := tx.QueryContext(ctx, itemFilterCTEs+`SELECT a.item_unit_id FROM asset_profiles a
 LEFT JOIN item_units u ON u.id=a.item_unit_id LEFT JOIN items i ON i.id=u.item_id
 WHERE `+strings.Join(clauses, " AND ")+" ORDER BY a.item_unit_id", args...)
	if err != nil {
		return metadataPage[Asset]{}, err
	}
	defer rows.Close()
	page := metadataPage[Asset]{Data: make([]Asset, 0)}
	// ponytail: stream civil-date candidates until validated matching lookahead;
	// add a timezone-keyed date index only if measured household latency needs it.
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return metadataPage[Asset]{}, err
		}
		value, err := loadAsset(ctx, tx, id)
		if err != nil {
			return metadataPage[Asset]{}, err
		}
		matched, err := assetMatchesPurchase(ctx, tx, id, filters, from, to, zone)
		if err != nil {
			return metadataPage[Asset]{}, err
		}
		if !matched {
			continue
		}
		page.Data = append(page.Data, value)
		if len(page.Data) > limit {
			page.Data = page.Data[:limit]
			page.NextID = page.Data[limit-1].ItemUnitID
			return page, nil
		}
	}
	if err = rows.Err(); err != nil {
		return metadataPage[Asset]{}, err
	}
	return page, nil
}

func assetMatchesPurchase(ctx context.Context, tx *sql.Tx, unitID string, filters ItemFilters, from, to int, zone *time.Location) (bool, error) {
	if from == 0 && to == 0 && filters.Merchant == "" {
		return true, nil
	}
	condition, args := "l.item_unit_id=?", []any{unitID}
	if filters.Merchant != "" {
		condition += " AND p.merchant_name=?"
		args = append(args, filters.Merchant)
	}
	rows, err := tx.QueryContext(ctx, purchaseLineSelect+" LEFT JOIN purchases p ON p.id=l.purchase_id WHERE "+condition+" ORDER BY l.id", args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	matched := false
	for rows.Next() {
		line, err := scanPurchaseLine(rows)
		if err != nil {
			return false, err
		}
		purchase, err := loadPurchaseLineAuthority(ctx, tx, line)
		if err != nil {
			return false, err
		}
		if line.ItemUnitID == nil || *line.ItemUnitID != unitID {
			return false, ErrDomainState
		}
		active, err := purchaseLineIsActive(ctx, tx, line)
		if err != nil {
			return false, err
		}
		if !active {
			continue
		}
		instant, _ := time.Parse(time.RFC3339Nano, purchase.PurchasedAt)
		day := civilDateKey(instant.In(zone))
		if (from == 0 || day >= from) && (to == 0 || day <= to) {
			matched = true
		}
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	return matched, nil
}

func (a *Assets) PageUsage(ctx context.Context, token, unitID, cursor string, limit int) (UsageList, error) {
	return readAssetEventsPage(ctx, a.auth, token, unitID, cursor, limit, "usage", "u", usageSelect, scanUsage)
}

func (a *Assets) PageCosts(ctx context.Context, token, unitID, cursor string, limit int) (CostList, error) {
	return readAssetEventsPage(ctx, a.auth, token, unitID, cursor, limit, "costs", "c", costSelect, scanCost)
}

func readAssetEventsPage[T interface{ correctionRef() assetCorrectionRef }](ctx context.Context, auth *Auth, token, unitID, cursor string, limit int,
	kind, alias, query string, scan func(rowScanner) (T, error),
) (metadataList[T], error) {
	if !validDomainID(unitID) {
		return metadataList[T]{}, ErrDomainInput
	}
	purpose := "asset-events-list-cursor-v1\x00" + projectionPair(kind, unitID)
	return readMetadataPage(ctx, auth, token, cursor, limit, purpose, func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[T], error) {
		if _, err := loadAsset(ctx, tx, unitID); err != nil {
			return metadataPage[T]{}, err
		}
		rows, err := tx.QueryContext(ctx, query+" WHERE "+alias+".item_unit_id=? AND "+alias+".id>? ORDER BY "+alias+".id LIMIT ?", unitID, after, size+1)
		if err != nil {
			return metadataPage[T]{}, err
		}
		defer rows.Close()
		page := metadataPage[T]{Data: make([]T, 0)}
		verified := make(map[string]bool)
		for rows.Next() {
			value, err := scan(rows)
			if err != nil {
				return metadataPage[T]{}, err
			}
			if value.correctionRef().owner != unitID {
				return metadataPage[T]{}, ErrDomainState
			}
			if err = validateAssetCorrection(value.correctionRef(), func(id string) (assetCorrectionRef, error) {
				target, err := scan(tx.QueryRowContext(ctx, query+" WHERE "+alias+".id=?", id))
				return target.correctionRef(), err
			}, verified); err != nil {
				return metadataPage[T]{}, err
			}
			page.Data = append(page.Data, value)
		}
		if err = rows.Err(); err != nil {
			return metadataPage[T]{}, err
		}
		if len(page.Data) > size {
			page.Data = page.Data[:size]
			page.NextID = page.Data[size-1].correctionRef().id
		}
		return page, nil
	})
}
