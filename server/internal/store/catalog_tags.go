package store

import "context"

// ReplaceTags applies one complete reviewed set, not a truncated membership page.
// A nil slice is not the contracted empty JSON array; callers must be explicit.
func (catalog *Catalog) ReplaceTags(ctx context.Context, token, key, itemID string, revision int64, tagIDs []string) (Item, error) {
	if tagIDs == nil || len(tagIDs) > 100 {
		return Item{}, ErrDomainInput
	}
	ids := append([]string{}, tagIDs...)
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validDomainID(id) || wanted[id] {
			return Item{}, ErrDomainInput
		}
		wanted[id] = true
	}
	input := struct {
		TagIDs []string `json:"tag_ids"`
	}{ids}
	return runCatalogLinkMutation(ctx, catalog, token, key, "put_items_id_tags", itemID, revision, input, func(m *mutationTx) (Item, error) {
		before, err := itemAtRevision(ctx, m.tx, itemID, revision)
		if err != nil {
			return Item{}, err
		}
		for _, id := range ids {
			if _, err := loadTag(ctx, m.tx, id); err != nil {
				return Item{}, err
			}
		}
		if err := validateItemTagAuthority(ctx, m.tx, itemID); err != nil {
			return Item{}, err
		}
		current, err := queryIDs(ctx, m.tx, "SELECT tag_id FROM item_tags WHERE item_id=? ORDER BY tag_id", itemID)
		if err != nil {
			return Item{}, err
		}
		present := make(map[string]bool, len(current))
		for _, id := range current {
			if !validDomainID(id) {
				return Item{}, ErrDomainState
			}
			present[id] = true
			if !wanted[id] {
				// A removed legacy member still needs its real retained Tag counter;
				// touching it must never invent revision one for corrupt authority.
				if _, err := loadTag(ctx, m.tx, id); err != nil {
					return Item{}, err
				}
			}
		}
		changed := false
		for _, id := range current {
			if wanted[id] {
				continue
			}
			if _, err := m.tx.ExecContext(ctx, "DELETE FROM item_tags WHERE item_id=? AND tag_id=?", itemID, id); err != nil {
				return Item{}, err
			}
			if _, err := m.touch("item_tag", projectionPair(itemID, id), true); err != nil {
				return Item{}, err
			}
			if _, err := m.touch("tag", id, false); err != nil {
				return Item{}, err
			}
			changed = true
		}
		for _, id := range ids {
			if present[id] {
				continue
			}
			if _, err := m.tx.ExecContext(ctx, "INSERT INTO item_tags(item_id,tag_id) VALUES(?,?)", itemID, id); err != nil {
				return Item{}, err
			}
			if _, err := m.touch("item_tag", projectionPair(itemID, id), false); err != nil {
				return Item{}, err
			}
			if _, err := m.touch("tag", id, false); err != nil {
				return Item{}, err
			}
			changed = true
		}
		if !changed {
			return before, nil
		}
		if err := m.touchItemSearch(itemID, true); err != nil {
			return Item{}, err
		}
		return loadItem(ctx, m.tx, itemID)
	})
}

// Existing membership has independent canonical authority even though the public
// page returns Tag resources. Never seed a missing legacy link counter on removal.
func validateItemTagAuthority(ctx context.Context, query queryer, itemID string) error {
	var invalid bool
	if err := query.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM item_tags l LEFT JOIN projection_revisions r
 ON r.entity_type='item_tag' AND r.entity_id=json_array(l.item_id,l.tag_id)
 WHERE l.item_id=? AND (r.revision IS NULL OR r.revision NOT BETWEEN 1 AND ?))`, itemID, maxSafeInteger).Scan(&invalid); err != nil {
		return err
	}
	if invalid {
		return ErrDomainState
	}
	return nil
}
