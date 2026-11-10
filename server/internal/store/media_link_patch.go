package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
)

// MediaLinkPatch changes only supplied metadata, never the Media or owner.
type MediaLinkPatch struct {
	IsCover     *bool   `json:"is_cover,omitempty"`
	SortOrder   *int64  `json:"sort_order,omitempty"`
	Description *string `json:"description,omitempty"`
}

// PatchLink binds the original independent link proof and advances the actual
// Media and owner aggregates only when metadata changes.
func (library *MediaLibrary) PatchLink(ctx context.Context, token, key, id string, revision int64, input MediaLinkPatch) (MediaLink, error) {
	input.SortOrder = snapshotAssetNumber(input.SortOrder)
	input.Description = snapshotPurchaseString(input.Description)
	if input.IsCover != nil {
		cover := *input.IsCover
		input.IsCover = &cover
	}
	if !validDomainID(id) || input == (MediaLinkPatch{}) ||
		input.SortOrder != nil && (*input.SortOrder < 0 || *input.SortOrder > maxSafeInteger) ||
		input.Description != nil && !authString(*input.Description, 0, 10000) {
		return MediaLink{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return MediaLink{}, err
	}
	request := struct {
		ID       string         `json:"id"`
		Revision int64          `json:"revision"`
		Input    MediaLinkPatch `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, library.auth, token, key, "patch_media_links_id", request, func(m *mutationTx) (MediaLink, error) {
		before, err := loadMediaLink(ctx, m.tx, id)
		if err != nil {
			return MediaLink{}, err
		}
		if before.Revision != revision {
			return MediaLink{}, ErrRevisionConflict
		}
		media, err := loadMedia(ctx, m.tx, before.MediaID)
		if err != nil {
			return MediaLink{}, mediaAuthorityError(err)
		}
		if err = validateMediaCurrentProjection(ctx, m.tx, "media", media.ID, media.Revision); err != nil {
			return MediaLink{}, err
		}
		owner, err := m.readMediaOwner(before.OwnerType, before.OwnerID)
		if err != nil {
			return MediaLink{}, mediaAuthorityError(err)
		}
		var column string
		switch before.OwnerType {
		case "item":
			column = "item_id"
		case "unit":
			column = "item_unit_id"
		case "location":
			column = "location_id"
		default:
			return MediaLink{}, ErrDomainState
		}
		mediaMembers, err := readMediaMembers(ctx, m.tx, media.ID)
		if err != nil {
			return MediaLink{}, err
		}
		ownerMembers, err := readMediaMembersFor(ctx, m.tx, column, owner.id)
		if err != nil {
			return MediaLink{}, err
		}
		if !hasMediaMember(mediaMembers, before) || !hasMediaMember(ownerMembers, before) {
			return MediaLink{}, ErrDomainState
		}
		after := before
		if input.IsCover != nil {
			after.IsCover = *input.IsCover
		}
		if input.SortOrder != nil {
			after.SortOrder = *input.SortOrder
		}
		if input.Description != nil {
			after.Description = *input.Description
		}
		if !before.IsCover && after.IsCover {
			for _, member := range ownerMembers {
				if member.link.ID != id && member.link.IsCover {
					return MediaLink{}, ErrMediaCoverConflict
				}
			}
		}
		if after.mediaLinkData == before.mediaLinkData {
			allMedia, err := m.captureOtherMedia(ownerMembers, "")
			if err != nil {
				return MediaLink{}, err
			}
			projections, err := m.captureMediaOwnerProjections(owner)
			if err != nil {
				return MediaLink{}, err
			}
			m.finalChecks = append(m.finalChecks, func() error {
				return m.verifyUnchangedMediaLink(owner, media, mediaMembers, ownerMembers, allMedia, projections, column)
			})
			return publicMediaLink(before), nil
		}
		related, err := m.captureOtherMedia(ownerMembers, media.ID)
		if err != nil {
			return MediaLink{}, err
		}
		if err = requireDomainRow(m.tx.ExecContext(ctx, "UPDATE media_links SET is_cover=?,sort_order=?,description=? WHERE id=?", after.IsCover, after.SortOrder, after.Description, id)); err != nil {
			return MediaLink{}, err
		}
		if next, err := m.touch("media_link", id, false); err != nil {
			return MediaLink{}, err
		} else if next != before.Revision+1 {
			return MediaLink{}, ErrDomainState
		}
		if next, err := m.touch("media", media.ID, false); err != nil {
			return MediaLink{}, err
		} else if next != media.Revision+1 {
			return MediaLink{}, ErrDomainState
		}
		if err = m.touchMediaOwner(owner); err != nil {
			return MediaLink{}, err
		}
		after.Revision++
		payload, err := json.Marshal(after.mediaLinkData)
		if err != nil {
			return MediaLink{}, err
		}
		member := mediaMember{link: after, payload: string(payload)}
		mediaMembers = replaceMediaMember(mediaMembers, member)
		ownerMembers = replaceMediaMember(ownerMembers, member)
		m.finalChecks = append(m.finalChecks, func() error {
			return m.verifyUpdatedMediaLink(owner, media, after, mediaMembers, ownerMembers, related, column)
		})
		return publicMediaLink(after), nil
	})
}

func hasMediaMember(members []mediaMember, expected retainedMediaLink) bool {
	for _, member := range members {
		if member.link.ID == expected.ID {
			return reflect.DeepEqual(member.link, expected)
		}
	}
	return false
}

func replaceMediaMember(members []mediaMember, value mediaMember) []mediaMember {
	result := slices.Clone(members)
	for index := range result {
		if result[index].link.ID == value.link.ID {
			result[index] = value
			break
		}
	}
	return result
}

type guardedOwnerProjection struct {
	present           bool
	revision, version int64
	payload, updated  string
}

func (m *mutationTx) captureMediaOwnerProjections(owner mediaOwnerState) (map[projectionKey]guardedOwnerProjection, error) {
	keys := make([]projectionKey, 0, len(owner.locations)+2)
	if owner.item != nil {
		keys = append(keys, projectionKey{"item", owner.item.ID})
	}
	if owner.unit != nil {
		keys = append(keys, projectionKey{"unit", owner.unit.ID})
	}
	for _, location := range owner.locations {
		keys = append(keys, projectionKey{"location", location.ID})
	}
	result := make(map[projectionKey]guardedOwnerProjection, len(keys))
	for _, key := range keys {
		var value guardedOwnerProjection
		err := m.tx.QueryRowContext(m.ctx, `SELECT entity_revision,projection_version,payload_json,updated_at FROM current_projections
 WHERE entity_type=? AND entity_id=?`, key.kind, key.id).Scan(&value.revision, &value.version, &value.payload, &value.updated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		value.present = err == nil
		result[key] = value
	}
	return result, nil
}

func (m *mutationTx) verifyUnchangedMediaLink(owner mediaOwnerState, media retainedMedia, mediaMembers, ownerMembers []mediaMember,
	allMedia map[string]guardedMedia, projections map[projectionKey]guardedOwnerProjection, column string,
) error {
	actualOwner, err := m.readMediaOwner(owner.kind, owner.id)
	if err != nil {
		return mediaAuthorityError(err)
	}
	actualMedia, err := loadMedia(m.ctx, m.tx, media.ID)
	if err != nil {
		return mediaAuthorityError(err)
	}
	if !reflect.DeepEqual(actualOwner, owner) || !reflect.DeepEqual(actualMedia, media) {
		return ErrDomainState
	}
	actualMediaMembers, err := readMediaMembers(m.ctx, m.tx, media.ID)
	if err != nil {
		return err
	}
	actualOwnerMembers, err := readMediaMembersFor(m.ctx, m.tx, column, owner.id)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actualMediaMembers, mediaMembers) || !reflect.DeepEqual(actualOwnerMembers, ownerMembers) {
		return ErrDomainState
	}
	actualMediaSet, err := m.captureOtherMedia(actualOwnerMembers, "")
	if err != nil {
		return err
	}
	actualProjections, err := m.captureMediaOwnerProjections(actualOwner)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actualMediaSet, allMedia) || !reflect.DeepEqual(actualProjections, projections) {
		return ErrDomainState
	}
	return nil
}
