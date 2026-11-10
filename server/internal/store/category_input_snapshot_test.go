package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCategoryCommandsSnapshotCallerInputsBeforeFingerprintAndClock(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		s, taxonomy, auth, token := taxonomyFixture(t)
		originalParent := createCategory(t, taxonomy, token, "Original parent", nil)
		otherParent := createCategory(t, taxonomy, token, "Other parent", nil)
		parent := originalParent.ID
		input := CategoryCreate{Name: "Reviewed child", ParentID: &parent}
		clock := auth.now
		auth.now = func() time.Time {
			parent = otherParent.ID
			return clock()
		}
		created, err := taxonomy.CreateCategory(t.Context(), token, "category-create-input-snapshot", originalParent.Revision, input)
		auth.now = clock
		if err != nil || created.ParentID == nil || *created.ParentID != originalParent.ID {
			t.Fatal("caller pointer changed the fingerprinted category parent", created, err)
		}
		if scalar[string](t, s.db, "SELECT parent_id FROM categories WHERE id=?", created.ID) != originalParent.ID ||
			getCategory(t, taxonomy, token, originalParent.ID).Revision != originalParent.Revision+1 || getCategory(t, taxonomy, token, otherParent.ID).Revision != otherParent.Revision {
			t.Fatal("category creation changed the wrong actual parent or aggregate")
		}
		parent = originalParent.ID
		replay, err := taxonomy.CreateCategory(t.Context(), token, "category-create-input-snapshot", originalParent.Revision, input)
		if err != nil || !reflect.DeepEqual(created, replay) {
			t.Fatal("original category creation did not reconcile", err)
		}
	})
	t.Run("patch", func(t *testing.T) {
		s, taxonomy, auth, token := taxonomyFixture(t)
		category := createCategory(t, taxonomy, token, "Original category", nil)
		originalParent := createCategory(t, taxonomy, token, "Reviewed parent", nil)
		otherParent := createCategory(t, taxonomy, token, "Different parent", nil)
		catalog, err := NewCatalog(auth)
		if err != nil {
			t.Fatal(err)
		}
		item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Associated item", CategoryID: &category.ID})
		category = getCategory(t, taxonomy, token, category.ID)
		parent, name := originalParent.ID, "Reviewed category name"
		input := CategoryPatch{ParentID: NullableID{Set: true, Value: &parent}, Name: &name}
		clock := auth.now
		auth.now = func() time.Time {
			parent, name = otherParent.ID, "Changed category name"
			return clock()
		}
		updated, err := taxonomy.PatchCategory(t.Context(), token, "category-patch-input-snapshot", category.ID, category.Revision, input)
		auth.now = clock
		if err != nil || updated.ParentID == nil || *updated.ParentID != originalParent.ID || updated.Name != "Reviewed category name" || updated.Revision != category.Revision+1 {
			t.Fatal("caller pointers changed the fingerprinted category patch", updated, err)
		}
		text := approvedText(t, s, item.ID)
		if !strings.Contains(text, "Reviewed category name") || strings.Contains(text, "Changed category name") ||
			getCategory(t, taxonomy, token, originalParent.ID).Revision != originalParent.Revision+1 || getCategory(t, taxonomy, token, otherParent.ID).Revision != otherParent.Revision {
			t.Fatal("category patch published the wrong search text or parent aggregate")
		}
		parent, name = originalParent.ID, "Reviewed category name"
		replay, err := taxonomy.PatchCategory(t.Context(), token, "category-patch-input-snapshot", category.ID, category.Revision, input)
		if err != nil || !reflect.DeepEqual(updated, replay) {
			t.Fatal("original category patch did not reconcile", err)
		}
	})
	t.Run("equal patch", func(t *testing.T) {
		s, taxonomy, auth, token := taxonomyFixture(t)
		category := createCategory(t, taxonomy, token, "Original category", nil)
		name := category.Name
		input := CategoryPatch{Name: &name}
		changes := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes")
		clock := auth.now
		auth.now = func() time.Time {
			name = "Changed after fingerprint"
			return clock()
		}
		result, err := taxonomy.PatchCategory(t.Context(), token, "category-equal-input-snapshot", category.ID, category.Revision, input)
		auth.now = clock
		if err != nil || !reflect.DeepEqual(category, result) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != changes {
			t.Fatal("fingerprinted equal category intent published a change", result, err)
		}
		name = category.Name
		replay, err := taxonomy.PatchCategory(t.Context(), token, "category-equal-input-snapshot", category.ID, category.Revision, input)
		if err != nil || !reflect.DeepEqual(category, replay) {
			t.Fatal("equal original category intent did not reconcile", err)
		}
	})
}
