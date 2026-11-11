package store

import (
	"context"
	"database/sql"
	"errors"

	"homebox/internal/barcode"
)

// Owned payloads retain independent row IDs; the enclosing resource owns the
// revision. Legacy raw/normalized text is read exactly, never silently repaired.
type BarcodeData struct {
	ID              string `json:"id"`
	ItemID          string `json:"item_id"`
	Symbology       string `json:"symbology"`
	RawValue        string `json:"raw_value"`
	NormalizedValue string `json:"normalized_value"`
}

type Barcode struct {
	BarcodeData
	Revision int64 `json:"revision"`
}

type AliasData struct {
	ID     string `json:"id"`
	ItemID string `json:"item_id"`
	Alias  string `json:"alias"`
}

type Alias struct {
	AliasData
	Revision int64 `json:"revision"`
}

type AttributeData struct {
	ID             string `json:"id"`
	ItemID         string `json:"item_id"`
	AttributeKey   string `json:"attribute_key"`
	AttributeValue string `json:"attribute_value"`
}

type Attribute struct {
	AttributeData
	Revision int64 `json:"revision"`
}

const barcodeSelect = `SELECT b.id,b.item_id,b.symbology,b.raw_value,b.normalized_value,COALESCE(r.revision,0)
 FROM item_barcodes b LEFT JOIN projection_revisions r ON r.entity_type='barcode' AND r.entity_id=b.id`
const aliasSelect = `SELECT a.id,a.item_id,a.alias,COALESCE(r.revision,0)
 FROM item_aliases a LEFT JOIN projection_revisions r ON r.entity_type='alias' AND r.entity_id=a.id`
const attributeSelect = `SELECT a.id,a.item_id,a.attribute_key,a.attribute_value,COALESCE(r.revision,0)
 FROM item_attributes a LEFT JOIN projection_revisions r ON r.entity_type='attribute' AND r.entity_id=a.id`

func scanBarcode(row rowScanner) (Barcode, error) {
	var value Barcode
	if err := row.Scan(&value.ID, &value.ItemID, &value.Symbology, &value.RawValue, &value.NormalizedValue, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Barcode{}, ErrNotFound
		}
		return Barcode{}, err
	}
	if err := value.validateDomainResult(); err != nil {
		return Barcode{}, err
	}
	return value, nil
}

func scanAlias(row rowScanner) (Alias, error) {
	var value Alias
	if err := row.Scan(&value.ID, &value.ItemID, &value.Alias, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Alias{}, ErrNotFound
		}
		return Alias{}, err
	}
	if err := value.validateDomainResult(); err != nil {
		return Alias{}, err
	}
	return value, nil
}

func scanAttribute(row rowScanner) (Attribute, error) {
	var value Attribute
	if err := row.Scan(&value.ID, &value.ItemID, &value.AttributeKey, &value.AttributeValue, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Attribute{}, ErrNotFound
		}
		return Attribute{}, err
	}
	if err := value.validateDomainResult(); err != nil {
		return Attribute{}, err
	}
	return value, nil
}

func loadBarcode(ctx context.Context, query queryer, id string) (Barcode, error) {
	return scanBarcode(query.QueryRowContext(ctx, barcodeSelect+" WHERE b.id=?", id))
}

func loadAlias(ctx context.Context, query queryer, id string) (Alias, error) {
	return scanAlias(query.QueryRowContext(ctx, aliasSelect+" WHERE a.id=?", id))
}

func loadAttribute(ctx context.Context, query queryer, id string) (Attribute, error) {
	return scanAttribute(query.QueryRowContext(ctx, attributeSelect+" WHERE a.id=?", id))
}

func (value Barcode) validateDomainResult() error {
	// Validate persisted representation, not new-input checksum policy. Previously
	// retained manual values must remain inspectable without a hidden rewrite.
	if !validDomainID(value.ID) || !validDomainID(value.ItemID) || !barcode.ValidSymbology(value.Symbology) ||
		!authString(value.RawValue, 1, 512) || !authString(value.NormalizedValue, 1, 512) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func (value Alias) validateDomainResult() error {
	if !validDomainID(value.ID) || !validDomainID(value.ItemID) || !authString(value.Alias, 1, 300) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func (value Attribute) validateDomainResult() error {
	if !validDomainID(value.ID) || !validDomainID(value.ItemID) || !authString(value.AttributeKey, 1, 100) ||
		!authString(value.AttributeValue, 0, 10000) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

// Validate the complete backfill inside the migration transaction before its
// checksum ledger can commit. SQLite length/JSON checks alone do not reject all
// invalid UTF-8 or correctly count strings containing native Code128 NUL bytes.
func validateCatalogLinkBackfill(ctx context.Context, tx *sql.Tx) error {
	for _, check := range []struct {
		query string
		scan  func(rowScanner) error
	}{
		{barcodeSelect, func(row rowScanner) error { _, err := scanBarcode(row); return err }},
		{aliasSelect, func(row rowScanner) error { _, err := scanAlias(row); return err }},
		{attributeSelect, func(row rowScanner) error { _, err := scanAttribute(row); return err }},
	} {
		rows, err := tx.QueryContext(ctx, check.query)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err = check.scan(rows); err != nil {
				_ = rows.Close()
				return err
			}
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
