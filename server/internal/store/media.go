package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
)

// Media source metadata is retained exactly; it does not establish that a
// private file exists, matches its claimed digest or has had GPS data removed.
type mediaData struct {
	ID             string  `json:"id"`
	SHA256         string  `json:"sha256"`
	ThumbURL       *string `json:"thumb_url"`
	Width          int64   `json:"width"`
	Height         int64   `json:"height"`
	ThumbSHA256    *string `json:"thumb_sha256"`
	MIMEType       string  `json:"mime_type"`
	Bytes          int64   `json:"bytes"`
	PerceptualHash *string `json:"perceptual_hash"`
}

type retainedMedia struct {
	mediaData
	storageKey   string
	createdAt    string
	Verification string
	Revision     int64
}

type mediaLinkData struct {
	ID          string `json:"id"`
	MediaID     string `json:"media_id"`
	OwnerType   string `json:"owner_type"`
	OwnerID     string `json:"owner_id"`
	IsCover     bool   `json:"is_cover"`
	SortOrder   int64  `json:"sort_order"`
	Description string `json:"description"`
}

type retainedMediaLink struct {
	mediaLinkData
	Revision int64
}

type mediaMember struct {
	link    retainedMediaLink
	payload string
}

const mediaSelect = `SELECT m.id,m.sha256,m.mime_type,m.width,m.height,m.bytes,m.perceptual_hash,m.verification,m.storage_key,m.created_at,COALESCE(r.revision,0)
 FROM media m LEFT JOIN projection_revisions r ON r.entity_type='media' AND r.entity_id=m.id`

const mediaLinkSelect = `SELECT l.id,l.media_id,l.item_id,l.item_unit_id,l.location_id,l.is_cover,l.sort_order,l.description,COALESCE(r.revision,0)
 FROM media_links l LEFT JOIN projection_revisions r ON r.entity_type='media_link' AND r.entity_id=l.id`

func validMediaDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func supportedMediaMIME(value string) bool {
	return value == "image/jpeg" || value == "image/png" || value == "image/webp"
}

func scanMedia(row rowScanner) (retainedMedia, error) {
	var value retainedMedia
	var hash sql.NullString
	if err := row.Scan(&value.ID, &value.SHA256, &value.MIMEType, &value.Width, &value.Height, &value.Bytes,
		&hash, &value.Verification, &value.storageKey, &value.createdAt, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return retainedMedia{}, ErrNotFound
		}
		return retainedMedia{}, err
	}
	if hash.Valid {
		value.PerceptualHash = &hash.String
	}
	if !validDomainID(value.ID) || !validMediaDigest(value.SHA256) || !authString(value.MIMEType, 1, 100) ||
		value.Width < 1 || value.Width > maxSafeInteger || value.Height < 1 || value.Height > maxSafeInteger ||
		value.Bytes < 1 || value.Bytes > maxSafeInteger || (value.PerceptualHash != nil && !authString(*value.PerceptualHash, 16, 16)) ||
		(value.Verification != "unverified" && value.Verification != "verified") ||
		(value.Verification == "verified" && !supportedMediaMIME(value.MIMEType)) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return retainedMedia{}, ErrDomainState
	}
	// No thumbnail storage or byte-verification producer exists yet. Both
	// nullable fields deliberately remain null rather than inventing a file.
	return value, nil
}

func loadMedia(ctx context.Context, query queryer, id string) (retainedMedia, error) {
	return scanMedia(query.QueryRowContext(ctx, mediaSelect+" WHERE m.id=?", id))
}

func scanMediaLink(row rowScanner) (retainedMediaLink, error) {
	var value retainedMediaLink
	var item, unit, location sql.NullString
	var cover int64
	if err := row.Scan(&value.ID, &value.MediaID, &item, &unit, &location, &cover, &value.SortOrder, &value.Description, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return retainedMediaLink{}, ErrNotFound
		}
		return retainedMediaLink{}, err
	}
	owners := 0
	for _, owner := range []struct {
		kind  string
		value sql.NullString
	}{{"item", item}, {"unit", unit}, {"location", location}} {
		if owner.value.Valid {
			owners++
			value.OwnerType, value.OwnerID = owner.kind, owner.value.String
		}
	}
	if owners != 1 || !validDomainID(value.ID) || !validDomainID(value.MediaID) || !validDomainID(value.OwnerID) ||
		(cover != 0 && cover != 1) || value.SortOrder < -maxSafeInteger || value.SortOrder > maxSafeInteger ||
		!authString(value.Description, 0, 10000) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return retainedMediaLink{}, ErrDomainState
	}
	value.IsCover = cover == 1
	return value, nil
}

func mediaAuthorityError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	return err
}

func validateMediaLinkAuthority(ctx context.Context, query queryer, link retainedMediaLink) error {
	if _, err := loadMedia(ctx, query, link.MediaID); err != nil {
		return mediaAuthorityError(err)
	}
	return mediaAuthorityError(loadMediaOwner(ctx, query, link.OwnerType, link.OwnerID))
}

func loadMediaOwner(ctx context.Context, query queryer, ownerType, ownerID string) error {
	var err error
	switch ownerType {
	case "item":
		_, err = loadItem(ctx, query, ownerID)
	case "unit":
		var unit Unit
		unit, err = loadUnit(ctx, query, ownerID)
		if err == nil {
			_, err = loadItem(ctx, query, unit.ItemID)
			err = mediaAuthorityError(err)
		}
	case "location":
		_, err = loadLocation(ctx, query, ownerID)
	default:
		return ErrDomainInput
	}
	return err
}

func loadMediaLink(ctx context.Context, query queryer, id string) (retainedMediaLink, error) {
	value, err := scanMediaLink(query.QueryRowContext(ctx, mediaLinkSelect+" WHERE l.id=?", id))
	if err == nil {
		err = validateMediaLinkAuthority(ctx, query, value)
	}
	if err != nil {
		return retainedMediaLink{}, err
	}
	return value, nil
}

// Validate every retained identity and actual owner inside the migration
// transaction. SQLite text length and JSON alone cannot certify UTF-8 or NUL.
func validateMediaBackfill(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT entity_type,entity_id,revision FROM projection_revisions WHERE entity_type IN('media','media_link')")
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
		{mediaSelect, func(row rowScanner) error {
			value, err := scanMedia(row)
			if err == nil && value.Verification != "unverified" {
				return ErrDomainState
			}
			if err == nil {
				err = validateMediaCurrentProjection(ctx, tx, "media", value.ID, value.Revision)
			}
			return err
		}},
		{mediaLinkSelect, func(row rowScanner) error {
			value, err := scanMediaLink(row)
			if err == nil {
				err = validateMediaLinkAuthority(ctx, tx, value)
			}
			if err == nil {
				err = validateMediaCurrentProjection(ctx, tx, "media_link", value.ID, value.Revision)
			}
			return err
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

func validateMediaCurrentProjection(ctx context.Context, tx *sql.Tx, kind, id string, revision int64) error {
	var actual int64
	err := tx.QueryRowContext(ctx, "SELECT entity_revision FROM current_projections WHERE entity_type=? AND entity_id=?", kind, id).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) || err == nil && actual != revision {
		return ErrDomainState
	}
	return err
}

func readMediaMembers(ctx context.Context, tx *sql.Tx, id string) ([]mediaMember, error) {
	rows, err := tx.QueryContext(ctx, mediaLinkSelect+" WHERE l.media_id=? ORDER BY l.id", id)
	if err != nil {
		return nil, err
	}
	members := make([]mediaMember, 0)
	for rows.Next() {
		link, err := scanMediaLink(rows)
		if err == nil {
			err = validateMediaLinkAuthority(ctx, tx, link)
		}
		var revision int64
		var payload string
		if err == nil {
			err = tx.QueryRowContext(ctx, `SELECT entity_revision,payload_json FROM current_projections
 WHERE entity_type='media_link' AND entity_id=?`, link.ID).Scan(&revision, &payload)
			if errors.Is(err, sql.ErrNoRows) || err == nil && revision != link.Revision {
				err = ErrDomainState
			}
		}
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		members = append(members, mediaMember{link: link, payload: payload})
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return members, nil
}

// Remove only the selected owner's links, with independently retained link
// tombstones and one coalesced Media membership revision per affected image.
// The caller has already loaded and reviewed the actual owner and its proof.
func (m *mutationTx) removeOwnerMediaLinks(ownerType, ownerID string) error {
	var column string
	switch ownerType {
	case "item":
		column = "item_id"
	case "unit":
		column = "item_unit_id"
	case "location":
		column = "location_id"
	default:
		return ErrDomainState
	}
	ids, err := queryIDs(m.ctx, m.tx, "SELECT id FROM media_links WHERE "+column+"=? ORDER BY id", ownerID)
	if err != nil {
		return err
	}
	links := make([]retainedMediaLink, 0, len(ids))
	mediaIDs := make([]string, 0, len(ids))
	media := make(map[string]retainedMedia)
	selected := make(map[string]retainedMediaLink, len(ids))
	for _, id := range ids {
		link, err := scanMediaLink(m.tx.QueryRowContext(m.ctx, mediaLinkSelect+" WHERE l.id=?", id))
		if err != nil {
			return mediaAuthorityError(err)
		}
		if link.OwnerType != ownerType || link.OwnerID != ownerID {
			return ErrDomainState
		}
		if _, exists := media[link.MediaID]; !exists {
			value, err := loadMedia(m.ctx, m.tx, link.MediaID)
			if err != nil {
				return mediaAuthorityError(err)
			}
			if err = validateMediaCurrentProjection(m.ctx, m.tx, "media", value.ID, value.Revision); err != nil {
				return err
			}
			media[link.MediaID] = value
			mediaIDs = append(mediaIDs, link.MediaID)
		}
		links = append(links, link)
		selected[link.ID] = link
	}
	remaining := make(map[string][]mediaMember, len(mediaIDs))
	for _, id := range mediaIDs {
		members, err := readMediaMembers(m.ctx, m.tx, id)
		if err != nil {
			return err
		}
		expected := make([]mediaMember, 0, len(members))
		for _, member := range members {
			if chosen, exists := selected[member.link.ID]; exists {
				if !reflect.DeepEqual(chosen, member.link) {
					return ErrDomainState
				}
				delete(selected, member.link.ID)
			} else {
				expected = append(expected, member)
			}
		}
		remaining[id] = expected
	}
	if len(selected) != 0 {
		return ErrDomainState
	}
	for _, link := range links {
		revision, err := m.touch("media_link", link.ID, true)
		if err != nil {
			return err
		}
		if revision != link.Revision+1 {
			return ErrDomainState
		}
		if err = requireDomainRow(m.tx.ExecContext(m.ctx, "DELETE FROM media_links WHERE id=?", link.ID)); err != nil {
			return err
		}
	}
	for _, link := range links {
		var exists bool
		if err = m.tx.QueryRowContext(m.ctx, "SELECT EXISTS(SELECT 1 FROM media_links WHERE id=?)", link.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrDomainState
		}
	}
	var remains bool
	if err = m.tx.QueryRowContext(m.ctx, "SELECT EXISTS(SELECT 1 FROM media_links WHERE "+column+"=?)", ownerID).Scan(&remains); err != nil {
		return err
	}
	if remains {
		return ErrDomainState
	}
	for _, id := range mediaIDs {
		members, err := readMediaMembers(m.ctx, m.tx, id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(remaining[id], members) {
			return ErrDomainState
		}
		after, err := loadMedia(m.ctx, m.tx, id)
		if err != nil {
			return mediaAuthorityError(err)
		}
		if !reflect.DeepEqual(media[id], after) {
			return ErrDomainState
		}
		revision, err := m.touch("media", id, false)
		if err != nil {
			return err
		}
		if revision != media[id].Revision+1 {
			return ErrDomainState
		}
	}
	m.finalChecks = append(m.finalChecks, func() error {
		return m.verifyRemovedMediaOwner(column, ownerID, links, mediaIDs, media, remaining)
	})
	return nil
}

// Verify the final committed shape after publication, audit and session writes.
// Actual source is authoritative; sibling current bytes are compared only to
// ensure they were not silently changed by this unrelated owner deletion.
func (m *mutationTx) verifyRemovedMediaOwner(column, ownerID string, links []retainedMediaLink, mediaIDs []string,
	before map[string]retainedMedia, remaining map[string][]mediaMember) error {
	var exists bool
	if err := m.tx.QueryRowContext(m.ctx, "SELECT EXISTS(SELECT 1 FROM media_links WHERE "+column+"=?)", ownerID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrDomainState
	}
	for _, link := range links {
		var revision int64
		err := m.tx.QueryRowContext(m.ctx, "SELECT revision FROM projection_revisions WHERE entity_type='media_link' AND entity_id=?", link.ID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) || err == nil && revision != link.Revision+1 {
			return ErrDomainState
		}
		if err != nil {
			return err
		}
		if err = m.tx.QueryRowContext(m.ctx, `SELECT EXISTS(SELECT 1 FROM media_links WHERE id=?) OR
 EXISTS(SELECT 1 FROM current_projections WHERE entity_type='media_link' AND entity_id=?)`, link.ID, link.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrDomainState
		}
		if err = m.requireMediaFrozenChange("media_link", link.ID, revision, "delete", nil); err != nil {
			return err
		}
	}
	for _, id := range mediaIDs {
		expected := before[id]
		expected.Revision++
		actual, err := loadMedia(m.ctx, m.tx, id)
		if err != nil {
			return mediaAuthorityError(err)
		}
		if !reflect.DeepEqual(expected, actual) {
			return ErrDomainState
		}
		members, err := readMediaMembers(m.ctx, m.tx, id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(remaining[id], members) {
			return ErrDomainState
		}
		payload, err := json.Marshal(expected.mediaData)
		if err != nil {
			return err
		}
		var current string
		err = m.tx.QueryRowContext(m.ctx, `SELECT payload_json FROM current_projections
 WHERE entity_type='media' AND entity_id=? AND entity_revision=?`, id, expected.Revision).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) || err == nil && current != string(payload) {
			return ErrDomainState
		}
		if err != nil {
			return err
		}
		if err = m.requireMediaFrozenChange("media", id, expected.Revision, "upsert", &current); err != nil {
			return err
		}
	}
	return nil
}

func (m *mutationTx) requireMediaFrozenChange(kind, id string, revision int64, operation string, payload *string) error {
	var value any
	if payload != nil {
		value = *payload
	}
	var count, matching int
	if err := m.tx.QueryRowContext(m.ctx, `SELECT COUNT(*),COALESCE(SUM(operation=? AND projection_json IS ?),0)
 FROM sync_changes WHERE entity_type=? AND entity_id=? AND entity_revision=?`, operation, value, kind, id, revision).Scan(&count, &matching); err != nil {
		return err
	}
	if count != 1 || matching != 1 {
		return ErrDomainState
	}
	return nil
}
