package store

import (
	"context"
	"database/sql"
	"errors"
)

var (
	ErrCategoryCycle    = errors.New("category cannot contain itself or a descendant")
	ErrCategoryChildren = errors.New("category still has child categories")
	ErrTagName          = errors.New("tag name is already in use")
)

type CategoryData struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	Name     string  `json:"name"`
}

type Category struct {
	CategoryData
	Revision int64 `json:"revision"`
}

type CategoryCreate struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id"`
}

type CategoryPatch struct {
	Name     *string    `json:"name,omitempty"`
	ParentID NullableID `json:"parent_id"`
}

type CategoryPage struct {
	Data   []Category `json:"data"`
	NextID string     `json:"-"`
}

type TagData struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Tag struct {
	TagData
	Revision int64 `json:"revision"`
}

type TagPage struct {
	Data   []Tag  `json:"data"`
	NextID string `json:"-"`
}

// Taxonomy owns classification mutations inside the existing private store.
// HTTP must still enforce Origin/CSRF/version/admission before calling it.
type Taxonomy struct{ auth *Auth }

func NewTaxonomy(a *Auth) (*Taxonomy, error) {
	if a == nil || a.store == nil {
		return nil, ErrDomainInput
	}
	return a.store.NewTaxonomy(a)
}

func (s *Store) NewTaxonomy(a *Auth) (*Taxonomy, error) {
	if s == nil || a == nil || a.store != s {
		return nil, ErrDomainInput
	}
	return &Taxonomy{auth: a}, nil
}

func (taxonomy *Taxonomy) GetCategory(ctx context.Context, token, id string) (Category, error) {
	if !validDomainID(id) {
		return Category{}, ErrDomainInput
	}
	return readDomain(ctx, taxonomy.auth, token, func(tx *sql.Tx) (Category, error) { return loadCategory(ctx, tx, id) })
}

func (taxonomy *Taxonomy) GetTag(ctx context.Context, token, id string) (Tag, error) {
	if !validDomainID(id) {
		return Tag{}, ErrDomainInput
	}
	return readDomain(ctx, taxonomy.auth, token, func(tx *sql.Tx) (Tag, error) { return loadTag(ctx, tx, id) })
}

// NextID is private continuation data, not an unsigned HTTP cursor or snapshot.
func (taxonomy *Taxonomy) ListCategories(ctx context.Context, token, afterID string, limit int) (CategoryPage, error) {
	if !validTaxonomyPage(afterID, limit) {
		return CategoryPage{}, ErrDomainInput
	}
	return readDomain(ctx, taxonomy.auth, token, func(tx *sql.Tx) (CategoryPage, error) {
		rows, err := tx.QueryContext(ctx, categorySelect+" WHERE c.id>? ORDER BY c.id LIMIT ?", afterID, limit+1)
		if err != nil {
			return CategoryPage{}, err
		}
		defer rows.Close()
		page := CategoryPage{Data: make([]Category, 0)}
		for rows.Next() {
			value, err := scanCategory(rows)
			if err != nil {
				return CategoryPage{}, err
			}
			page.Data = append(page.Data, value)
		}
		if err := rows.Err(); err != nil {
			return CategoryPage{}, err
		}
		if len(page.Data) > limit {
			page.Data = page.Data[:limit]
			page.NextID = page.Data[limit-1].ID
		}
		return page, nil
	})
}

func (taxonomy *Taxonomy) ListTags(ctx context.Context, token, afterID string, limit int) (TagPage, error) {
	if !validTaxonomyPage(afterID, limit) {
		return TagPage{}, ErrDomainInput
	}
	return readDomain(ctx, taxonomy.auth, token, func(tx *sql.Tx) (TagPage, error) {
		rows, err := tx.QueryContext(ctx, tagSelect+" WHERE t.id>? ORDER BY t.id LIMIT ?", afterID, limit+1)
		if err != nil {
			return TagPage{}, err
		}
		defer rows.Close()
		page := TagPage{Data: make([]Tag, 0)}
		for rows.Next() {
			value, err := scanTag(rows)
			if err != nil {
				return TagPage{}, err
			}
			page.Data = append(page.Data, value)
		}
		if err := rows.Err(); err != nil {
			return TagPage{}, err
		}
		if len(page.Data) > limit {
			page.Data = page.Data[:limit]
			page.NextID = page.Data[limit-1].ID
		}
		return page, nil
	})
}

func validTaxonomyPage(afterID string, limit int) bool {
	return (afterID == "" || validDomainID(afterID)) && limit >= 1 && limit <= 100
}

func (taxonomy *Taxonomy) CreateCategory(ctx context.Context, token, key string, parentRevision int64, input CategoryCreate) (Category, error) {
	if !authString(input.Name, 1, 200) || input.ParentID != nil && !validDomainID(*input.ParentID) || input.ParentID == nil && parentRevision != 0 {
		return Category{}, ErrDomainInput
	}
	if input.ParentID != nil {
		if err := validExpectedRevision(parentRevision); err != nil {
			return Category{}, err
		}
	}
	request := struct {
		ParentRevision int64          `json:"parent_revision"`
		Input          CategoryCreate `json:"input"`
	}{parentRevision, input}
	return runDomainMutation(ctx, taxonomy.auth, token, key, "post_categories", request, func(m *mutationTx) (Category, error) {
		if input.ParentID != nil {
			if _, err := categoryAtRevision(ctx, m.tx, *input.ParentID, parentRevision); err != nil {
				return Category{}, err
			}
		}
		id, err := newDomainID()
		if err != nil {
			return Category{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "INSERT INTO categories(id,parent_id,name) VALUES(?,?,?)", id, input.ParentID, input.Name); err != nil {
			return Category{}, err
		}
		if err = m.touchCategoryPath(id); err != nil {
			return Category{}, err
		}
		return loadCategory(ctx, m.tx, id)
	})
}

func (taxonomy *Taxonomy) PatchCategory(ctx context.Context, token, key, id string, revision int64, input CategoryPatch) (Category, error) {
	if !validDomainID(id) || input.Name == nil && !input.ParentID.Set ||
		input.Name != nil && !authString(*input.Name, 1, 200) ||
		!input.ParentID.Set && input.ParentID.Value != nil || input.ParentID.Value != nil && !validDomainID(*input.ParentID.Value) {
		return Category{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Category{}, err
	}
	request := struct {
		ID       string        `json:"id"`
		Revision int64         `json:"revision"`
		Input    CategoryPatch `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, taxonomy.auth, token, key, "patch_categories_id", request, func(m *mutationTx) (Category, error) {
		before, err := categoryAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Category{}, err
		}
		after := before
		if input.Name != nil {
			after.Name = *input.Name
		}
		if input.ParentID.Set {
			after.ParentID = input.ParentID.Value
		}
		if after.Name == before.Name && sameParent(after.ParentID, before.ParentID) {
			return before, nil
		}
		if after.ParentID != nil {
			if _, err := loadCategory(ctx, m.tx, *after.ParentID); err != nil {
				return Category{}, err
			}
			var cycle bool
			if err := m.tx.QueryRowContext(ctx, categorySubtreeCTE+"SELECT EXISTS(SELECT 1 FROM category_subtree WHERE id=?)", id, *after.ParentID).Scan(&cycle); err != nil {
				return Category{}, err
			}
			if cycle {
				return Category{}, ErrCategoryCycle
			}
		}
		items, err := queryIDs(ctx, m.tx, categorySubtreeCTE+"SELECT id FROM items WHERE category_id IN(SELECT id FROM category_subtree) ORDER BY id", id)
		if err != nil {
			return Category{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE categories SET name=?,parent_id=? WHERE id=?", after.Name, after.ParentID, id); err != nil {
			return Category{}, err
		}
		if before.ParentID != nil && !sameParent(before.ParentID, after.ParentID) {
			if err = m.touchCategoryPath(*before.ParentID); err != nil {
				return Category{}, err
			}
		}
		if err = m.touchCategoryPath(id); err != nil {
			return Category{}, err
		}
		for _, itemID := range items {
			if err = m.touchItemSearch(itemID, true); err != nil {
				return Category{}, err
			}
		}
		return loadCategory(ctx, m.tx, id)
	})
}

// DeleteCategory clears a leaf's classification references, not inventory. Child
// branches require explicit prior reparenting/deletion; old counters are retained.
func (taxonomy *Taxonomy) DeleteCategory(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
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
	return runDomainMutation(ctx, taxonomy.auth, token, key, "delete_categories_id", request, func(m *mutationTx) (ActionAck, error) {
		before, err := categoryAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return ActionAck{}, err
		}
		var children bool
		if err = m.tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM categories WHERE parent_id=?)", id).Scan(&children); err != nil {
			return ActionAck{}, err
		}
		if children {
			return ActionAck{}, ErrCategoryChildren
		}
		items, err := queryIDs(ctx, m.tx, "SELECT id FROM items WHERE category_id=? ORDER BY id", id)
		if err != nil {
			return ActionAck{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE items SET category_id=NULL WHERE category_id=?", id); err != nil {
			return ActionAck{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "DELETE FROM categories WHERE id=?", id); err != nil {
			return ActionAck{}, err
		}
		deletedRevision, err := m.touch("category", id, true)
		if err != nil {
			return ActionAck{}, err
		}
		if before.ParentID != nil {
			if err = m.touchCategoryPath(*before.ParentID); err != nil {
				return ActionAck{}, err
			}
		}
		for _, itemID := range items {
			if err = m.touchItemSearch(itemID, true); err != nil {
				return ActionAck{}, err
			}
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: deletedRevision}, nil
	})
}

const categorySelect = `SELECT c.id,c.parent_id,c.name,COALESCE(r.revision,0) FROM categories c
 LEFT JOIN projection_revisions r ON r.entity_type='category' AND r.entity_id=c.id`
const tagSelect = `SELECT t.id,t.name,COALESCE(r.revision,0) FROM tags t
 LEFT JOIN projection_revisions r ON r.entity_type='tag' AND r.entity_id=t.id`

func scanCategory(row rowScanner) (Category, error) {
	var value Category
	var parent sql.NullString
	if err := row.Scan(&value.ID, &parent, &value.Name, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Category{}, ErrNotFound
		}
		return Category{}, err
	}
	if parent.Valid {
		value.ParentID = &parent.String
	}
	return value, value.validateDomainResult()
}

func scanTag(row rowScanner) (Tag, error) {
	var value Tag
	if err := row.Scan(&value.ID, &value.Name, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Tag{}, ErrNotFound
		}
		return Tag{}, err
	}
	return value, value.validateDomainResult()
}

func loadCategory(ctx context.Context, query queryer, id string) (Category, error) {
	return scanCategory(query.QueryRowContext(ctx, categorySelect+" WHERE c.id=?", id))
}

func loadTag(ctx context.Context, query queryer, id string) (Tag, error) {
	return scanTag(query.QueryRowContext(ctx, tagSelect+" WHERE t.id=?", id))
}

func categoryAtRevision(ctx context.Context, query queryer, id string, revision int64) (Category, error) {
	value, err := loadCategory(ctx, query, id)
	if err == nil && value.Revision != revision {
		return Category{}, ErrRevisionConflict
	}
	return value, err
}

func tagAtRevision(ctx context.Context, query queryer, id string, revision int64) (Tag, error) {
	value, err := loadTag(ctx, query, id)
	if err == nil && value.Revision != revision {
		return Tag{}, ErrRevisionConflict
	}
	return value, err
}

func validCategoryData(value CategoryData) bool {
	return validDomainID(value.ID) && authString(value.Name, 1, 200) &&
		(value.ParentID == nil || validDomainID(*value.ParentID) && *value.ParentID != value.ID)
}

func (value Category) validateDomainResult() error {
	if !validCategoryData(value.CategoryData) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func (value Tag) validateDomainResult() error {
	if !validDomainID(value.ID) || !authString(value.Name, 1, 200) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

const categoryAncestorCTE = `WITH RECURSIVE category_ancestors(id,parent_id,name) AS (
 SELECT id,parent_id,name FROM categories WHERE id=?
 UNION SELECT c.id,c.parent_id,c.name FROM categories c JOIN category_ancestors p ON c.id=p.parent_id
) `
const categorySubtreeCTE = `WITH RECURSIVE category_subtree(id) AS (
 SELECT id FROM categories WHERE id=?
 UNION SELECT c.id FROM categories c JOIN category_subtree p ON c.parent_id=p.id
) `

func (m *mutationTx) touchCategoryPath(id string) error {
	ids, err := queryIDs(m.ctx, m.tx, categoryAncestorCTE+"SELECT id FROM category_ancestors ORDER BY id", id)
	if err != nil {
		return err
	}
	for _, categoryID := range ids {
		if _, err = m.touch("category", categoryID, false); err != nil {
			return err
		}
	}
	return nil
}
