package store

import (
	"context"
	"database/sql"
	"errors"
)

// Physical payloads are exactly the version-one offline representations. Their
// independent retained revisions belong only to the enclosing HTTP/sync resource.
type UnitData struct {
	ID           string  `json:"id"`
	ItemID       string  `json:"item_id"`
	LocationID   *string `json:"location_id"`
	AssetCode    string  `json:"asset_code"`
	Status       string  `json:"status"`
	SerialNumber *string `json:"serial_number"`
	Notes        string  `json:"notes"`
}

type Unit struct {
	UnitData
	Revision int64 `json:"revision"`
}

type StockData struct {
	ItemID       string `json:"item_id"`
	LocationID   string `json:"location_id"`
	QuantityBase int64  `json:"quantity_base"`
	UnitCode     string `json:"unit_code"`
	BaseUnit     string `json:"base_unit"`
	ScaleFactor  int64  `json:"scale_factor"`
}

type Stock struct {
	StockData
	Revision int64 `json:"revision"`
}

const unitSelect = `SELECT u.id,u.item_id,u.location_id,u.asset_code,u.status,u.serial_number,u.notes,
 COALESCE(r.revision,0),COALESCE(i.tracking_mode,''),COALESCE(ir.revision,0)
 FROM item_units u LEFT JOIN items i ON i.id=u.item_id
 LEFT JOIN projection_revisions r ON r.entity_type='unit' AND r.entity_id=u.id
 LEFT JOIN projection_revisions ir ON ir.entity_type='item' AND ir.entity_id=u.item_id`

const stockSelect = `SELECT b.item_id,b.location_id,b.quantity_base,COALESCE(i.unit_code,''),COALESCE(i.base_unit,''),
 COALESCE(i.scale_factor,0),COALESCE(r.revision,0),COALESCE(i.tracking_mode,''),COALESCE(ir.revision,0)
 FROM stock_balances b LEFT JOIN items i ON i.id=b.item_id
 LEFT JOIN projection_revisions r ON r.entity_type='stock' AND r.entity_id=json_array(b.item_id,b.location_id)
 LEFT JOIN projection_revisions ir ON ir.entity_type='item' AND ir.entity_id=b.item_id`

func scanUnit(row rowScanner) (Unit, error) {
	var value Unit
	var location, serial sql.NullString
	var tracking string
	var ownerRevision int64
	if err := row.Scan(&value.ID, &value.ItemID, &location, &value.AssetCode, &value.Status, &serial, &value.Notes,
		&value.Revision, &tracking, &ownerRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Unit{}, ErrNotFound
		}
		return Unit{}, err
	}
	if location.Valid {
		value.LocationID = &location.String
	}
	if serial.Valid {
		value.SerialNumber = &serial.String
	}
	if tracking != "individual" || ownerRevision < 1 || ownerRevision > maxSafeInteger {
		return Unit{}, ErrDomainState
	}
	if err := value.validateDomainResult(); err != nil {
		return Unit{}, err
	}
	return value, nil
}

func scanStock(row rowScanner) (Stock, error) {
	var value Stock
	var tracking string
	var ownerRevision int64
	if err := row.Scan(&value.ItemID, &value.LocationID, &value.QuantityBase, &value.UnitCode, &value.BaseUnit,
		&value.ScaleFactor, &value.Revision, &tracking, &ownerRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Stock{}, ErrNotFound
		}
		return Stock{}, err
	}
	if tracking != "quantity" || ownerRevision < 1 || ownerRevision > maxSafeInteger {
		return Stock{}, ErrDomainState
	}
	if err := value.validateDomainResult(); err != nil {
		return Stock{}, err
	}
	return value, nil
}

func loadUnit(ctx context.Context, query queryer, id string) (Unit, error) {
	return scanUnit(query.QueryRowContext(ctx, unitSelect+" WHERE u.id=?", id))
}

func loadStock(ctx context.Context, query queryer, itemID, locationID string) (Stock, error) {
	return scanStock(query.QueryRowContext(ctx, stockSelect+" WHERE b.item_id=? AND b.location_id=?", itemID, locationID))
}

func validUnitStatus(status string) bool {
	switch status {
	case "stored", "in_use", "lent", "lost", "retired", "disposed":
		return true
	default:
		return false
	}
}

func (value Unit) validateDomainResult() error {
	if !validDomainID(value.ID) || !validDomainID(value.ItemID) ||
		(value.LocationID != nil && !validDomainID(*value.LocationID)) || !authString(value.AssetCode, 1, 10000) ||
		(value.SerialNumber != nil && !authString(*value.SerialNumber, 0, 10000)) || !authString(value.Notes, 0, 10000) ||
		!validUnitStatus(value.Status) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func (value Stock) validateDomainResult() error {
	if !validDomainID(value.ItemID) || !validDomainID(value.LocationID) || value.QuantityBase < 0 || value.QuantityBase > maxSafeInteger ||
		!authString(value.UnitCode, 1, 32) || !baseUnitPattern.MatchString(value.BaseUnit) || value.ScaleFactor < 1 || value.ScaleFactor > maxSafeInteger ||
		value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

// SQL text length stops at native NUL and does not certify UTF-8 or retained
// authority. Validate every source after backfill SQL, before the ledger commits.
func validateHoldingsBackfill(ctx context.Context, tx *sql.Tx) error {
	if err := validateHoldingCounters(ctx, tx); err != nil {
		return err
	}
	for _, check := range []struct {
		query string
		scan  func(rowScanner) error
	}{
		{unitSelect, func(row rowScanner) error { _, err := scanUnit(row); return err }},
		{stockSelect, func(row rowScanner) error { _, err := scanStock(row); return err }},
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

// A noncanonical composite counter must not silently become a new revision-one
// Stock identity. Retained counter-only identities remain, but must be valid.
func validateHoldingCounters(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT entity_type,entity_id,revision FROM projection_revisions WHERE entity_type IN('unit','stock')")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, identity string
		var revision int64
		if err = rows.Scan(&kind, &identity, &revision); err != nil {
			return err
		}
		if !validProjectionIdentity(kind, identity) || revision < 1 || revision > maxSafeInteger {
			return ErrDomainState
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}
