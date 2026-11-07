package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

var ErrPurchaseLineVoided = errors.New("purchase attribution was already financially voided")

// A correction voids an actual active attribution and may post a new Line. The
// original row/owner remains history; explicit JSON null is enforced by HTTP.
type PurchaseLineCorrectionCreate struct {
	LineID       string              `json:"line_id"`
	LineRevision int64               `json:"line_revision"`
	ItemRevision int64               `json:"item_revision"`
	Action       string              `json:"action"`
	Replacement  *PurchaseLineCreate `json:"replacement"`
	Reason       string              `json:"reason"`
}

type PurchaseLineCorrection struct {
	ID         string        `json:"id"`
	Revision   int64         `json:"revision"`
	PurchaseID string        `json:"purchase_id"`
	Action     string        `json:"action"`
	Reason     string        `json:"reason"`
	CreatedAt  string        `json:"created_at"`
	Before     PurchaseLine  `json:"before"`
	After      *PurchaseLine `json:"after"`
}

type PurchaseLineCorrectionList = metadataList[PurchaseLineCorrection]

type purchaseLineCorrectionContext struct {
	Before          PurchaseLine                 `json:"before"`
	After           *PurchaseLine                `json:"after"`
	Input           PurchaseLineCorrectionCreate `json:"input"`
	Purchase        Purchase                     `json:"purchase"`
	Item            Item                         `json:"item"`
	ReplacementItem *Item                        `json:"replacement_item"`
}

func validPurchaseLineCorrection(input PurchaseLineCorrectionCreate) bool {
	return validDomainID(input.LineID) && input.LineRevision >= 1 && input.LineRevision <= maxSafeInteger &&
		input.ItemRevision >= 1 && input.ItemRevision <= maxSafeInteger && authString(input.Reason, 1, 10000) &&
		(input.Action == "void" && input.Replacement == nil || input.Action == "replace" && input.Replacement != nil && validPurchaseLineCreate(*input.Replacement))
}

func (value PurchaseLineCorrection) validateDomainResult() error {
	if !validDomainID(value.ID) || value.Revision != 1 || !validDomainID(value.PurchaseID) || !authString(value.Reason, 1, 10000) ||
		!validPurchaseInstant(value.CreatedAt) || value.Before.validateDomainResult() != nil || value.Before.PurchaseID != value.PurchaseID ||
		value.Before.Revision == maxSafeInteger {
		return ErrDomainState
	}
	if value.Action == "void" && value.After == nil {
		return nil
	}
	if value.Action != "replace" || value.After == nil || value.After.validateDomainResult() != nil ||
		value.After.ID == value.Before.ID || value.After.PurchaseID != value.PurchaseID || value.After.Revision != 1 {
		return ErrDomainState
	}
	return nil
}

const purchaseLineCorrectionSelect = `SELECT c.mutation_event_id,c.revision,c.purchase_id,c.line_id,c.replacement_line_id,
 c.action,c.reason,c.created_at,c.context_json FROM purchase_line_corrections c`

func decodePurchaseLineCorrectionContext(encoded string) (purchaseLineCorrectionContext, error) {
	var value purchaseLineCorrectionContext
	if !utf8.ValidString(encoded) {
		return value, ErrDomainState
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return value, ErrDomainState
	}
	// Nullable source decisions must not silently become zero values after a
	// truncated private context. The producer always writes all these fields.
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(encoded), &fields) != nil {
		return value, ErrDomainState
	}
	for _, name := range []string{"before", "after", "input", "purchase", "item", "replacement_item"} {
		if fields[name] == nil {
			return value, ErrDomainState
		}
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(fields["input"], &input) != nil || input["replacement"] == nil {
		return value, ErrDomainState
	}
	lineFields := []string{"id", "purchase_id", "item_id", "item_unit_id", "quantity_base", "paid_minor", "note", "revision"}
	for _, object := range []struct {
		raw      json.RawMessage
		required []string
	}{
		{fields["before"], lineFields}, {fields["after"], lineFields},
		{input["replacement"], []string{"item_id", "item_revision", "item_unit_id", "quantity_base", "paid_minor", "note"}},
	} {
		if bytes.Equal(bytes.TrimSpace(object.raw), []byte("null")) {
			continue
		}
		var members map[string]json.RawMessage
		if json.Unmarshal(object.raw, &members) != nil {
			return value, ErrDomainState
		}
		for _, name := range object.required {
			if members[name] == nil {
				return value, ErrDomainState
			}
		}
	}
	return value, nil
}

func scanPurchaseLineCorrection(ctx context.Context, query queryer, row rowScanner, original *PurchaseLine) (value PurchaseLineCorrection, finalErr error) {
	var lineID, encoded string
	var replacement sql.NullString
	if err := row.Scan(&value.ID, &value.Revision, &value.PurchaseID, &lineID, &replacement,
		&value.Action, &value.Reason, &value.CreatedAt, &encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, ErrNotFound
		}
		return value, err
	}
	// Only an absent correction row means active. Missing referenced source must
	// never be mistaken for that absence and resurrect the original attribution.
	defer func() {
		if errors.Is(finalErr, ErrNotFound) || errors.Is(finalErr, sql.ErrNoRows) {
			finalErr = ErrDomainState
		}
	}()
	stored, err := decodePurchaseLineCorrectionContext(encoded)
	if err != nil {
		return value, err
	}
	value.Before, value.After = stored.Before, stored.After
	if value.validateDomainResult() != nil || !validPurchaseLineCorrection(stored.Input) || stored.Purchase.validateDomainResult() != nil ||
		stored.Item.validateDomainResult() != nil || value.Before.ID != lineID || value.PurchaseID != stored.Purchase.ID ||
		stored.Input.LineID != lineID || stored.Input.LineRevision != value.Before.Revision || stored.Input.ItemRevision != stored.Item.Revision ||
		stored.Input.Action != value.Action || stored.Input.Reason != value.Reason || stored.Item.ID != value.Before.ItemID ||
		replacement.Valid != (value.After != nil) || replacement.Valid && replacement.String != value.After.ID {
		return value, ErrDomainState
	}
	if original == nil {
		loaded, err := loadPurchaseLine(ctx, query, lineID)
		if err != nil {
			return value, err
		}
		original = &loaded
	}
	if original.Revision != value.Before.Revision+1 || !reflect.DeepEqual(original.PurchaseLineData, value.Before.PurchaseLineData) {
		return value, ErrDomainState
	}
	purchase, err := loadPurchase(ctx, query, value.PurchaseID)
	if err != nil {
		return value, err
	}
	item, err := loadItem(ctx, query, stored.Item.ID)
	if err != nil {
		return value, err
	}
	if purchase.Revision <= stored.Purchase.Revision || purchase.Currency != stored.Purchase.Currency || item.Revision <= stored.Item.Revision ||
		!samePurchaseItemInterpretation(item, stored.Item) {
		return value, ErrDomainState
	}
	if value.After == nil {
		if stored.ReplacementItem != nil {
			return value, ErrDomainState
		}
		return value, nil
	}
	if stored.ReplacementItem == nil || stored.ReplacementItem.validateDomainResult() != nil ||
		stored.ReplacementItem.ID != stored.Input.Replacement.ItemID || stored.ReplacementItem.Revision != stored.Input.Replacement.ItemRevision {
		return value, ErrDomainState
	}
	after, err := loadPurchaseLine(ctx, query, value.After.ID)
	if err != nil {
		return value, err
	}
	target, err := loadItem(ctx, query, after.ItemID)
	if err != nil {
		return value, err
	}
	expected := PurchaseLineData{value.After.ID, value.PurchaseID, stored.Input.Replacement.ItemID, stored.Input.Replacement.ItemUnitID,
		stored.Input.Replacement.QuantityBase, stored.Input.Replacement.PaidMinor, stored.Input.Replacement.Note}
	if !reflect.DeepEqual(value.After.PurchaseLineData, expected) || !reflect.DeepEqual(after.PurchaseLineData, expected) ||
		target.Revision <= stored.ReplacementItem.Revision || !samePurchaseItemInterpretation(target, *stored.ReplacementItem) {
		return value, ErrDomainState
	}
	var created, event sql.NullString
	if err = query.QueryRowContext(ctx, "SELECT created_at,mutation_event_id FROM purchase_lines WHERE id=?", after.ID).Scan(&created, &event); err != nil {
		return value, err
	}
	if !created.Valid || !event.Valid || created.String != value.CreatedAt || event.String != value.ID {
		return value, ErrDomainState
	}
	return value, nil
}

func samePurchaseItemInterpretation(current, original Item) bool {
	return current.TrackingMode == original.TrackingMode && current.UnitCode == original.UnitCode &&
		current.BaseUnit == original.BaseUnit && current.ScaleFactor == original.ScaleFactor
}

// Classify actual retained source, not current projections or an unchecked SQL
// anti-join that could silently hide a corrupt correction from every consumer.
func purchaseLineIsActive(ctx context.Context, query queryer, line PurchaseLine) (bool, error) {
	_, err := scanPurchaseLineCorrection(ctx, query, query.QueryRowContext(ctx, purchaseLineCorrectionSelect+" WHERE c.line_id=?", line.ID), &line)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	return false, err
}

func loadPurchaseLineCorrection(ctx context.Context, query queryer, id string) (PurchaseLineCorrection, error) {
	return scanPurchaseLineCorrection(ctx, query, query.QueryRowContext(ctx, purchaseLineCorrectionSelect+" WHERE c.mutation_event_id=?", id), nil)
}

func (p *Purchases) PageLineCorrections(ctx context.Context, token, purchaseID, cursor string, limit int) (PurchaseLineCorrectionList, error) {
	if !validDomainID(purchaseID) {
		return PurchaseLineCorrectionList{}, ErrDomainInput
	}
	purpose := "purchase-line-corrections-list-cursor-v1\x00" + projectionPair("line-corrections", purchaseID)
	return readMetadataPage(ctx, p.auth, token, cursor, limit, purpose,
		func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[PurchaseLineCorrection], error) {
			if _, err := loadPurchase(ctx, tx, purchaseID); err != nil {
				return metadataPage[PurchaseLineCorrection]{}, err
			}
			rows, err := tx.QueryContext(ctx, purchaseLineCorrectionSelect+" WHERE c.purchase_id=? AND c.mutation_event_id>? ORDER BY c.mutation_event_id LIMIT ?", purchaseID, after, size+1)
			if err != nil {
				return metadataPage[PurchaseLineCorrection]{}, err
			}
			defer rows.Close()
			page := metadataPage[PurchaseLineCorrection]{Data: make([]PurchaseLineCorrection, 0)}
			for rows.Next() {
				value, err := scanPurchaseLineCorrection(ctx, tx, rows, nil)
				if err != nil {
					return metadataPage[PurchaseLineCorrection]{}, err
				}
				if value.PurchaseID != purchaseID {
					return metadataPage[PurchaseLineCorrection]{}, ErrDomainState
				}
				page.Data = append(page.Data, value)
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				return metadataPage[PurchaseLineCorrection]{}, err
			}
			if len(page.Data) > size {
				page.Data = page.Data[:size]
				page.NextID = page.Data[size-1].ID
			}
			return page, nil
		})
}

func (p *Purchases) CorrectLine(ctx context.Context, token, key, purchaseID string, revision int64, input PurchaseLineCorrectionCreate) (PurchaseLineCorrection, error) {
	if input.Replacement != nil {
		copy := *input.Replacement
		copy.ItemUnitID = snapshotPurchaseString(copy.ItemUnitID)
		input.Replacement = &copy
	}
	if !validDomainID(purchaseID) || !validPurchaseLineCorrection(input) {
		return PurchaseLineCorrection{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return PurchaseLineCorrection{}, err
	}
	request := struct {
		ID       string                       `json:"id"`
		Revision int64                        `json:"revision"`
		Input    PurchaseLineCorrectionCreate `json:"input"`
	}{purchaseID, revision, input}
	return runDomainMutation(ctx, p.auth, token, key, "post_purchases_id_line_corrections", request, func(m *mutationTx) (PurchaseLineCorrection, error) {
		purchase, err := loadPurchase(ctx, m.tx, purchaseID)
		if err != nil {
			return PurchaseLineCorrection{}, err
		}
		if purchase.Revision != revision {
			return PurchaseLineCorrection{}, ErrRevisionConflict
		}
		line, err := loadPurchaseLine(ctx, m.tx, input.LineID)
		if err != nil {
			return PurchaseLineCorrection{}, err
		}
		if line.PurchaseID != purchaseID {
			return PurchaseLineCorrection{}, ErrNotFound
		}
		active, err := purchaseLineIsActive(ctx, m.tx, line)
		if err != nil {
			return PurchaseLineCorrection{}, err
		}
		if !active {
			return PurchaseLineCorrection{}, ErrPurchaseLineVoided
		}
		if line.Revision != input.LineRevision {
			return PurchaseLineCorrection{}, ErrRevisionConflict
		}
		item, err := itemAtRevision(ctx, m.tx, line.ItemID, input.ItemRevision)
		if err != nil {
			return PurchaseLineCorrection{}, err
		}
		var oldAsset, newAsset *Asset
		if line.ItemUnitID != nil {
			value, err := loadAsset(ctx, m.tx, *line.ItemUnitID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return PurchaseLineCorrection{}, err
			}
			if err == nil {
				oldAsset = &value
			}
		}
		context := purchaseLineCorrectionContext{Before: line, Input: input, Purchase: purchase, Item: item}
		if input.Replacement != nil {
			target, asset, err := m.preparePurchaseLine(purchase, *input.Replacement)
			if err != nil {
				return PurchaseLineCorrection{}, err
			}
			after, err := m.insertPurchaseLine(purchaseID, *input.Replacement)
			if err != nil {
				return PurchaseLineCorrection{}, err
			}
			context.After, context.ReplacementItem, newAsset = &after, &target, asset
		}
		encoded, err := json.Marshal(context)
		if err != nil {
			return PurchaseLineCorrection{}, err
		}
		var replacementID *string
		if context.After != nil {
			replacementID = &context.After.ID
		}
		m.reason = input.Reason
		if err = requireDomainRow(m.tx.ExecContext(ctx, `INSERT INTO purchase_line_corrections(mutation_event_id,revision,purchase_id,line_id,replacement_line_id,action,reason,created_at,context_json)
 VALUES(?,1,?,?,?,?,?,?,?)`, m.eventID, purchaseID, line.ID, replacementID, input.Action, input.Reason, stamp(m.now), string(encoded))); err != nil {
			return PurchaseLineCorrection{}, err
		}
		if _, err = m.touch("purchase_line", line.ID, true); err != nil {
			return PurchaseLineCorrection{}, err
		}
		if _, err = m.touch("purchase", purchaseID, false); err != nil {
			return PurchaseLineCorrection{}, err
		}
		if err = m.touchItemSearch(item.ID, true); err != nil {
			return PurchaseLineCorrection{}, err
		}
		if context.ReplacementItem != nil && context.ReplacementItem.ID != item.ID {
			if err = m.touchItemSearch(context.ReplacementItem.ID, true); err != nil {
				return PurchaseLineCorrection{}, err
			}
		}
		for index, asset := range []*Asset{oldAsset, newAsset} {
			if asset == nil || index == 1 && oldAsset != nil && asset.ItemUnitID == oldAsset.ItemUnitID {
				continue
			}
			if err = m.finishAssetEvent(*asset); err != nil {
				return PurchaseLineCorrection{}, err
			}
			stored, err := loadAsset(ctx, m.tx, asset.ItemUnitID)
			if err != nil {
				return PurchaseLineCorrection{}, err
			}
			if stored.Revision != asset.Revision+1 || !reflect.DeepEqual(stored.AssetData, asset.AssetData) {
				return PurchaseLineCorrection{}, ErrDomainState
			}
		}
		current, err := loadPurchase(ctx, m.tx, purchaseID)
		if err != nil {
			return PurchaseLineCorrection{}, err
		}
		if current.Revision != purchase.Revision+1 || !reflect.DeepEqual(current.PurchaseData, purchase.PurchaseData) {
			return PurchaseLineCorrection{}, ErrDomainState
		}
		for _, original := range []*Item{&item, context.ReplacementItem} {
			if original == nil {
				continue
			}
			stored, err := loadItem(ctx, m.tx, original.ID)
			if err != nil {
				return PurchaseLineCorrection{}, err
			}
			if stored.Revision != original.Revision+1 || !reflect.DeepEqual(stored.ItemData, original.ItemData) {
				return PurchaseLineCorrection{}, ErrDomainState
			}
		}
		if context.After != nil {
			stored, err := loadPurchaseLine(ctx, m.tx, context.After.ID)
			if err != nil {
				return PurchaseLineCorrection{}, err
			}
			active, err := purchaseLineIsActive(ctx, m.tx, stored)
			if err != nil {
				return PurchaseLineCorrection{}, err
			}
			if !active || !reflect.DeepEqual(stored, *context.After) {
				return PurchaseLineCorrection{}, ErrDomainState
			}
		}
		stored, err := loadPurchaseLineCorrection(ctx, m.tx, m.eventID)
		if err != nil {
			return PurchaseLineCorrection{}, err
		}
		expected := PurchaseLineCorrection{m.eventID, 1, purchaseID, input.Action, input.Reason, stamp(m.now), line, context.After}
		var sourceContext string
		if err = m.tx.QueryRowContext(ctx, "SELECT context_json FROM purchase_line_corrections WHERE mutation_event_id=?", m.eventID).Scan(&sourceContext); err != nil {
			return PurchaseLineCorrection{}, err
		}
		if sourceContext != string(encoded) || !reflect.DeepEqual(stored, expected) {
			return PurchaseLineCorrection{}, ErrDomainState
		}
		return stored, nil
	})
}
