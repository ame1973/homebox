package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"homebox/internal/currency"
)

// The subject proof remains readable after financial void. Original data has no
// invented original revision/provenance; Current null is not zero proceeds.
type DisposalRecord struct {
	ItemUnitID string        `json:"item_unit_id"`
	Revision   int64         `json:"revision"`
	Original   DisposalData  `json:"original"`
	Current    *DisposalData `json:"current"`
}

// No Unit/Item proof or physical effect belongs in a financial replacement.
// HTTP must independently require explicit proceeds and nullable replacement.
type DisposalFinancialWrite struct {
	Kind                  string `json:"kind"`
	DisposedAt            string `json:"disposed_at"`
	RealizedProceedsMinor int64  `json:"realized_proceeds_minor"`
	Currency              string `json:"currency"`
	Note                  string `json:"note"`
}

type DisposalCorrectionWrite struct {
	AssetRevision int64                   `json:"asset_revision"`
	Action        string                  `json:"action"`
	Replacement   *DisposalFinancialWrite `json:"replacement"`
	Reason        string                  `json:"reason"`
}

type DisposalCorrection struct {
	ID         string         `json:"id"`
	Revision   int64          `json:"revision"`
	ItemUnitID string         `json:"item_unit_id"`
	Action     string         `json:"action"`
	Reason     string         `json:"reason"`
	CreatedAt  string         `json:"created_at"`
	Before     DisposalRecord `json:"before"`
	After      DisposalRecord `json:"after"`
}

type DisposalCorrectionList = metadataList[DisposalCorrection]

type disposalCorrectionContext struct {
	PreviousID *string                 `json:"previous_id"`
	Before     DisposalRecord          `json:"before"`
	After      DisposalRecord          `json:"after"`
	Input      DisposalCorrectionWrite `json:"input"`
	Asset      Asset                   `json:"asset"`
	Item       Item                    `json:"item"`
}

func (value DisposalRecord) validateDomainResult() error {
	if value.ItemUnitID != value.Original.ItemUnitID || (Disposal{value.Original, value.Revision}).validateDomainResult() != nil ||
		value.Current != nil && (value.Current.ItemUnitID != value.ItemUnitID || (Disposal{*value.Current, value.Revision}).validateDomainResult() != nil) {
		return ErrDomainState
	}
	return nil
}

func validDisposalCorrection(input DisposalCorrectionWrite) bool {
	if input.AssetRevision < 1 || input.AssetRevision > maxSafeInteger || !authString(input.Reason, 1, 10000) {
		return false
	}
	if input.Action == "void" {
		return input.Replacement == nil
	}
	if input.Action != "replace" || input.Replacement == nil {
		return false
	}
	v := input.Replacement
	data := DisposalData{"validation", v.Kind, v.DisposedAt, v.RealizedProceedsMinor, v.Currency, v.Note}
	return (Disposal{data, 1}).validateDomainResult() == nil && (v.Kind == "sold" || v.RealizedProceedsMinor == 0)
}

func (value DisposalCorrection) validateDomainResult() error {
	if !validDomainID(value.ID) || value.Revision != 1 || !authString(value.Reason, 1, 10000) || !validPurchaseInstant(value.CreatedAt) ||
		value.Before.validateDomainResult() != nil || value.After.validateDomainResult() != nil || value.ItemUnitID != value.Before.ItemUnitID ||
		value.ItemUnitID != value.After.ItemUnitID || value.Before.Original != value.After.Original || value.Before.Revision == maxSafeInteger ||
		value.After.Revision != value.Before.Revision+1 {
		return ErrDomainState
	}
	if value.Action == "void" && value.Before.Current != nil && value.After.Current == nil || value.Action == "replace" && value.After.Current != nil {
		return nil
	}
	return ErrDomainState
}

func disposalReplacement(before DisposalRecord, input DisposalFinancialWrite) DisposalData {
	retained := before.Original.DisposedAt
	if before.Current != nil {
		retained = before.Current.DisposedAt
	}
	return DisposalData{before.ItemUnitID, input.Kind, *canonicalAssetInstant(&input.DisposedAt, &retained), input.RealizedProceedsMinor, input.Currency, input.Note}
}

const disposalCorrectionSelect = `SELECT c.mutation_event_id,c.revision,c.item_unit_id,c.subject_revision,
 c.action,c.reason,c.created_at,c.context_json,c.previous_correction_id FROM asset_disposal_corrections c`

func decodeDisposalCorrectionContext(encoded string) (disposalCorrectionContext, error) {
	var value disposalCorrectionContext
	if !utf8.ValidString(encoded) {
		return value, ErrDomainState
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return value, ErrDomainState
	}
	// This new private journal has only canonical producer-written JSON, never
	// imported legacy contexts. Exact re-encoding checks every required nullable
	// and zero-valued field, duplicate keys and repaired Unicode without defaults.
	canonical, err := json.Marshal(value)
	if err != nil || string(canonical) != encoded {
		return value, ErrDomainState
	}
	return value, nil
}

func scanDisposalCorrection(row rowScanner) (DisposalCorrection, disposalCorrectionContext, error) {
	var value DisposalCorrection
	var subjectRevision int64
	var encoded string
	var previous sql.NullString
	if err := row.Scan(&value.ID, &value.Revision, &value.ItemUnitID, &subjectRevision, &value.Action, &value.Reason, &value.CreatedAt, &encoded, &previous); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return value, disposalCorrectionContext{}, err
	}
	stored, err := decodeDisposalCorrectionContext(encoded)
	if err != nil {
		return value, stored, err
	}
	value.Before, value.After = stored.Before, stored.After
	if value.validateDomainResult() != nil || !validDisposalCorrection(stored.Input) || stored.Asset.validateDomainResult() != nil || stored.Item.validateDomainResult() != nil ||
		value.After.Revision != subjectRevision || stored.Input.Action != value.Action || stored.Input.Reason != value.Reason ||
		stored.Input.AssetRevision != stored.Asset.Revision || stored.Asset.ItemUnitID != value.ItemUnitID || stored.Item.TrackingMode != "individual" ||
		previous.Valid != (stored.PreviousID != nil) || previous.Valid && (previous.String != *stored.PreviousID || !validDomainID(previous.String) || previous.String == value.ID) {
		return value, stored, ErrDomainState
	}
	if stored.Input.Replacement != nil {
		expected := disposalReplacement(value.Before, *stored.Input.Replacement)
		if value.After.Current == nil || *value.After.Current != expected || expected.Currency != stored.Asset.Currency {
			return value, stored, ErrDomainState
		}
	}
	return value, stored, nil
}

// Stream the complete actual chain once, in subject-revision order. An unchecked
// last-row lookup could hide a broken middle or resurrect a financially voided
// original. Stable-ID history pagination remains independent of this ordering.
func loadDisposalRecord(ctx context.Context, query queryer, id string) (DisposalRecord, error) {
	original, err := scanDisposal(query.QueryRowContext(ctx, disposalSelect+" WHERE d.item_unit_id=?", id))
	if errors.Is(err, ErrNotFound) {
		var retained bool
		if err = query.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM asset_disposal_corrections WHERE item_unit_id=?)", id).Scan(&retained); err != nil {
			return DisposalRecord{}, err
		}
		if retained {
			return DisposalRecord{}, ErrDomainState
		}
		return DisposalRecord{}, ErrNotFound
	}
	if err != nil {
		return DisposalRecord{}, err
	}
	asset, err := loadAsset(ctx, query, id)
	if errors.Is(err, ErrNotFound) {
		err = ErrDomainState
	}
	if err != nil {
		return DisposalRecord{}, err
	}
	unit, err := loadUnit(ctx, query, id)
	if err != nil {
		return DisposalRecord{}, err
	}
	item, err := loadItem(ctx, query, unit.ItemID)
	if err != nil {
		return DisposalRecord{}, err
	}
	rows, err := query.QueryContext(ctx, disposalCorrectionSelect+" WHERE c.item_unit_id=? ORDER BY c.subject_revision", id)
	if err != nil {
		return DisposalRecord{}, err
	}
	defer rows.Close()
	current := original.DisposalData
	record := DisposalRecord{id, original.Revision, current, &current}
	first := true
	var previous *string
	for rows.Next() {
		entry, stored, err := scanDisposalCorrection(rows)
		if err != nil {
			return DisposalRecord{}, err
		}
		if first {
			// The first retained proof need not be one. Never reconstruct an
			// unknown original revision from legacy creation/projection history.
			record.Revision, first = entry.Before.Revision, false
		}
		if !sameParent(stored.PreviousID, previous) || !reflect.DeepEqual(entry.Before, record) || entry.ItemUnitID != id || stored.Item.ID != item.ID ||
			asset.Revision <= stored.Asset.Revision || item.Revision <= stored.Item.Revision {
			return DisposalRecord{}, ErrDomainState
		}
		record, previous = entry.After, &entry.ID
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return DisposalRecord{}, err
	}
	if record.Revision != original.Revision {
		return DisposalRecord{}, ErrDomainState
	}
	return record, nil
}

func (a *Assets) GetDisposalRecord(ctx context.Context, token, id string) (DisposalRecord, error) {
	if !validDomainID(id) {
		return DisposalRecord{}, ErrDomainInput
	}
	return readDomain(ctx, a.auth, token, func(tx *sql.Tx) (DisposalRecord, error) { return loadDisposalRecord(ctx, tx, id) })
}

func (a *Assets) PageDisposalCorrections(ctx context.Context, token, id, cursor string, limit int) (DisposalCorrectionList, error) {
	if !validDomainID(id) {
		return DisposalCorrectionList{}, ErrDomainInput
	}
	purpose := "disposal-corrections-list-cursor-v1\x00" + projectionPair("disposal-corrections", id)
	return readMetadataPage(ctx, a.auth, token, cursor, limit, purpose,
		func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[DisposalCorrection], error) {
			if _, err := loadDisposalRecord(ctx, tx, id); err != nil {
				return metadataPage[DisposalCorrection]{}, err
			}
			rows, err := tx.QueryContext(ctx, disposalCorrectionSelect+" WHERE c.item_unit_id=? AND c.mutation_event_id>? ORDER BY c.mutation_event_id LIMIT ?", id, after, size+1)
			if err != nil {
				return metadataPage[DisposalCorrection]{}, err
			}
			defer rows.Close()
			page := metadataPage[DisposalCorrection]{Data: make([]DisposalCorrection, 0)}
			for rows.Next() {
				entry, _, err := scanDisposalCorrection(rows)
				if err != nil {
					return metadataPage[DisposalCorrection]{}, err
				}
				if entry.ItemUnitID != id {
					return metadataPage[DisposalCorrection]{}, ErrDomainState
				}
				page.Data = append(page.Data, entry)
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				return metadataPage[DisposalCorrection]{}, err
			}
			if len(page.Data) > size {
				page.Data = page.Data[:size]
				page.NextID = page.Data[size-1].ID
			}
			return page, nil
		})
}

func (a *Assets) CorrectDisposal(ctx context.Context, token, key, id string, revision int64, input DisposalCorrectionWrite) (DisposalCorrection, error) {
	if input.Replacement != nil {
		copy := *input.Replacement
		input.Replacement = &copy
	}
	if !validDomainID(id) || !validDisposalCorrection(input) {
		return DisposalCorrection{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return DisposalCorrection{}, err
	}
	request := struct {
		ID       string                  `json:"unit_id"`
		Revision int64                   `json:"disposal_revision"`
		Input    DisposalCorrectionWrite `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, a.auth, token, key, "post_units_id_disposal_corrections", request, func(m *mutationTx) (DisposalCorrection, error) {
		before, err := loadDisposalRecord(ctx, m.tx, id)
		if err != nil {
			return DisposalCorrection{}, err
		}
		if before.Revision != revision {
			return DisposalCorrection{}, ErrRevisionConflict
		}
		asset, err := assetAtRevision(ctx, m.tx, id, input.AssetRevision)
		if err != nil {
			return DisposalCorrection{}, err
		}
		unit, err := loadUnit(ctx, m.tx, id)
		if err != nil {
			return DisposalCorrection{}, err
		}
		item, err := loadItem(ctx, m.tx, unit.ItemID)
		if err != nil {
			return DisposalCorrection{}, err
		}
		if input.Action == "void" && before.Current == nil {
			return DisposalCorrection{}, ErrAssetHistory
		}
		after := before
		after.Current = nil
		if input.Replacement != nil {
			if _, ok := currency.MinorUnits(input.Replacement.Currency); !ok {
				return DisposalCorrection{}, ErrAssetCurrency
			}
			if input.Replacement.Currency != asset.Currency {
				return DisposalCorrection{}, ErrDomainInput
			}
			data := disposalReplacement(before, *input.Replacement)
			after.Current = &data
			m.note = input.Replacement.Note
		}
		after.Revision, err = m.touch("disposal", id, after.Current == nil)
		if err != nil {
			return DisposalCorrection{}, err
		}
		if after.Revision != before.Revision+1 {
			return DisposalCorrection{}, ErrDomainState
		}
		// Advance actual aggregates before reading the proposed chain. The shared
		// finalizer coalesces these identities and validates complete proposed
		// totals after the journal exists, not an unsafe old derived summary.
		if _, err = m.touch("asset", id, false); err != nil {
			return DisposalCorrection{}, err
		}
		if _, err = m.touch("item", item.ID, false); err != nil {
			return DisposalCorrection{}, err
		}
		var previous sql.NullString
		err = m.tx.QueryRowContext(ctx, "SELECT mutation_event_id FROM asset_disposal_corrections WHERE item_unit_id=? AND subject_revision=?", id, before.Revision).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return DisposalCorrection{}, err
		}
		stored := disposalCorrectionContext{Before: before, After: after, Input: input, Asset: asset, Item: item}
		if previous.Valid {
			stored.PreviousID = &previous.String
		}
		encoded, err := json.Marshal(stored)
		if err != nil {
			return DisposalCorrection{}, err
		}
		m.reason = input.Reason
		if err = m.execAssetRow(`INSERT INTO asset_disposal_corrections(mutation_event_id,revision,item_unit_id,previous_correction_id,subject_revision,action,reason,created_at,context_json)
 VALUES(?,1,?,?,?,?,?,?,?)`, m.eventID, id, stored.PreviousID, after.Revision, input.Action, input.Reason, stamp(m.now), string(encoded)); err != nil {
			return DisposalCorrection{}, err
		}
		if err = m.finishAssetEvent(asset); err != nil {
			return DisposalCorrection{}, err
		}
		finalAsset, err := loadAsset(ctx, m.tx, id)
		if err != nil {
			return DisposalCorrection{}, err
		}
		finalItem, err := loadItem(ctx, m.tx, item.ID)
		if err != nil {
			return DisposalCorrection{}, err
		}
		finalUnit, err := loadUnit(ctx, m.tx, id)
		if err != nil {
			return DisposalCorrection{}, err
		}
		if finalAsset.Revision != asset.Revision+1 || !reflect.DeepEqual(finalAsset.AssetData, asset.AssetData) ||
			finalItem.Revision != item.Revision+1 || !reflect.DeepEqual(finalItem.ItemData, item.ItemData) || !reflect.DeepEqual(finalUnit, unit) {
			return DisposalCorrection{}, ErrDomainState
		}
		actual, err := loadDisposalRecord(ctx, m.tx, id)
		if err != nil {
			return DisposalCorrection{}, err
		}
		entry, _, err := scanDisposalCorrection(m.tx.QueryRowContext(ctx, disposalCorrectionSelect+" WHERE c.mutation_event_id=?", m.eventID))
		if err != nil {
			return DisposalCorrection{}, err
		}
		var contextJSON string
		if err = m.tx.QueryRowContext(ctx, "SELECT context_json FROM asset_disposal_corrections WHERE mutation_event_id=?", m.eventID).Scan(&contextJSON); err != nil {
			return DisposalCorrection{}, err
		}
		expected := DisposalCorrection{m.eventID, 1, id, input.Action, input.Reason, stamp(m.now), before, after}
		if !reflect.DeepEqual(actual, after) || !reflect.DeepEqual(entry, expected) || contextJSON != string(encoded) {
			return DisposalCorrection{}, ErrDomainState
		}
		return entry, nil
	})
}
