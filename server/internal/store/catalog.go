package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
)

var ErrItemHistory = errors.New("item has retained unit, balance, purchase, or movement history")

// ItemData is exactly the version-one offline catalog payload. Link identities
// remain independent, and the enclosing HTTP/sync resource owns the revision.
type ItemData struct {
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

type Item struct {
	ItemData
	Revision int64 `json:"revision"`
}

type ItemCreate struct {
	Name         string  `json:"name"`
	Brand        *string `json:"brand"`
	Model        *string `json:"model"`
	CategoryID   *string `json:"category_id"`
	Description  string  `json:"description"`
	Notes        string  `json:"notes"`
	TrackingMode *string `json:"tracking_mode,omitempty"`
	UnitCode     *string `json:"unit_code,omitempty"`
	BaseUnit     *string `json:"base_unit,omitempty"`
	ScaleFactor  *int64  `json:"scale_factor,omitempty"`
	Status       *string `json:"status,omitempty"`
}

type ItemPatch struct {
	Name         *string        `json:"name,omitempty"`
	Brand        NullableString `json:"brand"`
	Model        NullableString `json:"model"`
	CategoryID   NullableID     `json:"category_id"`
	Description  *string        `json:"description,omitempty"`
	Notes        *string        `json:"notes,omitempty"`
	TrackingMode *string        `json:"tracking_mode,omitempty"`
	UnitCode     *string        `json:"unit_code,omitempty"`
	BaseUnit     *string        `json:"base_unit,omitempty"`
	ScaleFactor  *int64         `json:"scale_factor,omitempty"`
	Status       *string        `json:"status,omitempty"`
}

type ItemPage = metadataPage[Item]

// Catalog owns authoritative catalog transactions in the existing private store.
// HTTP still owns Origin/CSRF/version/admission; no AI worker is required.
type Catalog struct{ auth *Auth }

func NewCatalog(a *Auth) (*Catalog, error) {
	if a == nil || a.store == nil {
		return nil, ErrDomainInput
	}
	return a.store.NewCatalog(a)
}

func (s *Store) NewCatalog(a *Auth) (*Catalog, error) {
	if s == nil || a == nil || a.store != s {
		return nil, ErrDomainInput
	}
	return &Catalog{auth: a}, nil
}

func (catalog *Catalog) Get(ctx context.Context, token, id string) (Item, error) {
	if !validDomainID(id) {
		return Item{}, ErrDomainInput
	}
	return readDomain(ctx, catalog.auth, token, func(tx *sql.Tx) (Item, error) { return loadItem(ctx, tx, id) })
}

// List is an internal, unfiltered stable-ID page, not the public filtered list,
// an unsigned HTTP cursor, or a frozen offline snapshot.
func (catalog *Catalog) List(ctx context.Context, token, afterID string, limit int) (ItemPage, error) {
	if (afterID != "" && !validDomainID(afterID)) || limit < 1 || limit > 100 {
		return ItemPage{}, ErrDomainInput
	}
	return readDomain(ctx, catalog.auth, token, func(tx *sql.Tx) (ItemPage, error) {
		rows, err := tx.QueryContext(ctx, itemSelect+" WHERE i.id>? ORDER BY i.id LIMIT ?", afterID, limit+1)
		if err != nil {
			return ItemPage{}, err
		}
		defer rows.Close()
		page := ItemPage{Data: make([]Item, 0)}
		for rows.Next() {
			value, err := scanItem(rows)
			if err != nil {
				return ItemPage{}, err
			}
			page.Data = append(page.Data, value)
		}
		if err = rows.Err(); err != nil {
			return ItemPage{}, err
		}
		if len(page.Data) > limit {
			page.Data = page.Data[:limit]
			page.NextID = page.Data[limit-1].ID
		}
		return page, nil
	})
}

func (catalog *Catalog) Create(ctx context.Context, token, key string, input ItemCreate) (Item, error) {
	value := ItemData{Name: input.Name, Brand: input.Brand, Model: input.Model, CategoryID: input.CategoryID,
		Description: input.Description, Notes: input.Notes, TrackingMode: "individual", UnitCode: "pcs", BaseUnit: "pcs", ScaleFactor: 1, Status: "active"}
	for _, field := range []struct{ supplied, destination *string }{
		{input.TrackingMode, &value.TrackingMode}, {input.UnitCode, &value.UnitCode},
		{input.BaseUnit, &value.BaseUnit}, {input.Status, &value.Status},
	} {
		if field.supplied != nil {
			*field.destination = *field.supplied
		}
	}
	if input.ScaleFactor != nil {
		value.ScaleFactor = *input.ScaleFactor
	}
	if !validItemData(value, false) || (value.UnitCode != "pcs" || value.BaseUnit != "pcs" || value.ScaleFactor != 1) &&
		(input.UnitCode == nil || input.BaseUnit == nil || input.ScaleFactor == nil) {
		return Item{}, ErrDomainInput
	}
	return runDomainMutation(ctx, catalog.auth, token, key, "post_items", input, func(m *mutationTx) (Item, error) {
		if value.CategoryID != nil {
			if _, err := loadCategory(ctx, m.tx, *value.CategoryID); err != nil {
				return Item{}, err
			}
		}
		id, err := newDomainID()
		if err != nil {
			return Item{}, err
		}
		_, err = m.tx.ExecContext(ctx, `INSERT INTO items(id,name,brand,model,category_id,tracking_mode,description,notes,unit_code,base_unit,scale_factor,status,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, value.Name, value.Brand, value.Model, value.CategoryID, value.TrackingMode,
			value.Description, value.Notes, value.UnitCode, value.BaseUnit, value.ScaleFactor, value.Status, stamp(m.now), stamp(m.now))
		if err != nil {
			return Item{}, err
		}
		if value.CategoryID != nil {
			if err = m.touchCategoryPath(*value.CategoryID); err != nil {
				return Item{}, err
			}
		}
		if err = m.touchItemSearch(id, true); err != nil {
			return Item{}, err
		}
		result, err := loadItem(ctx, m.tx, id)
		if err == nil && result.Revision != 1 {
			return Item{}, ErrDomainState // Never reuse a retained/tombstoned identity.
		}
		return result, err
	})
}

func (catalog *Catalog) Patch(ctx context.Context, token, key, id string, revision int64, input ItemPatch) (Item, error) {
	if !validDomainID(id) || !input.hasFields() ||
		!input.Brand.Set && input.Brand.Value != nil || !input.Model.Set && input.Model.Value != nil || !input.CategoryID.Set && input.CategoryID.Value != nil {
		return Item{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Item{}, err
	}
	request := struct {
		ID       string    `json:"id"`
		Revision int64     `json:"revision"`
		Input    ItemPatch `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, catalog.auth, token, key, "patch_items_id", request, func(m *mutationTx) (Item, error) {
		return m.patchItem(id, revision, input)
	})
}

func (catalog *Catalog) Archive(ctx context.Context, token, key, id string, revision int64, reason string) (Item, error) {
	if !validDomainID(id) || !authString(reason, 0, 10000) {
		return Item{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Item{}, err
	}
	request := struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Reason   string `json:"reason"`
	}{id, revision, reason}
	return runDomainMutation(ctx, catalog.auth, token, key, "post_items_id_archive", request, func(m *mutationTx) (Item, error) {
		m.reason = reason
		status := "archived"
		return m.patchItem(id, revision, ItemPatch{Status: &status})
	})
}

func (catalog *Catalog) Delete(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
	if !validDomainID(id) {
		return ActionAck{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return ActionAck{}, err
	}
	request := struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}{id, revision}
	return runDomainMutation(ctx, catalog.auth, token, key, "delete_items_id", request, func(m *mutationTx) (ActionAck, error) {
		before, err := itemAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return ActionAck{}, err
		}
		if err = requireNoItemHistory(ctx, m.tx, id); err != nil {
			return ActionAck{}, err
		}
		tags, err := queryIDs(ctx, m.tx, "SELECT tag_id FROM item_tags WHERE item_id=? ORDER BY tag_id", id)
		if err != nil {
			return ActionAck{}, err
		}
		for _, tagID := range tags {
			if !validDomainID(tagID) {
				return ActionAck{}, ErrDomainState
			}
			if _, err = m.touch("tag", tagID, false); err != nil {
				return ActionAck{}, err
			}
			if _, err = m.touch("item_tag", projectionPair(id, tagID), true); err != nil {
				return ActionAck{}, err
			}
		}
		for _, owned := range []struct{ kind, table string }{
			{"barcode", "item_barcodes"}, {"alias", "item_aliases"}, {"attribute", "item_attributes"},
			{"media_link", "media_links"}, {"ai_review", "ai_review_entries"},
		} {
			ids, err := queryIDs(ctx, m.tx, "SELECT id FROM "+owned.table+" WHERE item_id=? ORDER BY id", id)
			if err != nil {
				return ActionAck{}, err
			}
			for _, linkedID := range ids {
				if !validDomainID(linkedID) {
					return ActionAck{}, ErrDomainState
				}
				if _, err = m.touch(owned.kind, linkedID, true); err != nil {
					return ActionAck{}, err
				}
			}
		}
		if before.CategoryID != nil {
			if err = m.touchCategoryPath(*before.CategoryID); err != nil {
				return ActionAck{}, err
			}
		}
		// FK cascades remove owned source rows/FTS/jobs only. Shared media and
		// independent links belonging to another owner are deliberately retained.
		if _, err = m.tx.ExecContext(ctx, "DELETE FROM items WHERE id=?", id); err != nil {
			return ActionAck{}, err
		}
		next, err := m.touch("item", id, true)
		if err != nil {
			return ActionAck{}, err
		}
		if _, err = m.touch("search_doc", projectionPair("item", id), true); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: next}, nil
	})
}

const itemSelect = `SELECT i.id,i.name,i.tracking_mode,i.unit_code,i.category_id,i.brand,i.model,
 i.description,i.notes,i.base_unit,i.scale_factor,i.status,COALESCE(r.revision,0)
 FROM items i LEFT JOIN projection_revisions r ON r.entity_type='item' AND r.entity_id=i.id`

func scanItem(row rowScanner) (Item, error) {
	var value Item
	var category, brand, model sql.NullString
	if err := row.Scan(&value.ID, &value.Name, &value.TrackingMode, &value.UnitCode, &category, &brand, &model,
		&value.Description, &value.Notes, &value.BaseUnit, &value.ScaleFactor, &value.Status, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Item{}, ErrNotFound
		}
		return Item{}, err
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
	if err := value.validateDomainResult(); err != nil {
		return Item{}, err
	}
	return value, nil
}

func loadItem(ctx context.Context, query queryer, id string) (Item, error) {
	return scanItem(query.QueryRowContext(ctx, itemSelect+" WHERE i.id=?", id))
}

func itemAtRevision(ctx context.Context, query queryer, id string, revision int64) (Item, error) {
	value, err := loadItem(ctx, query, id)
	if err == nil && value.Revision != revision {
		return Item{}, ErrRevisionConflict
	}
	return value, err
}

func validItemData(value ItemData, hasID bool) bool {
	return (!hasID || validDomainID(value.ID)) && authString(value.Name, 1, 300) &&
		(value.Brand == nil || authString(*value.Brand, 0, 300)) && (value.Model == nil || authString(*value.Model, 0, 300)) &&
		(value.CategoryID == nil || validDomainID(*value.CategoryID)) &&
		(value.TrackingMode == "individual" || value.TrackingMode == "quantity") && authString(value.UnitCode, 1, 32) &&
		baseUnitPattern.MatchString(value.BaseUnit) && value.ScaleFactor >= 1 && value.ScaleFactor <= maxSafeInteger &&
		authString(value.Description, 0, 10000) && authString(value.Notes, 0, 10000) && (value.Status == "active" || value.Status == "archived")
}

func (value Item) validateDomainResult() error {
	if !validItemData(value.ItemData, true) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func (input ItemPatch) hasFields() bool {
	return input.Name != nil || input.Brand.Set || input.Model.Set || input.CategoryID.Set || input.Description != nil || input.Notes != nil ||
		input.TrackingMode != nil || input.UnitCode != nil || input.BaseUnit != nil || input.ScaleFactor != nil || input.Status != nil
}

func requireNoItemHistory(ctx context.Context, tx *sql.Tx, id string) error {
	var used bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM item_units WHERE item_id=?)
 OR EXISTS(SELECT 1 FROM stock_balances WHERE item_id=?) OR EXISTS(SELECT 1 FROM purchase_lines WHERE item_id=?)
 OR EXISTS(SELECT 1 FROM inventory_events WHERE item_id=?)`, id, id, id, id).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrItemHistory
	}
	return nil
}

func (m *mutationTx) patchItem(id string, revision int64, input ItemPatch) (Item, error) {
	before, err := itemAtRevision(m.ctx, m.tx, id, revision)
	if err != nil {
		return Item{}, err
	}
	after := before
	for _, field := range []struct{ supplied, destination *string }{
		{input.Name, &after.Name}, {input.TrackingMode, &after.TrackingMode}, {input.UnitCode, &after.UnitCode},
		{input.BaseUnit, &after.BaseUnit}, {input.Status, &after.Status}, {input.Description, &after.Description}, {input.Notes, &after.Notes},
	} {
		if field.supplied != nil {
			*field.destination = *field.supplied
		}
	}
	for _, field := range []struct {
		supplied    NullableString
		destination **string
	}{{input.Brand, &after.Brand}, {input.Model, &after.Model}, {input.CategoryID, &after.CategoryID}} {
		if field.supplied.Set {
			*field.destination = field.supplied.Value
		}
	}
	if input.ScaleFactor != nil {
		after.ScaleFactor = *input.ScaleFactor
	}
	if !validItemData(after.ItemData, true) {
		return Item{}, ErrDomainInput
	}
	if reflect.DeepEqual(before.ItemData, after.ItemData) {
		return before, nil
	}
	if before.TrackingMode != after.TrackingMode || before.UnitCode != after.UnitCode || before.BaseUnit != after.BaseUnit || before.ScaleFactor != after.ScaleFactor {
		if err = requireNoItemHistory(m.ctx, m.tx, id); err != nil {
			return Item{}, err
		}
	}
	classified := !sameParent(before.CategoryID, after.CategoryID)
	if classified && after.CategoryID != nil {
		if _, err = loadCategory(m.ctx, m.tx, *after.CategoryID); err != nil {
			return Item{}, err
		}
	}
	_, err = m.tx.ExecContext(m.ctx, `UPDATE items SET name=?,brand=?,model=?,category_id=?,tracking_mode=?,description=?,notes=?,unit_code=?,base_unit=?,scale_factor=?,status=?,updated_at=? WHERE id=?`,
		after.Name, after.Brand, after.Model, after.CategoryID, after.TrackingMode, after.Description, after.Notes,
		after.UnitCode, after.BaseUnit, after.ScaleFactor, after.Status, stamp(m.now), id)
	if err != nil {
		return Item{}, err
	}
	if classified {
		for _, category := range []*string{before.CategoryID, after.CategoryID} {
			if category != nil {
				if err = m.touchCategoryPath(*category); err != nil {
					return Item{}, err
				}
			}
		}
	}
	if err = m.touchItemSearch(id, true); err != nil {
		return Item{}, err
	}
	return loadItem(m.ctx, m.tx, id)
}
