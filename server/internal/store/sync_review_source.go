package store

import (
	"context"
	"database/sql"
	"errors"
)

// syncReviewData contains only the version-one offline review fields. Unreviewed
// and rejected raw OCR is deliberately not selected into this representation.
type syncReviewData struct {
	ID             string  `json:"id"`
	ItemID         string  `json:"item_id"`
	State          string  `json:"state"`
	EffectiveValue *string `json:"effective_value"`
	FieldKey       string  `json:"field_key"`
	MediaID        *string `json:"media_id"`
}

type syncReviewSource struct {
	Data     syncReviewData
	Revision int64
}

func loadSyncReviewSource(ctx context.Context, tx *sql.Tx, id string) (syncReviewSource, error) {
	if ctx == nil || tx == nil {
		return syncReviewSource{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncReviewSource{}, err
	}
	if !validDomainID(id) {
		return syncReviewSource{}, ErrDomainState
	}
	var value syncReviewSource
	var owner, media, field, state, reviewed, effective sql.NullString
	var mediaNull, reviewedNull bool
	err := tx.QueryRowContext(ctx, `SELECT v.id,
 CASE WHEN length(CAST(v.item_id AS BLOB))<=512 THEN v.item_id END,
 CASE WHEN length(CAST(v.media_id AS BLOB))<=512 THEN v.media_id END,v.media_id IS NULL,
 CASE WHEN length(CAST(v.field_key AS BLOB))<=400 THEN v.field_key END,
 CASE WHEN length(CAST(v.state AS BLOB))<=12 THEN v.state END,
 CASE WHEN length(CAST(v.reviewed_at AS BLOB))<=40000 THEN v.reviewed_at END,v.reviewed_at IS NULL,
 CASE WHEN v.state='accepted' AND length(CAST(v.raw_value AS BLOB))<=40000 THEN v.raw_value
      WHEN v.state='corrected' AND length(CAST(v.corrected_value AS BLOB))<=40000 THEN v.corrected_value END,
 COALESCE(r.revision,0) FROM ai_review_entries v
 LEFT JOIN projection_revisions r ON r.entity_type='ai_review' AND r.entity_id=v.id WHERE v.id=?`, id).
		Scan(&value.Data.ID, &owner, &media, &mediaNull, &field, &state, &reviewed, &reviewedNull, &effective, &value.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return syncReviewSource{}, ErrNotFound
	}
	if err != nil {
		return syncReviewSource{}, err
	}
	if value.Data.ID != id || !owner.Valid || !validDomainID(owner.String) || !field.Valid || !authString(field.String, 1, 100) ||
		!state.Valid || value.Revision < 1 || value.Revision > maxSafeInteger ||
		!mediaNull && (!media.Valid || !validDomainID(media.String)) ||
		!reviewedNull && (!reviewed.Valid || !authString(reviewed.String, 1, 10000)) {
		return syncReviewSource{}, ErrDomainState
	}
	value.Data.ItemID, value.Data.FieldKey, value.Data.State = owner.String, field.String, state.String
	if !mediaNull {
		value.Data.MediaID = &media.String
	}
	switch value.Data.State {
	case "needs_review":
	case "rejected":
		if reviewedNull {
			return syncReviewSource{}, ErrDomainState
		}
	case "accepted", "corrected":
		if reviewedNull || !effective.Valid || !authString(effective.String, 0, 10000) {
			return syncReviewSource{}, ErrDomainState
		}
		value.Data.EffectiveValue = &effective.String
	default:
		return syncReviewSource{}, ErrDomainState
	}
	if _, err = loadItem(ctx, tx, value.Data.ItemID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return syncReviewSource{}, ErrDomainState
		}
		return syncReviewSource{}, err
	}
	if value.Data.MediaID != nil {
		if _, err = loadMedia(ctx, tx, *value.Data.MediaID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return syncReviewSource{}, ErrDomainState
			}
			return syncReviewSource{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return syncReviewSource{}, err
	}
	return value, nil
}
