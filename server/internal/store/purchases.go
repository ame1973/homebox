package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Purchase payloads are the unchanged offline representations. Their independent
// revisions and private creation/audit metadata never enter the payload itself.
type PurchaseData struct {
	ID           string `json:"id"`
	MerchantName string `json:"merchant_name"`
	PurchasedAt  string `json:"purchased_at"`
	Currency     string `json:"currency"`
	ReceiptNote  string `json:"receipt_note"`
}

type Purchase struct {
	PurchaseData
	Revision int64 `json:"revision"`
}

type PurchaseLineData struct {
	ID           string  `json:"id"`
	PurchaseID   string  `json:"purchase_id"`
	ItemID       string  `json:"item_id"`
	ItemUnitID   *string `json:"item_unit_id"`
	QuantityBase int64   `json:"quantity_base"`
	PaidMinor    int64   `json:"paid_minor"`
	Note         string  `json:"note"`
}

type PurchaseLine struct {
	PurchaseLineData
	Revision int64 `json:"revision"`
}

const purchaseSelect = `SELECT p.id,p.merchant_name,p.purchased_at,p.currency,p.receipt_note,COALESCE(r.revision,0)
 FROM purchases p LEFT JOIN projection_revisions r ON r.entity_type='purchase' AND r.entity_id=p.id`
const purchaseLineSelect = `SELECT l.id,l.purchase_id,l.item_id,l.item_unit_id,l.quantity_base,l.paid_minor,l.note,
 COALESCE(r.revision,0),l.created_at,l.mutation_event_id FROM purchase_lines l
 LEFT JOIN projection_revisions r ON r.entity_type='purchase_line' AND r.entity_id=l.id`

func scanPurchase(row rowScanner) (Purchase, error) {
	var value Purchase
	if err := row.Scan(&value.ID, &value.MerchantName, &value.PurchasedAt, &value.Currency, &value.ReceiptNote, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Purchase{}, ErrNotFound
		}
		return Purchase{}, err
	}
	if err := value.validateDomainResult(); err != nil {
		return Purchase{}, err
	}
	return value, nil
}

func scanPurchaseLine(row rowScanner) (PurchaseLine, error) {
	var value PurchaseLine
	var unit, created, event sql.NullString
	if err := row.Scan(&value.ID, &value.PurchaseID, &value.ItemID, &unit, &value.QuantityBase, &value.PaidMinor, &value.Note,
		&value.Revision, &created, &event); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PurchaseLine{}, ErrNotFound
		}
		return PurchaseLine{}, err
	}
	if unit.Valid {
		value.ItemUnitID = &unit.String
	}
	if created.Valid != event.Valid || created.Valid && (!validPurchaseInstant(created.String) || !validDomainID(event.String)) {
		return PurchaseLine{}, ErrDomainState
	}
	if err := value.validateDomainResult(); err != nil {
		return PurchaseLine{}, err
	}
	return value, nil
}

func loadPurchase(ctx context.Context, query queryer, id string) (Purchase, error) {
	return scanPurchase(query.QueryRowContext(ctx, purchaseSelect+" WHERE p.id=?", id))
}

func loadPurchaseLine(ctx context.Context, query queryer, id string) (PurchaseLine, error) {
	value, err := scanPurchaseLine(query.QueryRowContext(ctx, purchaseLineSelect+" WHERE l.id=?", id))
	if err != nil {
		return PurchaseLine{}, err
	}
	if err = validatePurchaseLineAuthority(ctx, query, value); err != nil {
		return PurchaseLine{}, err
	}
	return value, nil
}

// Retained reads validate representation and actual ownership, not today's new
// allocation policy. Never repair old counts or infer asset financial eligibility.
func validatePurchaseLineAuthority(ctx context.Context, query queryer, value PurchaseLine) error {
	if _, err := loadPurchase(ctx, query, value.PurchaseID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		return err
	}
	item, err := loadItem(ctx, query, value.ItemID)
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if value.ItemUnitID != nil {
		unit, err := loadUnit(ctx, query, *value.ItemUnitID)
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		if err != nil {
			return err
		}
		if item.TrackingMode != "individual" || unit.ItemID != item.ID {
			return ErrDomainState
		}
	}
	return nil
}

// Code spelling is not an ISO registry/exponent or cross-domain currency check.
func validCurrencyCode(value string) bool {
	return len(value) == 3 && strings.IndexFunc(value, func(r rune) bool { return r < 'A' || r > 'Z' }) < 0
}

func validPurchaseInstant(value string) bool {
	if len(value) < 20 || len(value) > 35 || !domainInstantSyntax.MatchString(value) {
		return false
	}
	// time.Parse accepts and truncates fractions beyond nanoseconds. Reject that
	// loss instead of silently changing a financial instant during publication.
	if dot := strings.IndexByte(value, '.'); dot >= 0 && strings.IndexAny(value[dot+1:], "Z+-") > 9 {
		return false
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && instant.Year() >= 1 && instant.Year() <= 9999 && instant.UTC().Year() >= 1 && instant.UTC().Year() <= 9999
}

func (value Purchase) validateDomainResult() error {
	if !validDomainID(value.ID) || !authString(value.MerchantName, 1, 300) || !validPurchaseInstant(value.PurchasedAt) ||
		!validCurrencyCode(value.Currency) || !authString(value.ReceiptNote, 0, 10000) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func (value PurchaseLine) validateDomainResult() error {
	if !validDomainID(value.ID) || !validDomainID(value.PurchaseID) || !validDomainID(value.ItemID) ||
		(value.ItemUnitID != nil && !validDomainID(*value.ItemUnitID)) || value.QuantityBase < 1 || value.QuantityBase > maxSafeInteger ||
		value.PaidMinor < 0 || value.PaidMinor > maxSafeInteger || !authString(value.Note, 0, 10000) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

// Validate after SQL, before checksums commit: native NUL/UTF-8, retained counters
// and actual related source cannot be certified by SQLite length/JSON alone.
func validatePurchasesBackfill(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT entity_type,entity_id,revision FROM projection_revisions WHERE entity_type IN('purchase','purchase_line')")
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind, identity string
		var revision int64
		if err = rows.Scan(&kind, &identity, &revision); err != nil {
			_ = rows.Close()
			return err
		}
		if !validProjectionIdentity(kind, identity) || revision < 1 || revision > maxSafeInteger {
			_ = rows.Close()
			return ErrDomainState
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, check := range []struct {
		query string
		scan  func(rowScanner) error
	}{
		{purchaseSelect, func(row rowScanner) error { _, err := scanPurchase(row); return err }},
		{purchaseLineSelect, func(row rowScanner) error {
			value, err := scanPurchaseLine(row)
			if err != nil {
				return err
			}
			return validatePurchaseLineAuthority(ctx, tx, value)
		}},
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
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	return nil
}
