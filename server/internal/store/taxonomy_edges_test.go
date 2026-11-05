package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestTaxonomyConstructionAndBoundaryFailuresDoNotExposeResources(t *testing.T) {
	s, taxonomy, auth, token := taxonomyFixture(t)
	if _, err := NewTaxonomy(nil); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil authorization service was accepted")
	}
	if _, err := NewTaxonomy(&Auth{}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("unbound authorization service was accepted")
	}
	if _, err := (*Store)(nil).NewTaxonomy(auth); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil storage boundary was accepted")
	}
	if _, err := s.NewTaxonomy(nil); !errors.Is(err, ErrDomainInput) {
		t.Fatal("missing authorization was accepted")
	}
	if _, err := openTestStore(t).NewTaxonomy(auth); !errors.Is(err, ErrDomainInput) {
		t.Fatal("taxonomy bound to another authoritative database")
	}
	category := createCategory(t, taxonomy, token, "Category", nil)
	tag := createTag(t, taxonomy, token, "Tag")
	checks := []func() error{
		func() error { _, err := taxonomy.GetCategory(t.Context(), token, ""); return err },
		func() error { _, err := taxonomy.GetTag(t.Context(), token, ""); return err },
		func() error {
			_, err := taxonomy.ListCategories(t.Context(), token, string([]byte{0xff}), 2)
			return err
		},
		func() error { _, err := taxonomy.ListTags(t.Context(), token, string([]byte{0xff}), 2); return err },
		func() error {
			_, err := taxonomy.DeleteCategory(t.Context(), token, "invalid-delete-category", "", 1)
			return err
		},
		func() error {
			_, err := taxonomy.DeleteTag(t.Context(), token, "invalid-delete-tag", "", 1)
			return err
		},
		func() error {
			_, err := taxonomy.PatchTag(t.Context(), token, "invalid-patch-tag", tag.ID, 1, "")
			return err
		},
	}
	before := domainState(t, s)
	for _, check := range checks {
		if err := check(); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid identity/input did not fail at the shared boundary", err)
		}
	}
	for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
		want := ErrDomainInput
		if revision == 0 {
			want = ErrPreconditionRequired
		}
		for _, check := range []func() error{
			func() error {
				_, err := taxonomy.PatchCategory(t.Context(), token, "bad-category-proof", category.ID, revision, CategoryPatch{Name: testPointer("New")})
				return err
			},
			func() error {
				_, err := taxonomy.DeleteCategory(t.Context(), token, "bad-category-delete-proof", category.ID, revision)
				return err
			},
			func() error {
				_, err := taxonomy.PatchTag(t.Context(), token, "bad-tag-proof", tag.ID, revision, "New")
				return err
			},
			func() error {
				_, err := taxonomy.DeleteTag(t.Context(), token, "bad-tag-delete-proof", tag.ID, revision)
				return err
			},
		} {
			if err := check(); !errors.Is(err, want) {
				t.Fatal("invalid revision bypassed the shared admission check", err)
			}
		}
	}
	for _, check := range []func() error{
		func() error {
			_, err := taxonomy.PatchCategory(t.Context(), token, "stale-category-proof", category.ID, 2, CategoryPatch{Name: testPointer("New")})
			return err
		},
		func() error {
			_, err := taxonomy.DeleteCategory(t.Context(), token, "stale-category-delete", category.ID, 2)
			return err
		},
		func() error {
			_, err := taxonomy.PatchTag(t.Context(), token, "stale-tag-proof", tag.ID, 2, "New")
			return err
		},
		func() error {
			_, err := taxonomy.DeleteTag(t.Context(), token, "stale-tag-delete", tag.ID, 2)
			return err
		},
	} {
		if err := check(); !errors.Is(err, ErrRevisionConflict) {
			t.Fatal("stale target proof was accepted", err)
		}
	}
	if value, err := taxonomy.GetCategory(t.Context(), token, "missing"); !errors.Is(err, ErrNotFound) || value.ID != "" {
		t.Fatal("missing category produced a resource", err)
	}
	if value, err := taxonomy.GetTag(t.Context(), token, "missing"); !errors.Is(err, ErrNotFound) || value.ID != "" {
		t.Fatal("missing tag produced a resource", err)
	}
	requireDomainState(t, s, before)
}

func TestTaxonomyDependentItemProjectionPreservesExplicitNullsAndArchiveState(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,status,created_at,updated_at) VALUES('legacy-item','Legacy','individual','archived','2026','2026')")
	_, err := runDomainMutation(t.Context(), taxonomy.auth, token, "legacy-null-publication", "test_legacy_null_publication", struct{}{}, func(m *mutationTx) (ActionAck, error) {
		if err := m.touchItemSearch("legacy-item", true); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='item' AND entity_id='legacy-item'")
	if encoded != `{"id":"legacy-item","name":"Legacy","tracking_mode":"individual","unit_code":"pcs","category_id":null,"brand":null,"model":null,"description":"","notes":"","base_unit":"pcs","scale_factor":1,"status":"archived"}` || approvedText(t, s, "legacy-item") != "Legacy" {
		t.Fatal("dependent publication invented defaults or changed nullable/archive metadata")
	}
}

func TestTaxonomyEveryPublicMutationHasExactProjectionReplay(t *testing.T) {
	for _, operation := range []string{"root create", "child create", "category rename", "category move", "category delete", "tag create", "tag rename", "tag delete", "category noop", "tag noop"} {
		t.Run(operation, func(t *testing.T) {
			s, taxonomy, _, token := taxonomyFixture(t)
			root := createCategory(t, taxonomy, token, "Root", nil)
			child := createCategory(t, taxonomy, token, "Child", &root.ID)
			tag := createTag(t, taxonomy, token, "Tag")
			changed := requireProjectionReplay(t, s, func() {
				var err error
				switch operation {
				case "root create":
					_, err = taxonomy.CreateCategory(t.Context(), token, "replay-new-root", 0, CategoryCreate{Name: "New"})
				case "child create":
					_, err = taxonomy.CreateCategory(t.Context(), token, "replay-new-child", 2, CategoryCreate{Name: "New", ParentID: &root.ID})
				case "category rename":
					_, err = taxonomy.PatchCategory(t.Context(), token, "replay-rename-category", child.ID, 1, CategoryPatch{Name: testPointer("Renamed")})
				case "category move":
					_, err = taxonomy.PatchCategory(t.Context(), token, "replay-move-category", child.ID, 1, CategoryPatch{ParentID: NullableID{Set: true}})
				case "category delete":
					_, err = taxonomy.DeleteCategory(t.Context(), token, "replay-delete-category", child.ID, 1)
				case "tag create":
					_, err = taxonomy.CreateTag(t.Context(), token, "replay-create-tag", "New")
				case "tag rename":
					_, err = taxonomy.PatchTag(t.Context(), token, "replay-rename-tag", tag.ID, 1, "Renamed")
				case "tag delete":
					_, err = taxonomy.DeleteTag(t.Context(), token, "replay-delete-tag", tag.ID, 1)
				case "category noop":
					_, err = taxonomy.PatchCategory(t.Context(), token, "replay-noop-category", child.ID, 1, CategoryPatch{Name: &child.Name})
				case "tag noop":
					_, err = taxonomy.PatchTag(t.Context(), token, "replay-noop-tag", tag.ID, 1, tag.Name)
				}
				if err != nil {
					t.Fatal(err)
				}
			})
			if operation == "category noop" || operation == "tag noop" {
				if len(changed) != 0 {
					t.Fatal("no-op invented a frozen projection change")
				}
			} else if len(changed) == 0 {
				t.Fatal("supported mutation omitted all projections")
			}
		})
	}
}

func TestTaxonomyConcurrentDuplicateRequestsHaveOneIdentityPerOperation(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	start := make(chan struct{})
	results := make(chan struct{ kind, id string }, 12)
	errorsOut := make(chan error, 12)
	var group sync.WaitGroup
	for index := range 12 {
		group.Go(func() {
			<-start
			if index%2 == 0 {
				value, err := taxonomy.CreateCategory(t.Context(), token, "concurrent-category-key", 0, CategoryCreate{Name: "Category"})
				errorsOut <- err
				results <- struct{ kind, id string }{"category", value.ID}
			} else {
				value, err := taxonomy.CreateTag(t.Context(), token, "concurrent-tag-key", "Tag")
				errorsOut <- err
				results <- struct{ kind, id string }{"tag", value.ID}
			}
		})
	}
	close(start)
	group.Wait()
	close(errorsOut)
	close(results)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	identities := make(map[string]string)
	for result := range results {
		if prior := identities[result.kind]; result.id == "" || prior != "" && result.id != prior {
			t.Fatal("duplicate caller received a replacement identity")
		}
		identities[result.kind] = result.id
	}
	if len(identities) != 2 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 2 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 2 {
		t.Fatal("concurrent retry duplicated source/event/projection effects")
	}
}

func TestTaxonomyConcurrentChildCreationHonorsOriginalParentProof(t *testing.T) {
	_, taxonomy, _, token := taxonomyFixture(t)
	parent := createCategory(t, taxonomy, token, "Parent", nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for index := range 2 {
		group.Go(func() {
			<-start
			_, err := taxonomy.CreateCategory(t.Context(), token, fmt.Sprintf("same-parent-child-%d", index), 1, CategoryCreate{Name: fmt.Sprintf("Child-%d", index), ParentID: &parent.ID})
			results <- err
		})
	}
	close(start)
	group.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrRevisionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 || getCategory(t, taxonomy, token, parent.ID).Revision != 2 {
		t.Fatal("a stale parent proof was silently refreshed")
	}
}

func TestTaxonomyArbitraryCategoryDepthHasNoRecursiveLimit(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	const depth = 1105
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index := range depth {
		id := fmt.Sprintf("category-%04d", index)
		var parent *string
		if index > 0 {
			parent = testPointer(fmt.Sprintf("category-%04d", index-1))
		}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{"INSERT INTO categories(id,parent_id,name) VALUES(?,?,?)", []any{id, parent, id}},
			{"INSERT INTO projection_revisions VALUES('category',?,1)", []any{id}},
			{"INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('category',?,1,json_object('id',?,'parent_id',?,'name',?),'2026')", []any{id, id, parent, id}},
		} {
			if _, err := tx.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	leaf := fmt.Sprintf("category-%04d", depth-1)
	changed := requireProjectionReplay(t, s, func() {
		if _, err := taxonomy.PatchCategory(t.Context(), token, "deep-category-rename", leaf, 1, CategoryPatch{Name: testPointer("DeepLeaf")}); err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != depth || getCategory(t, taxonomy, token, "category-0000").Revision != 2 {
		t.Fatal("deep ancestry was truncated or unchanged nodes were skipped")
	}
	if _, err := taxonomy.PatchCategory(t.Context(), token, "deep-category-cycle", "category-0000", 2, CategoryPatch{ParentID: NullableID{Set: true, Value: &leaf}}); !errors.Is(err, ErrCategoryCycle) {
		t.Fatal("deep descendant cycle was not rejected", err)
	}
}

func TestTaxonomyAllMutationsAndReadsRequireActiveCompletedEpochSession(t *testing.T) {
	for _, state := range []string{"mandatory", "revoked", "epoch", "expired", "invalid"} {
		t.Run(state, func(t *testing.T) {
			s, taxonomy, auth, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "Category", nil)
			tag := createTag(t, taxonomy, token, "Tag")
			want := ErrAuthentication
			switch state {
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(auth.now()))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='new-epoch'")
			case "expired":
				execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(auth.now()))
			case "invalid":
				token = "invalid"
			}
			before := domainState(t, s)
			checks := []func() error{
				func() error { _, err := taxonomy.GetCategory(t.Context(), token, category.ID); return err },
				func() error { _, err := taxonomy.GetTag(t.Context(), token, tag.ID); return err },
				func() error { _, err := taxonomy.ListCategories(t.Context(), token, "", 100); return err },
				func() error { _, err := taxonomy.ListTags(t.Context(), token, "", 100); return err },
				func() error {
					_, err := taxonomy.CreateCategory(t.Context(), token, "denied-create-category", 0, CategoryCreate{Name: "New"})
					return err
				},
				func() error { _, err := taxonomy.CreateTag(t.Context(), token, "denied-create-tag", "New"); return err },
				func() error {
					_, err := taxonomy.PatchCategory(t.Context(), token, "denied-patch-category", category.ID, 1, CategoryPatch{Name: testPointer("New")})
					return err
				},
				func() error {
					_, err := taxonomy.PatchTag(t.Context(), token, "denied-patch-tag", tag.ID, 1, "New")
					return err
				},
				func() error {
					_, err := taxonomy.DeleteCategory(t.Context(), token, "denied-delete-category", category.ID, 1)
					return err
				},
				func() error {
					_, err := taxonomy.DeleteTag(t.Context(), token, "denied-delete-tag", tag.ID, 1)
					return err
				},
				func() error {
					_, err := taxonomy.CreateCategory(t.Context(), token, "create-category-Category", 0, CategoryCreate{Name: "Category"})
					return err
				},
				func() error { _, err := taxonomy.CreateTag(t.Context(), token, "create-tag-Tag", "Tag"); return err },
			}
			for _, check := range checks {
				if err := check(); !errors.Is(err, want) {
					t.Fatal("taxonomy operation bypassed transactional authorization", err)
				}
				requireDomainState(t, s, before)
			}
		})
	}
}
