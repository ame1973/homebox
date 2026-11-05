package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func taxonomyFixture(t *testing.T) (*Store, *Taxonomy, *Auth, string) {
	t.Helper()
	s, a, _, _, token := completedAuthFixture(t)
	taxonomy, err := NewTaxonomy(a)
	if err != nil {
		t.Fatal(err)
	}
	return s, taxonomy, a, token
}

func getCategory(t *testing.T, taxonomy *Taxonomy, token, id string) Category {
	t.Helper()
	value, err := taxonomy.GetCategory(t.Context(), token, id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func createCategory(t *testing.T, taxonomy *Taxonomy, token, name string, parent *string) Category {
	t.Helper()
	var revision int64
	if parent != nil {
		revision = getCategory(t, taxonomy, token, *parent).Revision
	}
	value, err := taxonomy.CreateCategory(t.Context(), token, "create-category-"+name, revision, CategoryCreate{Name: name, ParentID: parent})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func createTag(t *testing.T, taxonomy *Taxonomy, token, name string) Tag {
	t.Helper()
	value, err := taxonomy.CreateTag(t.Context(), token, "create-tag-"+name, name)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTaxonomyCreatePublishesExactPayloadsAndPreservesNames(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	name := "  分類 😀 é  "
	first := createCategory(t, taxonomy, token, name, nil)
	second, err := taxonomy.CreateCategory(t.Context(), token, "another-category-key", 0, CategoryCreate{Name: name})
	if err != nil || first.ID == second.ID || first.Revision != 1 || first.ParentID != nil || first.Name != name {
		t.Fatal("display names replaced stable independent category identity", err)
	}
	if !reflect.DeepEqual(getCategory(t, taxonomy, token, first.ID), first) {
		t.Fatal("category read differs from its acknowledged source")
	}
	for _, fixture := range []struct {
		kind, id string
		fields   int
	}{{"category", first.ID, 3}, {"tag", createTag(t, taxonomy, token, name).ID, 2}} {
		current := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", fixture.kind, fixture.id)
		frozen := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE entity_type=? AND entity_id=?", fixture.kind, fixture.id)
		var payload map[string]any
		if json.Unmarshal([]byte(current), &payload) != nil || len(payload) != fixture.fields || payload["id"] != fixture.id || payload["name"] != name || payload["revision"] != nil || frozen != current {
			t.Fatal("taxonomy projection is not an exact frozen version-one payload")
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 3 || scalar[int](t, s.db, "SELECT revision FROM data_revision WHERE scope='catalog'") != 3 {
		t.Fatal("taxonomy mutation/event/catalog publication was not atomic")
	}
}

func TestTaxonomyCategoryParentProofsMovesCyclesAndAncestorRevisions(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	root := createCategory(t, taxonomy, token, "Root", nil)
	child := createCategory(t, taxonomy, token, "Child", &root.ID)
	leaf := createCategory(t, taxonomy, token, "Leaf", &child.ID)
	other := createCategory(t, taxonomy, token, "Other", nil)
	if getCategory(t, taxonomy, token, root.ID).Revision != 3 || getCategory(t, taxonomy, token, child.ID).Revision != 2 {
		t.Fatal("descendant category membership did not invalidate ancestors")
	}
	before := domainState(t, s)
	if _, err := taxonomy.CreateCategory(t.Context(), token, "stale-parent-category", 1, CategoryCreate{Name: "Stale", ParentID: &root.ID}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("old parent proof admitted a new child", err)
	}
	requireDomainState(t, s, before)
	for _, destination := range []string{root.ID, child.ID, leaf.ID} {
		if _, err := taxonomy.PatchCategory(t.Context(), token, "cycle-"+destination, root.ID, 3, CategoryPatch{ParentID: NullableID{Set: true, Value: &destination}}); !errors.Is(err, ErrCategoryCycle) {
			t.Fatal("category cycle admitted", err)
		}
		requireDomainState(t, s, before)
	}
	moved, err := taxonomy.PatchCategory(t.Context(), token, "move-category-key", child.ID, 2, CategoryPatch{ParentID: NullableID{Set: true, Value: &other.ID}})
	if err != nil || moved.ParentID == nil || *moved.ParentID != other.ID || moved.Name != child.Name || moved.Revision != 3 ||
		getCategory(t, taxonomy, token, root.ID).Revision != 4 || getCategory(t, taxonomy, token, other.ID).Revision != 2 || getCategory(t, taxonomy, token, leaf.ID).Revision != 1 {
		t.Fatal("move touched the wrong paths or rewrote descendants", err)
	}
	rooted, err := taxonomy.PatchCategory(t.Context(), token, "root-category-key", child.ID, 3, CategoryPatch{ParentID: NullableID{Set: true}})
	if err != nil || rooted.ParentID != nil || rooted.Name != child.Name || getCategory(t, taxonomy, token, other.ID).Revision != 3 {
		t.Fatal("explicit null parent was not a root move", err)
	}
}

func TestTaxonomyNoOpsKeepProjectionRevisionsButHaveImmutableAcknowledgements(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	category := createCategory(t, taxonomy, token, "Category", nil)
	tag := createTag(t, taxonomy, token, "Tag")
	changes := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes")
	categoryResult, err := taxonomy.PatchCategory(t.Context(), token, "noop-category-key", category.ID, 1, CategoryPatch{Name: &category.Name, ParentID: NullableID{Set: true}})
	if err != nil || !reflect.DeepEqual(categoryResult, category) {
		t.Fatal("category no-op invented state", err)
	}
	tagResult, err := taxonomy.PatchTag(t.Context(), token, "noop-tag-key", tag.ID, 1, tag.Name)
	if err != nil || !reflect.DeepEqual(tagResult, tag) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != changes ||
		scalar[int](t, s.db, "SELECT revision FROM data_revision WHERE scope='catalog'") != 2 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 4 {
		t.Fatal("no-op duplicated a projection or lost its immutable acknowledgement", err)
	}
}

func TestTaxonomyTagNamesAreExactUniqueAndDeletedNamesGetNewIdentity(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	original := createTag(t, taxonomy, token, "Case")
	for _, name := range []string{"case", " Case ", "é", "é"} {
		createTag(t, taxonomy, token, name)
	}
	before := domainState(t, s)
	if _, err := taxonomy.CreateTag(t.Context(), token, "duplicate-tag-key", "Case"); !errors.Is(err, ErrTagName) {
		t.Fatal("an exact duplicate tag name was allowed", err)
	}
	if _, err := taxonomy.PatchTag(t.Context(), token, "duplicate-rename-key", original.ID, 1, "case"); !errors.Is(err, ErrTagName) {
		t.Fatal("rename bypassed exact name uniqueness", err)
	}
	requireDomainState(t, s, before)
	ack, err := taxonomy.DeleteTag(t.Context(), token, "delete-tag-key", original.ID, 1)
	if err != nil || ack.Revision != 2 {
		t.Fatal("tag deletion failed", err)
	}
	replacement, err := taxonomy.CreateTag(t.Context(), token, "replacement-tag-key", "Case")
	if err != nil || replacement.ID == original.ID || replacement.Revision != 1 || scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='tag' AND entity_id=?", original.ID) != 2 {
		t.Fatal("a same-name replacement reused old identity or counter", err)
	}
	if _, err := taxonomy.GetTag(t.Context(), token, original.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted tag remained publicly readable", err)
	}
}

func TestTaxonomyDeleteRejectsChildBranchesAndReplaysFrozenResultsAcrossSessions(t *testing.T) {
	s, taxonomy, auth, token := taxonomyFixture(t)
	root := createCategory(t, taxonomy, token, "Root", nil)
	child := createCategory(t, taxonomy, token, "Child", &root.ID)
	before := domainState(t, s)
	if _, err := taxonomy.DeleteCategory(t.Context(), token, "delete-parent-key", root.ID, 2); !errors.Is(err, ErrCategoryChildren) {
		t.Fatal("delete implicitly removed a category branch", err)
	}
	requireDomainState(t, s, before)
	first, err := taxonomy.DeleteCategory(t.Context(), token, "delete-category-key", child.ID, 1)
	if err != nil || first.Revision != 2 || getCategory(t, taxonomy, token, root.ID).Revision != 3 {
		t.Fatal("leaf delete did not invalidate its old parent", err)
	}
	_, freshToken := login(t, auth)
	replay, err := taxonomy.DeleteCategory(t.Context(), freshToken, "delete-category-key", child.ID, 1)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatal("fresh session could not reconcile the original delete", err)
	}
	if _, err := taxonomy.GetCategory(t.Context(), token, child.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted category was still readable", err)
	}
	tag := createTag(t, taxonomy, token, "Tag")
	_, err = taxonomy.PatchTag(t.Context(), token, "rename-tag-key", tag.ID, 1, "Renamed")
	if err != nil {
		t.Fatal(err)
	}
	tagReplay, err := taxonomy.CreateTag(t.Context(), freshToken, "create-tag-Tag", "Tag")
	if err != nil || !reflect.DeepEqual(tagReplay, tag) {
		t.Fatal("create replay replaced a historical reply with fresh state", err)
	}
	if _, err := taxonomy.CreateTag(t.Context(), token, "create-tag-Tag", "Changed"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("changed request reused a domain key", err)
	}
	if _, err := taxonomy.CreateCategory(t.Context(), token, "create-tag-Tag", 0, CategoryCreate{Name: "Tag"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("another operation reused a domain key", err)
	}
}

func TestTaxonomyListPagesAreCompleteStableAndAuthenticated(t *testing.T) {
	_, taxonomy, _, token := taxonomyFixture(t)
	for index := range 5 {
		createCategory(t, taxonomy, token, fmt.Sprintf("Category-%d", index), nil)
		createTag(t, taxonomy, token, fmt.Sprintf("Tag-%d", index))
	}
	var categoryIDs, tagIDs []string
	var categoryAfter, tagAfter string
	for {
		categories, err := taxonomy.ListCategories(t.Context(), token, categoryAfter, 2)
		if err != nil || categories.Data == nil || len(categories.Data) > 2 {
			t.Fatal("category page failed", err)
		}
		for _, value := range categories.Data {
			categoryIDs = append(categoryIDs, value.ID)
		}
		categoryAfter = categories.NextID
		if categoryAfter == "" {
			break
		}
	}
	for {
		tags, err := taxonomy.ListTags(t.Context(), token, tagAfter, 2)
		if err != nil || tags.Data == nil || len(tags.Data) > 2 {
			t.Fatal("tag page failed", err)
		}
		for _, value := range tags.Data {
			tagIDs = append(tagIDs, value.ID)
		}
		tagAfter = tags.NextID
		if tagAfter == "" {
			break
		}
	}
	if len(categoryIDs) != 5 || len(tagIDs) != 5 {
		t.Fatal("stable continuation skipped records")
	}
	for index := 1; index < 5; index++ {
		if categoryIDs[index] <= categoryIDs[index-1] || tagIDs[index] <= tagIDs[index-1] {
			t.Fatal("metadata pages were not ordered by stable ID")
		}
	}
	if _, err := taxonomy.ListTags(t.Context(), "invalid", "", 2); !errors.Is(err, ErrAuthentication) {
		t.Fatal("an unauthenticated page disclosed taxonomy", err)
	}
}

func TestTaxonomyConcurrentConflictingCategoryMovesCannotCreateCycle(t *testing.T) {
	_, taxonomy, _, token := taxonomyFixture(t)
	left := createCategory(t, taxonomy, token, "Left", nil)
	right := createCategory(t, taxonomy, token, "Right", nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for index, pair := range [][2]Category{{left, right}, {right, left}} {
		group.Go(func() {
			<-start
			_, err := taxonomy.PatchCategory(context.Background(), token, fmt.Sprintf("concurrent-move-%d", index), pair[0].ID, 1, CategoryPatch{ParentID: NullableID{Set: true, Value: &pair[1].ID}})
			results <- err
		})
	}
	close(start)
	group.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrRevisionConflict) && !errors.Is(err, ErrCategoryCycle) {
			t.Fatal("unexpected move failure", err)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent opposite moves did not have one winner", successes)
	}
}

func TestTaxonomyAdmissionBoundsAndExpiredClaims(t *testing.T) {
	s, taxonomy, auth, token := taxonomyFixture(t)
	category := createCategory(t, taxonomy, token, "Category", nil)
	for _, name := range []string{"", strings.Repeat("x", 201), string([]byte{0xff})} {
		if _, err := taxonomy.CreateCategory(t.Context(), token, "invalid-category-key", 0, CategoryCreate{Name: name}); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid category name accepted", err)
		}
		if _, err := taxonomy.CreateTag(t.Context(), token, "invalid-tag-key", name); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid tag name accepted", err)
		}
	}
	longName := strings.Repeat("😀", 200)
	if _, err := taxonomy.CreateTag(t.Context(), token, "maximum-name-key", longName); err != nil {
		t.Fatal("valid maximum code-point name rejected", err)
	}
	for _, patch := range []CategoryPatch{{}, {ParentID: NullableID{Value: &category.ID}}, {ParentID: NullableID{Set: true, Value: testPointer("")}}, {Name: testPointer("")}} {
		if _, err := taxonomy.PatchCategory(t.Context(), token, "invalid-patch-key", category.ID, 1, patch); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid presence/metadata command accepted", err)
		}
	}
	if _, err := taxonomy.CreateCategory(t.Context(), token, "missing-parent-proof", 0, CategoryCreate{Name: "Child", ParentID: &category.ID}); !errors.Is(err, ErrPreconditionRequired) {
		t.Fatal("child category omitted its parent proof", err)
	}
	if _, err := taxonomy.CreateCategory(t.Context(), token, "unrelated-parent-proof", 1, CategoryCreate{Name: "Root"}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("root category accepted an unrelated proof", err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := taxonomy.ListCategories(t.Context(), token, "", limit); !errors.Is(err, ErrDomainInput) {
			t.Fatal("unbounded category read accepted", err)
		}
		if _, err := taxonomy.ListTags(t.Context(), token, "", limit); !errors.Is(err, ErrDomainInput) {
			t.Fatal("unbounded tag read accepted", err)
		}
	}
	now := auth.now()
	execSQL(t, s.db, "UPDATE api_idempotency SET created_at=?,expires_at=? WHERE operation='post_categories'", stamp(now.Add(-domainReplyLifetime-time.Hour)), stamp(now.Add(-time.Minute)))
	before := domainState(t, s)
	if _, err := taxonomy.CreateCategory(t.Context(), token, "create-category-Category", 0, CategoryCreate{Name: "Category"}); !errors.Is(err, ErrIdempotencyExpired) {
		t.Fatal("expired claim became another category write", err)
	}
	requireDomainState(t, s, before)
}
