package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
)

var ErrMediaCoverConflict = errors.New("another media link is already the owner cover")

// MediaLinkCreate retains the explicit owner and optional metadata decisions.
// HTTP must independently reject explicit nulls and unknown/duplicate fields.
type MediaLinkCreate struct {
	ItemID        *string `json:"item_id,omitempty"`
	ItemUnitID    *string `json:"item_unit_id,omitempty"`
	LocationID    *string `json:"location_id,omitempty"`
	OwnerRevision int64   `json:"owner_revision"`
	IsCover       *bool   `json:"is_cover,omitempty"`
	SortOrder     *int64  `json:"sort_order,omitempty"`
	Description   *string `json:"description,omitempty"`
}

func (value MediaLink) validateDomainResult() error {
	if !validDomainID(value.ID) || !validDomainID(value.MediaID) || !validDomainID(value.OwnerID) ||
		(value.OwnerType != "item" && value.OwnerType != "unit" && value.OwnerType != "location") ||
		value.Revision < 1 || value.Revision > maxSafeInteger || value.SortOrder < -maxSafeInteger || value.SortOrder > maxSafeInteger ||
		!authString(value.Description, 0, 10000) {
		return ErrDomainState
	}
	return nil
}

func (input MediaLinkCreate) owner() (string, string, string, error) {
	var kind, id, column string
	count := 0
	for _, value := range []struct {
		kind, column string
		id           *string
	}{{"item", "item_id", input.ItemID}, {"unit", "item_unit_id", input.ItemUnitID}, {"location", "location_id", input.LocationID}} {
		if value.id != nil {
			count++
			kind, column, id = value.kind, value.column, *value.id
		}
	}
	if count != 1 || !validDomainID(id) || input.OwnerRevision < 1 || input.OwnerRevision > maxSafeInteger ||
		input.SortOrder != nil && (*input.SortOrder < 0 || *input.SortOrder > maxSafeInteger) ||
		input.Description != nil && !authString(*input.Description, 0, 10000) {
		return "", "", "", ErrDomainInput
	}
	return kind, id, column, nil
}

// Attach creates one independent link against the original Media and owner
// proofs. Media bytes, other links and other owners are never deleted.
func (library *MediaLibrary) Attach(ctx context.Context, token, key, mediaID string, revision int64, input MediaLinkCreate) (MediaLink, error) {
	if !validDomainID(mediaID) {
		return MediaLink{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return MediaLink{}, err
	}
	kind, ownerID, column, err := input.owner()
	if err != nil {
		return MediaLink{}, err
	}
	request := struct {
		MediaID  string          `json:"media_id"`
		Revision int64           `json:"revision"`
		Input    MediaLinkCreate `json:"input"`
	}{mediaID, revision, input}
	return runDomainMutation(ctx, library.auth, token, key, "post_media_id_links", request, func(m *mutationTx) (MediaLink, error) {
		media, err := loadMedia(ctx, m.tx, mediaID)
		if err != nil {
			return MediaLink{}, err
		}
		if media.Revision != revision {
			return MediaLink{}, ErrRevisionConflict
		}
		if err = validateMediaCurrentProjection(ctx, m.tx, "media", mediaID, media.Revision); err != nil {
			return MediaLink{}, err
		}
		owner, err := m.readMediaOwner(kind, ownerID)
		if err != nil {
			return MediaLink{}, err
		}
		if owner.revision() != input.OwnerRevision {
			return MediaLink{}, ErrRevisionConflict
		}
		mediaMembers, err := readMediaMembers(ctx, m.tx, mediaID)
		if err != nil {
			return MediaLink{}, err
		}
		ownerMembers, err := readMediaMembersFor(ctx, m.tx, column, ownerID)
		if err != nil {
			return MediaLink{}, err
		}
		if input.IsCover != nil && *input.IsCover {
			for _, member := range ownerMembers {
				if member.link.IsCover {
					return MediaLink{}, ErrMediaCoverConflict
				}
			}
		}
		related, err := m.captureOtherMedia(ownerMembers, mediaID)
		if err != nil {
			return MediaLink{}, err
		}
		id, err := newDomainID()
		if err != nil {
			return MediaLink{}, err
		}
		value := retainedMediaLink{mediaLinkData: mediaLinkData{ID: id, MediaID: mediaID, OwnerType: kind, OwnerID: ownerID}, Revision: 1}
		if input.IsCover != nil {
			value.IsCover = *input.IsCover
		}
		if input.SortOrder != nil {
			value.SortOrder = *input.SortOrder
		}
		if input.Description != nil {
			value.Description = *input.Description
		}
		// column is selected solely by input.owner's three fixed owner cases.
		err = requireDomainRow(m.tx.ExecContext(ctx, "INSERT INTO media_links(id,media_id,"+column+",is_cover,sort_order,description) VALUES(?,?,?,?,?,?)",
			id, mediaID, ownerID, value.IsCover, value.SortOrder, value.Description))
		if err != nil {
			return MediaLink{}, err
		}
		if next, err := m.touch("media_link", id, false); err != nil {
			return MediaLink{}, err
		} else if next != 1 {
			return MediaLink{}, ErrDomainState
		}
		if next, err := m.touch("media", mediaID, false); err != nil {
			return MediaLink{}, err
		} else if next != media.Revision+1 {
			return MediaLink{}, ErrDomainState
		}
		if err = m.touchMediaOwner(owner); err != nil {
			return MediaLink{}, err
		}
		payload, err := json.Marshal(value.mediaLinkData)
		if err != nil {
			return MediaLink{}, err
		}
		member := mediaMember{link: value, payload: string(payload)}
		mediaMembers = insertMediaMember(mediaMembers, member)
		ownerMembers = insertMediaMember(ownerMembers, member)
		m.finalChecks = append(m.finalChecks, func() error {
			return m.verifyAttachedMedia(owner, media, value, mediaMembers, ownerMembers, related, column)
		})
		return publicMediaLink(value), nil
	})
}

func insertMediaMember(members []mediaMember, added mediaMember) []mediaMember {
	result := append(slices.Clone(members), added)
	slices.SortFunc(result, func(a, b mediaMember) int { return strings.Compare(a.link.ID, b.link.ID) })
	return result
}

// Other Media linked to the selected owner are not touched by an attachment.
type guardedMedia struct {
	value   retainedMedia
	payload string
}

func (m *mutationTx) captureOtherMedia(members []mediaMember, selected string) (map[string]guardedMedia, error) {
	result := make(map[string]guardedMedia)
	for _, member := range members {
		id := member.link.MediaID
		if id == selected {
			continue
		}
		if _, exists := result[id]; exists {
			continue
		}
		value, err := loadMedia(m.ctx, m.tx, id)
		if err != nil {
			return nil, mediaAuthorityError(err)
		}
		var payload string
		err = m.tx.QueryRowContext(m.ctx, `SELECT payload_json FROM current_projections
 WHERE entity_type='media' AND entity_id=? AND entity_revision=?`, id, value.Revision).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDomainState
		}
		if err != nil {
			return nil, err
		}
		result[id] = guardedMedia{value, payload}
	}
	return result, nil
}

func (m *mutationTx) verifyAttachedMedia(owner mediaOwnerState, before retainedMedia, link retainedMediaLink,
	mediaMembers, ownerMembers []mediaMember, related map[string]guardedMedia, column string,
) error {
	if err := m.verifyMediaOwner(owner); err != nil {
		return err
	}
	want := before
	want.Revision++
	actual, err := loadMedia(m.ctx, m.tx, before.ID)
	if err != nil {
		return mediaAuthorityError(err)
	}
	if !reflect.DeepEqual(want, actual) {
		return ErrDomainState
	}
	if err = m.requirePublishedMedia("media", want.ID, want.Revision, want.mediaData); err != nil {
		return err
	}
	if err = m.requirePublishedMedia("media_link", link.ID, link.Revision, link.mediaLinkData); err != nil {
		return err
	}
	actualMediaMembers, err := readMediaMembers(m.ctx, m.tx, before.ID)
	if err != nil {
		return err
	}
	actualOwnerMembers, err := readMediaMembersFor(m.ctx, m.tx, column, owner.id)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(mediaMembers, actualMediaMembers) || !reflect.DeepEqual(ownerMembers, actualOwnerMembers) {
		return ErrDomainState
	}
	currentRelated, err := m.captureOtherMedia(actualOwnerMembers, before.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(related, currentRelated) {
		return ErrDomainState
	}
	return nil
}

func (m *mutationTx) requirePublishedMedia(kind, id string, revision int64, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var current string
	err = m.tx.QueryRowContext(m.ctx, `SELECT payload_json FROM current_projections
 WHERE entity_type=? AND entity_id=? AND entity_revision=?`, kind, id, revision).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) || err == nil && current != string(payload) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	return m.requireMediaFrozenChange(kind, id, revision, "upsert", &current)
}
