package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

const stockCursorPurpose = "inventory-stock-list-cursor-v1"
const stockCursorBytes = 4096

type UnitList = metadataList[Unit]
type StockList = metadataList[Stock]

// StockFilters deliberately excludes catalog text search. Dates/currency/merchant
// refer to one associated purchase; location scope applies to each actual balance.
type StockFilters struct {
	ItemID     string `json:"item_id,omitempty"`
	From       string `json:"from"`
	To         string `json:"to"`
	Currency   string `json:"currency"`
	CategoryID string `json:"category_id"`
	LocationID string `json:"location_id"`
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	Merchant   string `json:"merchant"`
}

func (inventory *Inventory) GetUnit(ctx context.Context, token, id string) (Unit, error) {
	if !validDomainID(id) {
		return Unit{}, ErrDomainInput
	}
	return readDomain(ctx, inventory.auth, token, func(tx *sql.Tx) (Unit, error) { return loadUnit(ctx, tx, id) })
}

func (inventory *Inventory) PageUnits(ctx context.Context, token, itemID, cursor string, limit int) (UnitList, error) {
	if !validDomainID(itemID) {
		return UnitList{}, ErrDomainInput
	}
	purpose := "item-units-list-cursor-v1\x00" + projectionPair("units", itemID)
	return readMetadataPage(ctx, inventory.auth, token, cursor, limit, purpose, func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[Unit], error) {
		if _, err := loadItem(ctx, tx, itemID); err != nil {
			return metadataPage[Unit]{}, err
		}
		rows, err := tx.QueryContext(ctx, unitSelect+" WHERE u.item_id=? AND u.id>? ORDER BY u.id LIMIT ?", itemID, after, size+1)
		if err != nil {
			return metadataPage[Unit]{}, err
		}
		defer rows.Close()
		page := metadataPage[Unit]{Data: make([]Unit, 0)}
		for rows.Next() {
			value, err := scanUnit(rows)
			if err != nil {
				return metadataPage[Unit]{}, err
			}
			page.Data = append(page.Data, value)
		}
		if err = rows.Err(); err != nil {
			return metadataPage[Unit]{}, err
		}
		if len(page.Data) > size {
			page.Data = page.Data[:size]
			page.NextID = page.Data[size-1].ID
		}
		return page, nil
	})
}

func (inventory *Inventory) PageStock(ctx context.Context, token, cursor string, limit int, filters StockFilters) (StockList, error) {
	if limit < 1 || limit > 100 || len(cursor) > stockCursorBytes || filters.ItemID != "" && !validDomainID(filters.ItemID) {
		return StockList{}, ErrDomainInput
	}
	// Reuse the catalog's strict civil-date/Unicode/default policy without adding
	// its uncontracted q field or nesting a public catalog read inside this one.
	normalized, from, to, err := normalizeItemFilters(ItemFilters{From: filters.From, To: filters.To, Currency: filters.Currency,
		CategoryID: filters.CategoryID, LocationID: filters.LocationID, Scope: filters.Scope, Status: filters.Status, Merchant: filters.Merchant})
	if err != nil {
		return StockList{}, err
	}
	return readDomain(ctx, inventory.auth, token, func(tx *sql.Tx) (StockList, error) {
		zoneName, zone, err := readHomeTimezone(ctx, tx)
		if err != nil {
			return StockList{}, err
		}
		// Omit an absent Item filter to preserve existing unfiltered cursor MACs.
		scope, err := json.Marshal(struct {
			Filters  ItemFilters `json:"filters"`
			Timezone string      `json:"timezone"`
			ItemID   string      `json:"item_id,omitempty"`
		}{normalized, zoneName, filters.ItemID})
		if err != nil {
			return StockList{}, err
		}
		return readMetadataIdentityPageTx(ctx, tx, inventory.auth, token, cursor, limit, stockCursorPurpose+":"+authDigest(string(scope)), stockCursorBytes,
			func(id string) bool { return validProjectionIdentity("stock", id) },
			func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[Stock], error) {
				return listFilteredStock(ctx, tx, after, size, normalized, filters.ItemID, from, to, zone)
			})
	})
}

func listFilteredStock(ctx context.Context, tx *sql.Tx, after string, limit int, filters ItemFilters, itemID string, from, to int, zone *time.Location) (metadataPage[Stock], error) {
	afterItem, afterLocation := "", ""
	if after != "" {
		parts, valid := projectionPairParts(after)
		if !valid {
			return metadataPage[Stock]{}, ErrInvalidCursor
		}
		afterItem, afterLocation = parts[0], parts[1]
	}
	clauses, args := []string{"(b.item_id,b.location_id)>(?,?)"}, []any{filters.CategoryID, filters.LocationID, afterItem, afterLocation}
	if itemID != "" {
		clauses = append(clauses, "b.item_id=?")
		args = append(args, itemID)
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
		clauses = append(clauses, "b.location_id IN("+ids+")")
	}
	if filters.Status != "" {
		clauses = append(clauses, "i.status=?")
		args = append(args, filters.Status)
	}
	rows, err := tx.QueryContext(ctx, itemFilterCTEs+stockSelect+" WHERE "+strings.Join(clauses, " AND ")+" ORDER BY b.item_id,b.location_id", args...)
	if err != nil {
		return metadataPage[Stock]{}, err
	}
	defer rows.Close()
	page := metadataPage[Stock]{Data: make([]Stock, 0)}
	// As with catalog reads, scan candidates until a validated matching lookahead
	// exists. Filtering a raw SQL first page would silently lose later matches.
	for rows.Next() {
		value, err := scanStock(rows)
		if err != nil {
			return metadataPage[Stock]{}, err
		}
		matched, err := itemMatchesPurchase(ctx, tx, value.ItemID, filters, from, to, zone)
		if err != nil {
			return metadataPage[Stock]{}, err
		}
		if !matched {
			continue
		}
		page.Data = append(page.Data, value)
		if len(page.Data) > limit {
			page.Data = page.Data[:limit]
			last := page.Data[limit-1]
			page.NextID = projectionPair(last.ItemID, last.LocationID)
			return page, nil
		}
	}
	if err = rows.Err(); err != nil {
		return metadataPage[Stock]{}, err
	}
	return page, nil
}
