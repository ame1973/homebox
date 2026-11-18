package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"homebox/internal/offlineprojection"
)

// validateSyncRecord checks a frozen record's version-one shape and identity.
// A nil payload represents SQL NULL and is required for a tombstone. Callers
// must bound input before calling and separately authorize and verify sources.
func validateSyncRecord(kind, id, operation string, revision, version int64, payload []byte) error {
	if !validProjectionIdentity(kind, id) || revision < 1 || revision > maxSafeInteger || version != 1 {
		return ErrDomainState
	}
	switch operation {
	case "delete":
		if payload != nil {
			return ErrDomainState
		}
		return nil
	case "upsert":
		if payload == nil || offlineprojection.ValidatePayload(kind, payload) != nil {
			return ErrDomainState
		}
	default:
		return ErrDomainState
	}

	var value struct {
		ID         string `json:"id"`
		ItemID     string `json:"item_id"`
		LocationID string `json:"location_id"`
		TagID      string `json:"tag_id"`
		EntityType string `json:"entity_type"`
		EntityID   string `json:"entity_id"`
		ItemUnitID string `json:"item_unit_id"`
	}
	if err := json.Unmarshal(payload, &value); err != nil {
		return ErrDomainState
	}
	var actual string
	switch kind {
	case "stock":
		actual = projectionPair(value.ItemID, value.LocationID)
	case "item_tag":
		actual = projectionPair(value.ItemID, value.TagID)
	case "search_doc":
		actual = projectionPair(value.EntityType, value.EntityID)
	case "asset", "disposal":
		actual = value.ItemUnitID
	default:
		actual = value.ID
	}
	if actual != id {
		return ErrDomainState
	}
	return nil
}

// validateSearchProjectionSource compares a bounded current payload against
// actual approved source in the same transaction. The caller must separately
// establish current-row/counter authority and completed-session authorization.
// It must not be used for historical deltas after source changes.
func validateSearchProjectionSource(ctx context.Context, tx *sql.Tx, identity string, revision int64, payload []byte) error {
	if ctx == nil || tx == nil {
		return ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSyncRecord("search_doc", identity, "upsert", revision, 1, payload); err != nil {
		return err
	}
	parts, _ := projectionPairParts(identity)
	if parts[0] == "unit" {
		if _, err := loadUnit(ctx, tx, parts[1]); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrDomainState
			}
			return err
		}
	}
	text, err := readSearchProjectionText(ctx, tx, parts[0], parts[1])
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	var value searchData
	if json.Unmarshal(payload, &value) != nil || value.ApprovedText != sourceText(text.title, text.identifiers, text.details, text.reviewed) {
		return ErrDomainState
	}
	return nil
}
