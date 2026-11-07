package store

import (
	"database/sql"
	"encoding/json"
)

// Build each payload once from final authoritative state. Search publication also
// rebuilds its derived FTS row here, never copying an unverified historical cache.
func (m *mutationTx) projectionPayload(key projectionKey) ([]byte, error) {
	switch key.kind {
	case "location":
		value, err := loadLocation(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.LocationData)
	case "location_event":
		var movement locationMovement
		var from, to sql.NullString
		err := m.tx.QueryRowContext(m.ctx, `SELECT id,location_id,occurred_at,from_parent_id,to_parent_id FROM location_events WHERE id=?`, key.id).
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
		value, err := loadCategory(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.CategoryData)
	case "tag":
		value, err := loadTag(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.TagData)
	case "item":
		value, err := loadItem(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.ItemData)
	case "unit":
		value, err := loadUnit(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.UnitData)
	case "stock":
		parts, valid := projectionPairParts(key.id)
		if !valid {
			return nil, ErrDomainState
		}
		value, err := loadStock(m.ctx, m.tx, parts[0], parts[1])
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.StockData)
	case "inventory_event":
		value, err := loadInventoryEvent(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	case "purchase":
		value, err := loadPurchase(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.PurchaseData)
	case "purchase_line":
		value, err := loadPurchaseLine(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		active, err := purchaseLineIsActive(m.ctx, m.tx, value)
		if err != nil {
			return nil, err
		}
		if !active {
			return nil, ErrNotFound
		}
		return json.Marshal(value.PurchaseLineData)
	case "asset":
		value, err := loadAsset(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.AssetData)
	case "usage":
		value, err := loadUsage(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.UsageData)
	case "cost":
		value, err := loadCost(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.CostData)
	case "disposal":
		value, err := loadDisposal(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.DisposalData)
	case "barcode":
		value, err := loadBarcode(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.BarcodeData)
	case "alias":
		value, err := loadAlias(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.AliasData)
	case "attribute":
		value, err := loadAttribute(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.AttributeData)
	case "item_tag":
		parts, valid := projectionPairParts(key.id)
		if !valid {
			return nil, ErrDomainState
		}
		var value struct {
			ItemID string `json:"item_id"`
			TagID  string `json:"tag_id"`
		}
		if err := m.tx.QueryRowContext(m.ctx, "SELECT item_id,tag_id FROM item_tags WHERE item_id=? AND tag_id=?", parts[0], parts[1]).Scan(&value.ItemID, &value.TagID); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	case "search_doc":
		value, err := m.materializeSearch(key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	default:
		return nil, ErrDomainState
	}
}
