package store

import (
	"context"
	"errors"

	"homebox/internal/barcode"
)

var (
	ErrBarcodeDuplicate = errors.New("product barcode is already linked to this item")
	ErrAliasDuplicate   = errors.New("exact alias is already linked to this item")
)

// BarcodeWrite is the complete pair, never a caller-supplied normalized value.
type BarcodeWrite struct {
	Symbology string `json:"symbology"`
	RawValue  string `json:"raw_value"`
}

// AttributeWrite supplies both exact fields, including an explicit empty value.
// The HTTP reader must also enforce required JSON field presence.
type AttributeWrite struct {
	AttributeKey   string `json:"attribute_key"`
	AttributeValue string `json:"attribute_value"`
}

// AttributePatch retains omitted fields; a supplied empty value clears text.
type AttributePatch struct {
	AttributeKey   *string `json:"attribute_key,omitempty"`
	AttributeValue *string `json:"attribute_value,omitempty"`
}

// Bind the original operation, subject, proof and typed input before any source
// changes. The subject is the Item for creation and the independent row otherwise.
func runCatalogLinkMutation[T domainResult](ctx context.Context, catalog *Catalog, token, key, operation, id string, revision int64, input any, apply func(*mutationTx) (T, error)) (T, error) {
	var zero T
	if !validDomainID(id) {
		return zero, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return zero, err
	}
	request := struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Input    any    `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, catalog.auth, token, key, operation, request, apply)
}

func (catalog *Catalog) CreateBarcode(ctx context.Context, token, key, itemID string, itemRevision int64, input BarcodeWrite) (Barcode, error) {
	normalized, err := barcode.Normalize(input.Symbology, input.RawValue)
	if err != nil {
		return Barcode{}, ErrDomainInput
	}
	return runCatalogLinkMutation(ctx, catalog, token, key, "post_items_id_barcodes", itemID, itemRevision, input, func(m *mutationTx) (Barcode, error) {
		if _, err := itemAtRevision(ctx, m.tx, itemID, itemRevision); err != nil {
			return Barcode{}, err
		}
		if err := m.requireBarcode(itemID, input.Symbology, normalized, ""); err != nil {
			return Barcode{}, err
		}
		id, err := newDomainID()
		if err != nil {
			return Barcode{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "INSERT INTO item_barcodes(id,item_id,symbology,raw_value,normalized_value,created_at) VALUES(?,?,?,?,?,?)", id, itemID, input.Symbology, input.RawValue, normalized, stamp(m.now)); err != nil {
			return Barcode{}, err
		}
		revision, err := m.touchCatalogLink("barcode", id, itemID, false)
		if err != nil {
			return Barcode{}, err
		}
		if revision != 1 {
			return Barcode{}, ErrDomainState
		}
		return loadBarcode(ctx, m.tx, id)
	})
}

func (catalog *Catalog) PatchBarcode(ctx context.Context, token, key, id string, revision int64, input BarcodeWrite) (Barcode, error) {
	normalized, err := barcode.Normalize(input.Symbology, input.RawValue)
	if err != nil {
		return Barcode{}, ErrDomainInput
	}
	return runCatalogLinkMutation(ctx, catalog, token, key, "patch_barcodes_id", id, revision, input, func(m *mutationTx) (Barcode, error) {
		before, err := loadBarcode(ctx, m.tx, id)
		if err != nil {
			return Barcode{}, err
		}
		if before.Revision != revision {
			return Barcode{}, ErrRevisionConflict
		}
		if before.Symbology == input.Symbology && before.RawValue == input.RawValue && before.NormalizedValue == normalized {
			return before, nil
		}
		if err := m.requireBarcode(before.ItemID, input.Symbology, normalized, id); err != nil {
			return Barcode{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE item_barcodes SET symbology=?,raw_value=?,normalized_value=? WHERE id=?", input.Symbology, input.RawValue, normalized, id); err != nil {
			return Barcode{}, err
		}
		if _, err = m.touchCatalogLink("barcode", id, before.ItemID, false); err != nil {
			return Barcode{}, err
		}
		return loadBarcode(ctx, m.tx, id)
	})
}

func (catalog *Catalog) CreateAlias(ctx context.Context, token, key, itemID string, itemRevision int64, alias string) (Alias, error) {
	if !authString(alias, 1, 300) {
		return Alias{}, ErrDomainInput
	}
	return runCatalogLinkMutation(ctx, catalog, token, key, "post_items_id_aliases", itemID, itemRevision, alias, func(m *mutationTx) (Alias, error) {
		if _, err := itemAtRevision(ctx, m.tx, itemID, itemRevision); err != nil {
			return Alias{}, err
		}
		if err := m.requireAlias(itemID, alias, ""); err != nil {
			return Alias{}, err
		}
		id, err := newDomainID()
		if err != nil {
			return Alias{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "INSERT INTO item_aliases(id,item_id,alias,created_at) VALUES(?,?,?,?)", id, itemID, alias, stamp(m.now)); err != nil {
			return Alias{}, err
		}
		revision, err := m.touchCatalogLink("alias", id, itemID, false)
		if err != nil {
			return Alias{}, err
		}
		if revision != 1 {
			return Alias{}, ErrDomainState
		}
		return loadAlias(ctx, m.tx, id)
	})
}

func (catalog *Catalog) PatchAlias(ctx context.Context, token, key, id string, revision int64, alias string) (Alias, error) {
	if !authString(alias, 1, 300) {
		return Alias{}, ErrDomainInput
	}
	return runCatalogLinkMutation(ctx, catalog, token, key, "patch_aliases_id", id, revision, alias, func(m *mutationTx) (Alias, error) {
		before, err := loadAlias(ctx, m.tx, id)
		if err != nil {
			return Alias{}, err
		}
		if before.Revision != revision {
			return Alias{}, ErrRevisionConflict
		}
		if before.Alias == alias {
			return before, nil
		}
		if err := m.requireAlias(before.ItemID, alias, id); err != nil {
			return Alias{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE item_aliases SET alias=? WHERE id=?", alias, id); err != nil {
			return Alias{}, err
		}
		if _, err = m.touchCatalogLink("alias", id, before.ItemID, false); err != nil {
			return Alias{}, err
		}
		return loadAlias(ctx, m.tx, id)
	})
}

func (catalog *Catalog) CreateAttribute(ctx context.Context, token, key, itemID string, itemRevision int64, input AttributeWrite) (Attribute, error) {
	if !authString(input.AttributeKey, 1, 100) || !authString(input.AttributeValue, 0, 10000) {
		return Attribute{}, ErrDomainInput
	}
	return runCatalogLinkMutation(ctx, catalog, token, key, "post_items_id_attributes", itemID, itemRevision, input, func(m *mutationTx) (Attribute, error) {
		if _, err := itemAtRevision(ctx, m.tx, itemID, itemRevision); err != nil {
			return Attribute{}, err
		}
		id, err := newDomainID()
		if err != nil {
			return Attribute{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "INSERT INTO item_attributes(id,item_id,attribute_key,attribute_value,created_at) VALUES(?,?,?,?,?)", id, itemID, input.AttributeKey, input.AttributeValue, stamp(m.now)); err != nil {
			return Attribute{}, err
		}
		revision, err := m.touchCatalogLink("attribute", id, itemID, false)
		if err != nil {
			return Attribute{}, err
		}
		if revision != 1 {
			return Attribute{}, ErrDomainState
		}
		return loadAttribute(ctx, m.tx, id)
	})
}

func (catalog *Catalog) PatchAttribute(ctx context.Context, token, key, id string, revision int64, input AttributePatch) (Attribute, error) {
	if input.AttributeKey == nil && input.AttributeValue == nil || input.AttributeKey != nil && !authString(*input.AttributeKey, 1, 100) || input.AttributeValue != nil && !authString(*input.AttributeValue, 0, 10000) {
		return Attribute{}, ErrDomainInput
	}
	return runCatalogLinkMutation(ctx, catalog, token, key, "patch_attributes_id", id, revision, input, func(m *mutationTx) (Attribute, error) {
		before, err := loadAttribute(ctx, m.tx, id)
		if err != nil {
			return Attribute{}, err
		}
		if before.Revision != revision {
			return Attribute{}, ErrRevisionConflict
		}
		after := before
		if input.AttributeKey != nil {
			after.AttributeKey = *input.AttributeKey
		}
		if input.AttributeValue != nil {
			after.AttributeValue = *input.AttributeValue
		}
		if after.AttributeData == before.AttributeData {
			return before, nil
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE item_attributes SET attribute_key=?,attribute_value=? WHERE id=?", after.AttributeKey, after.AttributeValue, id); err != nil {
			return Attribute{}, err
		}
		if _, err = m.touchCatalogLink("attribute", id, before.ItemID, false); err != nil {
			return Attribute{}, err
		}
		return loadAttribute(ctx, m.tx, id)
	})
}

func (catalog *Catalog) DeleteBarcode(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
	return catalog.deleteCatalogLink(ctx, token, key, "barcode", id, revision)
}

func (catalog *Catalog) DeleteAlias(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
	return catalog.deleteCatalogLink(ctx, token, key, "alias", id, revision)
}

func (catalog *Catalog) DeleteAttribute(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
	return catalog.deleteCatalogLink(ctx, token, key, "attribute", id, revision)
}

func (catalog *Catalog) deleteCatalogLink(ctx context.Context, token, key, kind, id string, revision int64) (ActionAck, error) {
	var table, operation string
	switch kind {
	case "barcode":
		table, operation = "item_barcodes", "delete_barcodes_id"
	case "alias":
		table, operation = "item_aliases", "delete_aliases_id"
	case "attribute":
		table, operation = "item_attributes", "delete_attributes_id"
	default:
		return ActionAck{}, ErrDomainInput
	}
	return runCatalogLinkMutation(ctx, catalog, token, key, operation, id, revision, struct{}{}, func(m *mutationTx) (ActionAck, error) {
		var itemID string
		var current int64
		var err error
		switch kind {
		case "barcode":
			var value Barcode
			value, err = loadBarcode(ctx, m.tx, id)
			itemID, current = value.ItemID, value.Revision
		case "alias":
			var value Alias
			value, err = loadAlias(ctx, m.tx, id)
			itemID, current = value.ItemID, value.Revision
		case "attribute":
			var value Attribute
			value, err = loadAttribute(ctx, m.tx, id)
			itemID, current = value.ItemID, value.Revision
		}
		if err != nil {
			return ActionAck{}, err
		}
		if current != revision {
			return ActionAck{}, ErrRevisionConflict
		}
		// The table is selected solely from the compile-time domain cases above.
		if _, err = m.tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id); err != nil {
			return ActionAck{}, err
		}
		next, err := m.touchCatalogLink(kind, id, itemID, true)
		if err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: next}, nil
	})
}

func (m *mutationTx) touchCatalogLink(kind, id, itemID string, deleted bool) (int64, error) {
	// A missing owner counter must not silently become a fabricated revision one.
	if _, err := loadItem(m.ctx, m.tx, itemID); err != nil {
		return 0, err
	}
	revision, err := m.touch(kind, id, deleted)
	if err != nil {
		return 0, err
	}
	if err := m.touchItemSearch(itemID, true); err != nil {
		return 0, err
	}
	return revision, nil
}

func (m *mutationTx) requireBarcode(itemID, symbology, normalized, exceptID string) error {
	var exists bool
	if err := m.tx.QueryRowContext(m.ctx, "SELECT EXISTS(SELECT 1 FROM item_barcodes WHERE item_id=? AND symbology=? AND normalized_value=? AND id<>?)", itemID, symbology, normalized, exceptID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrBarcodeDuplicate
	}
	return nil
}

func (m *mutationTx) requireAlias(itemID, alias, exceptID string) error {
	var exists bool
	if err := m.tx.QueryRowContext(m.ctx, "SELECT EXISTS(SELECT 1 FROM item_aliases WHERE item_id=? AND alias=? AND id<>?)", itemID, alias, exceptID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrAliasDuplicate
	}
	return nil
}
