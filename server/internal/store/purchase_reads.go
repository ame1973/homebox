package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

const purchaseCursorPurpose = "purchases-list-cursor-v1"

type PurchaseList = metadataList[Purchase]
type PurchaseLineList = metadataList[PurchaseLine]

// PurchaseFilters has no catalog text query. Header criteria and same-Line Item
// criteria remain separate; current placement never reconstructs purchase history.
type PurchaseFilters struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Currency   string `json:"currency"`
	CategoryID string `json:"category_id"`
	LocationID string `json:"location_id"`
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	Merchant   string `json:"merchant"`
}

func (p *Purchases) Page(ctx context.Context, token, cursor string, limit int, filters PurchaseFilters) (PurchaseList, error) {
	if limit < 1 || limit > 100 || len(cursor) > 2048 {
		return PurchaseList{}, ErrDomainInput
	}
	// Reuse strict civil-date, exact-text and scope policy without exposing q or
	// nesting another public read transaction on the private connection.
	normalized, from, to, err := normalizeItemFilters(ItemFilters{From: filters.From, To: filters.To, Currency: filters.Currency,
		CategoryID: filters.CategoryID, LocationID: filters.LocationID, Scope: filters.Scope, Status: filters.Status, Merchant: filters.Merchant})
	if err != nil {
		return PurchaseList{}, err
	}
	return readDomain(ctx, p.auth, token, func(tx *sql.Tx) (PurchaseList, error) {
		zoneName, zone, err := readHomeTimezone(ctx, tx)
		if err != nil {
			return PurchaseList{}, err
		}
		scope, err := json.Marshal(struct {
			Filters  ItemFilters `json:"filters"`
			Timezone string      `json:"timezone"`
		}{normalized, zoneName})
		if err != nil {
			return PurchaseList{}, err
		}
		return readMetadataPageTx(ctx, tx, p.auth, token, cursor, limit, purchaseCursorPurpose+":"+authDigest(string(scope)),
			func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[Purchase], error) {
				return listPurchaseHeaders(ctx, tx, after, size, normalized, "", from, to, zone)
			})
	})
}

func (p *Purchases) PageForItem(ctx context.Context, token, itemID, cursor string, limit int) (PurchaseList, error) {
	if !validDomainID(itemID) {
		return PurchaseList{}, ErrDomainInput
	}
	purpose := "item-purchases-list-cursor-v1\x00" + projectionPair("purchases", itemID)
	return readMetadataPage(ctx, p.auth, token, cursor, limit, purpose,
		func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[Purchase], error) {
			if _, err := loadItem(ctx, tx, itemID); err != nil {
				return metadataPage[Purchase]{}, err
			}
			return listPurchaseHeaders(ctx, tx, after, size, ItemFilters{Scope: "subtree"}, itemID, 0, 0, time.UTC)
		})
}

func (p *Purchases) PageLines(ctx context.Context, token, purchaseID, cursor string, limit int) (PurchaseLineList, error) {
	if !validDomainID(purchaseID) {
		return PurchaseLineList{}, ErrDomainInput
	}
	purpose := "purchase-lines-list-cursor-v1\x00" + projectionPair("lines", purchaseID)
	return readMetadataPage(ctx, p.auth, token, cursor, limit, purpose,
		func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[PurchaseLine], error) {
			if _, err := loadPurchase(ctx, tx, purchaseID); err != nil {
				return metadataPage[PurchaseLine]{}, err
			}
			rows, err := tx.QueryContext(ctx, purchaseLineSelect+" WHERE l.purchase_id=? AND l.id>? ORDER BY l.id LIMIT ?", purchaseID, after, size+1)
			if err != nil {
				return metadataPage[PurchaseLine]{}, err
			}
			defer rows.Close()
			page := metadataPage[PurchaseLine]{Data: make([]PurchaseLine, 0)}
			for rows.Next() {
				value, err := scanPurchaseLine(rows)
				if err != nil {
					return metadataPage[PurchaseLine]{}, err
				}
				if err = validatePurchaseLineAuthority(ctx, tx, value); err != nil {
					return metadataPage[PurchaseLine]{}, err
				}
				page.Data = append(page.Data, value)
			}
			if err = rows.Err(); err != nil {
				return metadataPage[PurchaseLine]{}, err
			}
			if len(page.Data) > size {
				page.Data = page.Data[:size]
				page.NextID = page.Data[size-1].ID
			}
			return page, nil
		})
}

// Build one same-Line predicate for both SQL selection and retained authority
// validation. Never combine category/status/location matches from different Items.
func purchaseLineCriteria(filters ItemFilters, itemID string) (string, []any) {
	var clauses []string
	var args []any
	if itemID != "" {
		clauses = append(clauses, "l.item_id=?")
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
	if filters.Status != "" {
		clauses = append(clauses, "i.status=?")
		args = append(args, filters.Status)
	}
	if filters.LocationID != "" {
		ids := "SELECT id FROM filter_locations"
		if filters.Scope == "direct" {
			ids += " WHERE id=?"
			args = append(args, filters.LocationID, filters.LocationID, filters.LocationID)
		}
		clauses = append(clauses, `((l.item_unit_id IS NOT NULL AND EXISTS(SELECT 1 FROM item_units u
 WHERE u.id=l.item_unit_id AND u.item_id=i.id AND u.status<>'disposed' AND u.location_id IN(`+ids+`)))
 OR (l.item_unit_id IS NULL AND ((i.tracking_mode='individual' AND EXISTS(SELECT 1 FROM item_units u
 WHERE u.item_id=i.id AND u.status<>'disposed' AND u.location_id IN(`+ids+`)))
 OR (i.tracking_mode='quantity' AND EXISTS(SELECT 1 FROM stock_balances b
 WHERE b.item_id=i.id AND b.quantity_base>0 AND b.location_id IN(`+ids+`))))))`)
	}
	return strings.Join(clauses, " AND "), args
}

func listPurchaseHeaders(ctx context.Context, tx *sql.Tx, after string, limit int, filters ItemFilters, itemID string, from, to int, zone *time.Location) (metadataPage[Purchase], error) {
	clauses, args := []string{"p.id>?"}, []any{filters.CategoryID, filters.LocationID, after}
	if filters.Currency != "" {
		clauses = append(clauses, "p.currency=?")
		args = append(args, filters.Currency)
	}
	if filters.Merchant != "" {
		clauses = append(clauses, "p.merchant_name=?")
		args = append(args, filters.Merchant)
	}
	criteria, lineArgs := purchaseLineCriteria(filters, itemID)
	if criteria != "" {
		clauses = append(clauses, "EXISTS(SELECT 1 FROM purchase_lines l JOIN items i ON i.id=l.item_id WHERE l.purchase_id=p.id AND "+criteria+")")
		args = append(args, lineArgs...)
	}
	rows, err := tx.QueryContext(ctx, itemFilterCTEs+purchaseSelect+" WHERE "+strings.Join(clauses, " AND ")+" ORDER BY p.id", args...)
	if err != nil {
		return metadataPage[Purchase]{}, err
	}
	defer rows.Close()
	page := metadataPage[Purchase]{Data: make([]Purchase, 0)}
	// ponytail: stream civil-date candidates until a validated matching lookahead;
	// add a timezone-keyed date index only if measured household latency needs it.
	for rows.Next() {
		value, err := scanPurchase(rows)
		if err != nil {
			return metadataPage[Purchase]{}, err
		}
		if from != 0 || to != 0 {
			instant, _ := time.Parse(time.RFC3339Nano, value.PurchasedAt)
			day := civilDateKey(instant.In(zone))
			if from != 0 && day < from || to != 0 && day > to {
				continue
			}
		}
		if criteria != "" {
			if err = validatePurchaseCriteria(ctx, tx, value.ID, filters, criteria, lineArgs); err != nil {
				return metadataPage[Purchase]{}, err
			}
		}
		page.Data = append(page.Data, value)
		if len(page.Data) > limit {
			page.Data = page.Data[:limit]
			page.NextID = page.Data[limit-1].ID
			return page, nil
		}
	}
	if err = rows.Err(); err != nil {
		return metadataPage[Purchase]{}, err
	}
	return page, nil
}

func validatePurchaseCriteria(ctx context.Context, tx *sql.Tx, purchaseID string, filters ItemFilters, criteria string, lineArgs []any) error {
	args := append([]any{filters.CategoryID, filters.LocationID, purchaseID}, lineArgs...)
	rows, err := tx.QueryContext(ctx, itemFilterCTEs+purchaseLineSelect+" JOIN items i ON i.id=l.item_id WHERE l.purchase_id=? AND "+criteria+" ORDER BY l.id", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		line, err := scanPurchaseLine(rows)
		if err != nil {
			return err
		}
		if err = validatePurchaseLineAuthority(ctx, tx, line); err != nil {
			return err
		}
		found = true
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !found {
		return ErrDomainState
	}
	return nil
}
