package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Build each payload once from final authoritative state. Search publication also
// rebuilds its derived FTS row here, never copying an unverified historical cache.
func (m *mutationTx) projectionPayload(key projectionKey) ([]byte, error) {
	if key.kind == "search_doc" {
		value, err := m.materializeSearch(key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	return readProjectionSource(m.ctx, m.tx, key)
}

// This serializer only reads source. It does not establish session authority,
// current-row identity, complete source coverage or file-byte integrity.
func readProjectionSource(ctx context.Context, tx *sql.Tx, key projectionKey) ([]byte, error) {
	switch key.kind {
	case "location":
		value, err := loadLocation(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.LocationData)
	case "location_event":
		var movement locationMovement
		var from, to sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT id,location_id,occurred_at,from_parent_id,to_parent_id FROM location_events WHERE id=?`, key.id).
			Scan(&movement.ID, &movement.LocationID, &movement.OccurredAt, &from, &to)
		if err != nil {
			return nil, err
		}
		if from.Valid {
			movement.FromParentID = &from.String
		}
		if to.Valid {
			movement.ToParentID = &to.String
		}
		return json.Marshal(movement)
	case "category":
		value, err := loadCategory(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.CategoryData)
	case "tag":
		value, err := loadTag(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.TagData)
	case "item":
		value, err := loadItem(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.ItemData)
	case "unit":
		value, err := loadUnit(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.UnitData)
	case "stock":
		parts, valid := projectionPairParts(key.id)
		if !valid {
			return nil, ErrDomainState
		}
		value, err := loadStock(ctx, tx, parts[0], parts[1])
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.StockData)
	case "inventory_event":
		value, err := loadInventoryEvent(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	case "purchase":
		value, err := loadPurchase(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.PurchaseData)
	case "purchase_line":
		value, err := loadPurchaseLine(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		active, err := purchaseLineIsActive(ctx, tx, value)
		if err != nil {
			return nil, err
		}
		if !active {
			return nil, ErrNotFound
		}
		return json.Marshal(value.PurchaseLineData)
	case "asset":
		value, err := loadAsset(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.AssetData)
	case "usage":
		value, err := loadUsage(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.UsageData)
	case "cost":
		value, err := loadCost(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.CostData)
	case "disposal":
		value, err := loadDisposal(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.DisposalData)
	case "media":
		value, err := loadMedia(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.mediaData)
	case "media_link":
		value, err := loadMediaLink(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.mediaLinkData)
	case "barcode":
		value, err := loadBarcode(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.BarcodeData)
	case "alias":
		value, err := loadAlias(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.AliasData)
	case "attribute":
		value, err := loadAttribute(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.AttributeData)
	case "ai_review":
		value, err := loadSyncReviewSource(ctx, tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.Data)
	case "item_tag":
		parts, valid := projectionPairParts(key.id)
		if !valid {
			return nil, ErrDomainState
		}
		var value struct {
			ItemID string `json:"item_id"`
			TagID  string `json:"tag_id"`
		}
		if err := tx.QueryRowContext(ctx, "SELECT item_id,tag_id FROM item_tags WHERE item_id=? AND tag_id=?", parts[0], parts[1]).Scan(&value.ItemID, &value.TagID); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	default:
		return nil, ErrDomainState
	}
}
