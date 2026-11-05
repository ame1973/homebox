package store

import "context"

func (taxonomy *Taxonomy) CreateTag(ctx context.Context, token, key, name string) (Tag, error) {
	if !authString(name, 1, 200) {
		return Tag{}, ErrDomainInput
	}
	request := struct {
		Name string `json:"name"`
	}{name}
	return runDomainMutation(ctx, taxonomy.auth, token, key, "post_tags", request, func(m *mutationTx) (Tag, error) {
		if err := m.requireTagName(name, ""); err != nil {
			return Tag{}, err
		}
		id, err := newDomainID()
		if err != nil {
			return Tag{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "INSERT INTO tags(id,name) VALUES(?,?)", id, name); err != nil {
			return Tag{}, err
		}
		if _, err = m.touch("tag", id, false); err != nil {
			return Tag{}, err
		}
		return loadTag(ctx, m.tx, id)
	})
}

func (taxonomy *Taxonomy) PatchTag(ctx context.Context, token, key, id string, revision int64, name string) (Tag, error) {
	if !validDomainID(id) || !authString(name, 1, 200) {
		return Tag{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Tag{}, err
	}
	request := struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Name     string `json:"name"`
	}{id, revision, name}
	return runDomainMutation(ctx, taxonomy.auth, token, key, "patch_tags_id", request, func(m *mutationTx) (Tag, error) {
		before, err := tagAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Tag{}, err
		}
		if before.Name == name {
			return before, nil
		}
		if err = m.requireTagName(name, id); err != nil {
			return Tag{}, err
		}
		items, err := queryIDs(ctx, m.tx, "SELECT item_id FROM item_tags WHERE tag_id=? ORDER BY item_id", id)
		if err != nil {
			return Tag{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE tags SET name=? WHERE id=?", name, id); err != nil {
			return Tag{}, err
		}
		if _, err = m.touch("tag", id, false); err != nil {
			return Tag{}, err
		}
		for _, itemID := range items {
			if err = m.touchItemSearch(itemID, false); err != nil {
				return Tag{}, err
			}
		}
		return loadTag(ctx, m.tx, id)
	})
}

// DeleteTag detaches the tag's own independent links. It neither deletes items
// nor revives historical accepted candidate labels after this explicit override.
func (taxonomy *Taxonomy) DeleteTag(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
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
	return runDomainMutation(ctx, taxonomy.auth, token, key, "delete_tags_id", request, func(m *mutationTx) (ActionAck, error) {
		if _, err := tagAtRevision(ctx, m.tx, id, revision); err != nil {
			return ActionAck{}, err
		}
		items, err := queryIDs(ctx, m.tx, "SELECT item_id FROM item_tags WHERE tag_id=? ORDER BY item_id", id)
		if err != nil {
			return ActionAck{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "DELETE FROM item_tags WHERE tag_id=?", id); err != nil {
			return ActionAck{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "DELETE FROM tags WHERE id=?", id); err != nil {
			return ActionAck{}, err
		}
		deletedRevision, err := m.touch("tag", id, true)
		if err != nil {
			return ActionAck{}, err
		}
		for _, itemID := range items {
			if _, err = m.touch("item_tag", projectionPair(itemID, id), true); err != nil {
				return ActionAck{}, err
			}
			if err = m.touchItemSearch(itemID, true); err != nil {
				return ActionAck{}, err
			}
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: deletedRevision}, nil
	})
}

func (m *mutationTx) requireTagName(name, exceptID string) error {
	var exists bool
	if err := m.tx.QueryRowContext(m.ctx, "SELECT EXISTS(SELECT 1 FROM tags WHERE name=? AND id<>?)", name, exceptID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrTagName
	}
	return nil
}
