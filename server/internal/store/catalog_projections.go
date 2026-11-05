package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// itemData is the exact version-one offline payload, without envelope revision.
// Related tag/link/search records remain independently identified projections.
type itemData struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	TrackingMode string  `json:"tracking_mode"`
	UnitCode     string  `json:"unit_code"`
	CategoryID   *string `json:"category_id"`
	Brand        *string `json:"brand"`
	Model        *string `json:"model"`
	Description  string  `json:"description"`
	Notes        string  `json:"notes"`
	BaseUnit     string  `json:"base_unit"`
	ScaleFactor  int64   `json:"scale_factor"`
	Status       string  `json:"status"`
}

type searchData struct {
	EntityType   string `json:"entity_type"`
	EntityID     string `json:"entity_id"`
	ApprovedText string `json:"approved_text"`
}

func loadItemData(ctx context.Context, query queryer, id string) (itemData, error) {
	var value itemData
	var category, brand, model sql.NullString
	err := query.QueryRowContext(ctx, `SELECT id,name,tracking_mode,unit_code,category_id,brand,model,
 description,notes,base_unit,scale_factor,status FROM items WHERE id=?`, id).
		Scan(&value.ID, &value.Name, &value.TrackingMode, &value.UnitCode, &category, &brand, &model,
			&value.Description, &value.Notes, &value.BaseUnit, &value.ScaleFactor, &value.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return itemData{}, ErrNotFound
	}
	if err != nil {
		return itemData{}, err
	}
	if category.Valid {
		value.CategoryID = &category.String
	}
	if brand.Valid {
		value.Brand = &brand.String
	}
	if model.Valid {
		value.Model = &model.String
	}
	if !validDomainID(value.ID) || !authString(value.Name, 1, 300) ||
		(value.TrackingMode != "individual" && value.TrackingMode != "quantity") || !authString(value.UnitCode, 1, 32) ||
		!baseUnitPattern.MatchString(value.BaseUnit) || value.ScaleFactor < 1 || value.ScaleFactor > maxSafeInteger ||
		value.CategoryID != nil && !validDomainID(*value.CategoryID) ||
		!authString(value.Description, 0, 10000) || !authString(value.Notes, 0, 10000) ||
		(value.Brand != nil && !utf8.ValidString(*value.Brand)) || (value.Model != nil && !utf8.ValidString(*value.Model)) ||
		(value.Status != "active" && value.Status != "archived") {
		return itemData{}, ErrDomainState
	}
	return value, nil
}

func sourceText(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "\n")
}

func sourceTextRows(ctx context.Context, query queryer, statement string, args ...any) (string, error) {
	rows, err := query.QueryContext(ctx, statement, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return "", err
		}
		if !utf8.ValidString(value) {
			return "", ErrDomainState
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return sourceText(values...), nil
}

func categoryPathText(ctx context.Context, query queryer, id *string) (string, error) {
	if id == nil {
		return "", nil
	}
	rows, err := query.QueryContext(ctx, categoryAncestorCTE+"SELECT id,parent_id,name FROM category_ancestors", *id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ancestors := make(map[string]CategoryData)
	for rows.Next() {
		var value CategoryData
		var parent sql.NullString
		if err = rows.Scan(&value.ID, &parent, &value.Name); err != nil {
			return "", err
		}
		if parent.Valid {
			value.ParentID = &parent.String
		}
		if !validCategoryData(value) {
			return "", ErrDomainState
		}
		ancestors[value.ID] = value
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	var names []string
	seen := make(map[string]bool)
	for current := id; current != nil; {
		value, exists := ancestors[*current]
		if !exists || seen[*current] {
			return "", ErrDomainState
		}
		seen[*current] = true
		names = append(names, value.Name)
		current = value.ParentID
	}
	slices.Reverse(names)
	return sourceText(names...), nil
}

// Stage once; the flush rebuilds approved text from the final transaction state,
// even if multiple related changes occur before this identity is published.
func (m *mutationTx) touchItemSearch(id string, touchItem bool) error {
	if touchItem {
		if _, err := m.touch("item", id, false); err != nil {
			return err
		}
		if _, err := m.tx.ExecContext(m.ctx, "UPDATE items SET updated_at=? WHERE id=? AND updated_at<>?", stamp(m.now), id, stamp(m.now)); err != nil {
			return err
		}
	}
	_, err := m.touch("search_doc", projectionPair("item", id), false)
	return err
}

// Realized fields own title/brand/model/category/tag values after review. Old
// accepted labels must not resurrect a later user override. Only approved free
// OCR text contributes separately; raw jobs/results and the old cache never do.
func (m *mutationTx) materializeItemSearch(identity string) (searchData, error) {
	parts, valid := projectionPairParts(identity)
	pending, staged := m.pending[projectionKey{"search_doc", identity}]
	if !valid || parts[0] != "item" || !staged || pending.deleted || pending.revision < 1 {
		return searchData{}, ErrDomainState
	}
	id := parts[1]
	item, err := loadItemData(m.ctx, m.tx, id)
	if err != nil {
		return searchData{}, err
	}
	category, err := categoryPathText(m.ctx, m.tx, item.CategoryID)
	if err != nil {
		return searchData{}, err
	}
	var brand, model string
	if item.Brand != nil {
		brand = *item.Brand
	}
	if item.Model != nil {
		model = *item.Model
	}
	barcodes, err := sourceTextRows(m.ctx, m.tx, "SELECT raw_value||char(10)||normalized_value FROM item_barcodes WHERE item_id=? ORDER BY id", id)
	if err != nil {
		return searchData{}, err
	}
	identifiers := sourceText(brand, model, barcodes)
	details := sourceText(item.Description, item.Notes, category)
	for _, statement := range []string{
		"SELECT t.name FROM tags t JOIN item_tags l ON l.tag_id=t.id WHERE l.item_id=? ORDER BY t.id",
		"SELECT alias FROM item_aliases WHERE item_id=? ORDER BY id",
		"SELECT attribute_key||char(10)||attribute_value FROM item_attributes WHERE item_id=? ORDER BY id",
		"SELECT p.merchant_name||char(10)||p.receipt_note FROM purchases p WHERE EXISTS(SELECT 1 FROM purchase_lines l WHERE l.purchase_id=p.id AND l.item_id=?) ORDER BY p.id",
		"SELECT note FROM purchase_lines WHERE item_id=? ORDER BY id",
	} {
		value, err := sourceTextRows(m.ctx, m.tx, statement, id)
		if err != nil {
			return searchData{}, err
		}
		details = sourceText(details, value)
	}
	reviewed, err := sourceTextRows(m.ctx, m.tx, `SELECT CASE WHEN state='corrected' THEN corrected_value ELSE raw_value END
 FROM ai_review_entries WHERE item_id=? AND field_key IN('specification','keyword')
 AND state IN('accepted','corrected') AND reviewed_at IS NOT NULL ORDER BY id`, id)
	if err != nil {
		return searchData{}, err
	}
	_, err = m.tx.ExecContext(m.ctx, `INSERT INTO search_documents(item_id,title,identifiers,details,reviewed_ai,revision,updated_at)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(item_id) DO UPDATE SET title=excluded.title,identifiers=excluded.identifiers,
 details=excluded.details,reviewed_ai=excluded.reviewed_ai,revision=excluded.revision,updated_at=excluded.updated_at`,
		id, item.Name, identifiers, details, reviewed, pending.revision, stamp(m.now))
	if err != nil {
		return searchData{}, err
	}
	return searchData{EntityType: "item", EntityID: id, ApprovedText: sourceText(item.Name, identifiers, details, reviewed)}, nil
}
