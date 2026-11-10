package store

import (
	"context"
	"database/sql"
	"errors"
)

// Unlink removes one independently reviewed link without deleting its Media or
// any other owner's membership. Its acknowledgement carries the link tombstone.
func (library *MediaLibrary) Unlink(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
	if !validDomainID(id) {
		return ActionAck{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return ActionAck{}, err
	}
	request := struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}{id, revision}
	return runDomainMutation(ctx, library.auth, token, key, "delete_media_links_id", request, func(m *mutationTx) (ActionAck, error) {
		link, err := loadMediaLink(ctx, m.tx, id)
		if err != nil {
			return ActionAck{}, err
		}
		if link.Revision != revision {
			return ActionAck{}, ErrRevisionConflict
		}
		media, err := loadMedia(ctx, m.tx, link.MediaID)
		if err != nil {
			return ActionAck{}, mediaAuthorityError(err)
		}
		if err = validateMediaCurrentProjection(ctx, m.tx, "media", media.ID, media.Revision); err != nil {
			return ActionAck{}, err
		}
		owner, err := m.readMediaOwner(link.OwnerType, link.OwnerID)
		if err != nil {
			return ActionAck{}, mediaAuthorityError(err)
		}
		var column string
		switch link.OwnerType {
		case "item":
			column = "item_id"
		case "unit":
			column = "item_unit_id"
		case "location":
			column = "location_id"
		default:
			return ActionAck{}, ErrDomainState
		}
		mediaMembers, err := readMediaMembers(ctx, m.tx, media.ID)
		if err != nil {
			return ActionAck{}, err
		}
		ownerMembers, err := readMediaMembersFor(ctx, m.tx, column, owner.id)
		if err != nil {
			return ActionAck{}, err
		}
		if !hasMediaMember(mediaMembers, link) || !hasMediaMember(ownerMembers, link) {
			return ActionAck{}, ErrDomainState
		}
		related, err := m.captureOtherMedia(ownerMembers, media.ID)
		if err != nil {
			return ActionAck{}, err
		}
		if next, err := m.touch("media_link", id, true); err != nil {
			return ActionAck{}, err
		} else if next != link.Revision+1 {
			return ActionAck{}, ErrDomainState
		}
		if err = requireDomainRow(m.tx.ExecContext(ctx, "DELETE FROM media_links WHERE id=?", id)); err != nil {
			return ActionAck{}, err
		}
		if next, err := m.touch("media", media.ID, false); err != nil {
			return ActionAck{}, err
		} else if next != media.Revision+1 {
			return ActionAck{}, ErrDomainState
		}
		if err = m.touchMediaOwner(owner); err != nil {
			return ActionAck{}, err
		}
		mediaMembers = withoutMediaMember(mediaMembers, id)
		ownerMembers = withoutMediaMember(ownerMembers, id)
		m.finalChecks = append(m.finalChecks, func() error {
			return m.verifyUnlinkedMedia(owner, media, link, mediaMembers, ownerMembers, related, column)
		})
		return ActionAck{OK: true, OperationID: m.eventID, Revision: link.Revision + 1}, nil
	})
}

func withoutMediaMember(members []mediaMember, id string) []mediaMember {
	remaining := make([]mediaMember, 0, len(members))
	for _, member := range members {
		if member.link.ID != id {
			remaining = append(remaining, member)
		}
	}
	return remaining
}

func (m *mutationTx) verifyUnlinkedMedia(owner mediaOwnerState, before retainedMedia, link retainedMediaLink,
	mediaMembers, ownerMembers []mediaMember, related map[string]guardedMedia, column string,
) error {
	if err := m.verifyMediaMembership(owner, before, mediaMembers, ownerMembers, related, column); err != nil {
		return err
	}
	var revision int64
	err := m.tx.QueryRowContext(m.ctx, "SELECT revision FROM projection_revisions WHERE entity_type='media_link' AND entity_id=?", link.ID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) || err == nil && revision != link.Revision+1 {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	var exists bool
	if err = m.tx.QueryRowContext(m.ctx, `SELECT EXISTS(SELECT 1 FROM media_links WHERE id=?) OR
 EXISTS(SELECT 1 FROM current_projections WHERE entity_type='media_link' AND entity_id=?)`, link.ID, link.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrDomainState
	}
	return m.requireMediaFrozenChange("media_link", link.ID, revision, "delete", nil)
}
