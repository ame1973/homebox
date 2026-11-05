package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const itemCursorPurpose = "items-list-cursor-v1"

// time.Parse also accepts non-RFC spellings; financial date filtering must not
// silently turn an unsupported legacy instant into a guessed civil date.
var purchaseInstantSyntax = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

// ItemFilters preserves every contracted list filter. Empty optional strings are
// absent; only query text is trimmed/lowercased, never stored metadata or IDs.
type ItemFilters struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Currency   string `json:"currency"`
	CategoryID string `json:"category_id"`
	LocationID string `json:"location_id"`
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	Merchant   string `json:"merchant"`
	Query      string `json:"q"`
}

type ItemList = metadataList[Item]

func (catalog *Catalog) Page(ctx context.Context, token, cursor string, limit int, filters ItemFilters) (ItemList, error) {
	if limit < 1 || limit > 100 || len(cursor) > 2048 {
		return ItemList{}, ErrDomainInput
	}
	filters, from, to, err := normalizeItemFilters(filters)
	if err != nil {
		return ItemList{}, err
	}
	return readDomain(ctx, catalog.auth, token, func(tx *sql.Tx) (ItemList, error) {
		zoneName, zone, err := readHomeTimezone(ctx, tx)
		if err != nil {
			return ItemList{}, err
		}
		scope, err := json.Marshal(struct {
			ItemFilters
			Timezone string `json:"timezone"`
		}{filters, zoneName})
		if err != nil {
			return ItemList{}, err
		}
		// Bind all effective filters/settings through an independent MAC purpose,
		// preserving the existing location/category/tag cursor wire spelling.
		purpose := itemCursorPurpose + ":" + authDigest(string(scope))
		return readMetadataPageTx(ctx, tx, catalog.auth, token, cursor, limit, purpose,
			func(ctx context.Context, tx *sql.Tx, afterID string, limit int) (ItemPage, error) {
				return listFilteredItems(ctx, tx, afterID, limit, filters, from, to, zone)
			})
	})
}

func normalizeItemFilters(filters ItemFilters) (ItemFilters, int, int, error) {
	if filters.Scope == "" {
		filters.Scope = "subtree"
	}
	if (filters.Scope != "direct" && filters.Scope != "subtree") ||
		(filters.Status != "" && filters.Status != "active" && filters.Status != "archived") ||
		(filters.CategoryID != "" && !validDomainID(filters.CategoryID)) || (filters.LocationID != "" && !validDomainID(filters.LocationID)) ||
		!authString(filters.Merchant, 0, 300) || !authString(filters.Query, 0, 500) {
		return ItemFilters{}, 0, 0, ErrDomainInput
	}
	if filters.Currency != "" && (len(filters.Currency) != 3 || strings.IndexFunc(filters.Currency, func(value rune) bool { return value < 'A' || value > 'Z' }) >= 0) {
		return ItemFilters{}, 0, 0, ErrDomainInput
	}
	parseDate := func(value string) (int, error) {
		if value == "" {
			return 0, nil
		}
		day, err := time.Parse(time.DateOnly, value)
		if err != nil || day.Year() < 1 || day.Year() > 9999 || day.Format(time.DateOnly) != value {
			return 0, ErrDomainInput
		}
		return civilDateKey(day), nil
	}
	from, err := parseDate(filters.From)
	if err != nil {
		return ItemFilters{}, 0, 0, err
	}
	to, err := parseDate(filters.To)
	if err != nil || from != 0 && to != 0 && from > to {
		return ItemFilters{}, 0, 0, ErrDomainInput
	}
	filters.Query = strings.ToLower(strings.TrimSpace(filters.Query))
	return filters, from, to, nil
}

// These integers are ordered date keys, not elapsed-day or duration arithmetic.
func civilDateKey(value time.Time) int {
	year, month, day := value.Date()
	return year*10000 + int(month)*100 + day
}

func readHomeTimezone(ctx context.Context, query queryer) (string, *time.Location, error) {
	var encoded string
	err := query.QueryRowContext(ctx, "SELECT value_json FROM app_settings WHERE key='preferences.timezone'").Scan(&encoded)
	name := "UTC"
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	if err == nil {
		var configured *string
		if json.Unmarshal([]byte(encoded), &configured) != nil || configured == nil || !authString(*configured, 1, 100) || *configured == "Local" {
			return "", nil, ErrDomainState
		}
		name = *configured
	}
	zone, err := time.LoadLocation(name)
	if err != nil {
		return "", nil, ErrDomainState
	}
	return name, zone, nil
}

const itemFilterCTEs = `WITH RECURSIVE filter_categories(id) AS (
 SELECT id FROM categories WHERE id=?
 UNION SELECT c.id FROM categories c JOIN filter_categories p ON c.parent_id=p.id
), filter_locations(id) AS (
 SELECT id FROM locations WHERE id=? AND deleted_at IS NULL
 UNION SELECT l.id FROM locations l JOIN filter_locations p ON l.parent_id=p.id WHERE l.deleted_at IS NULL
) `

func listFilteredItems(ctx context.Context, tx *sql.Tx, afterID string, limit int, filters ItemFilters, from, to int, zone *time.Location) (ItemPage, error) {
	clauses, args := []string{"i.id>?"}, []any{filters.CategoryID, filters.LocationID, afterID}
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
			args = append(args, filters.LocationID, filters.LocationID)
		}
		clauses = append(clauses, `((i.tracking_mode='individual' AND EXISTS(SELECT 1 FROM item_units u
 WHERE u.item_id=i.id AND u.status<>'disposed' AND u.location_id IN(`+ids+`)))
 OR (i.tracking_mode='quantity' AND EXISTS(SELECT 1 FROM stock_balances b
 WHERE b.item_id=i.id AND b.quantity_base>0 AND b.location_id IN(`+ids+`))))`)
	}
	if filters.Status != "" {
		clauses = append(clauses, "i.status=?")
		args = append(args, filters.Status)
	}
	rows, err := tx.QueryContext(ctx, itemFilterCTEs+itemSelect+" WHERE "+strings.Join(clauses, " AND ")+" ORDER BY i.id", args...)
	if err != nil {
		return ItemPage{}, err
	}
	defer rows.Close()
	page := ItemPage{Data: make([]Item, 0)}
	// ponytail: stream O(candidates) literal matches; add a Unicode substring
	// index if measured catalog-list latency exceeds the household budget.
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return ItemPage{}, err
		}
		matched, err := itemMatchesPurchase(ctx, tx, item.ID, filters, from, to, zone)
		if err != nil {
			return ItemPage{}, err
		}
		if !matched {
			continue
		}
		if filters.Query != "" {
			text, err := readItemSearchText(ctx, tx, item)
			if err != nil {
				return ItemPage{}, err
			}
			if !strings.Contains(strings.ToLower(text), filters.Query) {
				continue
			}
		}
		page.Data = append(page.Data, item)
		if len(page.Data) > limit {
			page.Data = page.Data[:limit]
			page.NextID = page.Data[limit-1].ID
			return page, nil
		}
	}
	if err = rows.Err(); err != nil {
		return ItemPage{}, err
	}
	return page, nil
}

func itemMatchesPurchase(ctx context.Context, tx *sql.Tx, itemID string, filters ItemFilters, from, to int, zone *time.Location) (bool, error) {
	if from == 0 && to == 0 && filters.Currency == "" && filters.Merchant == "" {
		return true, nil
	}
	condition, args := "l.item_id=?", []any{itemID}
	if filters.Currency != "" {
		condition += " AND p.currency=?"
		args = append(args, filters.Currency)
	}
	if filters.Merchant != "" {
		condition += " AND p.merchant_name=?"
		args = append(args, filters.Merchant)
	}
	joined := " FROM purchase_lines l JOIN purchases p ON p.id=l.purchase_id WHERE " + condition
	if from == 0 && to == 0 {
		var matched bool
		err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1"+joined+")", args...).Scan(&matched)
		return matched, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT p.id,p.purchased_at"+joined+" ORDER BY p.id", args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, purchased string
		if err = rows.Scan(&id, &purchased); err != nil {
			return false, err
		}
		if !validDomainID(id) || !purchaseInstantSyntax.MatchString(purchased) {
			return false, ErrDomainState
		}
		instant, err := time.Parse(time.RFC3339Nano, purchased)
		if err != nil || instant.Year() < 1 {
			return false, ErrDomainState
		}
		// Classify the instant's local date directly. A 23/25-hour or skipped day
		// must not be approximated by UTC midnight or a fixed 24-hour window.
		day := civilDateKey(instant.In(zone))
		if (from == 0 || day >= from) && (to == 0 || day <= to) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func readItemSearchText(ctx context.Context, tx *sql.Tx, item Item) (string, error) {
	var payload string
	var revision, counter int64
	var fieldCount int
	err := tx.QueryRowContext(ctx, `SELECT p.payload_json,p.entity_revision,COALESCE(r.revision,0),
 (SELECT COUNT(*) FROM json_each(p.payload_json))
 FROM current_projections p LEFT JOIN projection_revisions r ON r.entity_type=p.entity_type AND r.entity_id=p.entity_id
 WHERE p.entity_type='search_doc' AND p.entity_id=?`, projectionPair("item", item.ID)).Scan(&payload, &revision, &counter, &fieldCount)
	if errors.Is(err, sql.ErrNoRows) {
		// Legacy search caches are not authoritative. Derive an unmaterialized
		// document from approved source without publishing any read-side writes.
		text, err := loadSearchText(ctx, tx, item.ItemData, "")
		if err != nil {
			return "", err
		}
		return sourceText(text.title, text.identifiers, text.details, text.reviewed), nil
	}
	if err != nil {
		return "", err
	}
	var fields map[string]*string
	if revision < 1 || revision > maxSafeInteger || counter != revision || !utf8.ValidString(payload) || fieldCount != 3 ||
		json.Unmarshal([]byte(payload), &fields) != nil || len(fields) != 3 || fields["entity_type"] == nil || fields["entity_id"] == nil || fields["approved_text"] == nil ||
		*fields["entity_type"] != "item" || *fields["entity_id"] != item.ID {
		return "", ErrDomainState
	}
	return *fields["approved_text"], nil
}
