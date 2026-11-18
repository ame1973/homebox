package store

import (
	"encoding/json"

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
