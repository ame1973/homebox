package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestTaxonomyCreationRollsBackSourceAndPublicationTogether(t *testing.T) {
	for _, kind := range []string{"category", "tag"} {
		for _, failure := range []struct{ table, event string }{
			{"source", "INSERT"}, {"projection_revisions", "INSERT"}, {"current_projections", "INSERT"},
			{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"},
			{"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
		} {
			t.Run(kind+failure.table, func(t *testing.T) {
				s, taxonomy, _, token := taxonomyFixture(t)
				table := failure.table
				if table == "source" {
					if kind == "category" {
						table = "categories"
					} else {
						table = "tags"
					}
				}
				before := domainState(t, s)
				execSQL(t, s.db, "CREATE TRIGGER fail_create_taxonomy BEFORE "+failure.event+" ON "+table+" BEGIN SELECT RAISE(ABORT,'creation failure'); END")
				var id string
				var err error
				if kind == "category" {
					var value Category
					value, err = taxonomy.CreateCategory(t.Context(), token, "failed-category-create", 0, CategoryCreate{Name: "New"})
					id = value.ID
				} else {
					var value Tag
					value, err = taxonomy.CreateTag(t.Context(), token, "failed-tag-create", "New")
					id = value.ID
				}
				if err == nil || id != "" {
					t.Fatal("failed creation exposed a resource")
				}
				requireDomainState(t, s, before)
			})
		}
	}
}

func TestTaxonomyCorruptReadDoesNotReturnPartialPagesOrResources(t *testing.T) {
	for _, kind := range []string{"category", "tag"} {
		t.Run(kind, func(t *testing.T) {
			s, taxonomy, _, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "Category", nil)
			tag := createTag(t, taxonomy, token, "Tag")
			if kind == "category" {
				execSQL(t, s.db, "UPDATE categories SET name='' WHERE id=?", category.ID)
				if value, err := taxonomy.GetCategory(t.Context(), token, category.ID); !errors.Is(err, ErrDomainState) || value.ID != "" {
					t.Fatal("corrupt category escaped runtime validation", err)
				}
				if page, err := taxonomy.ListCategories(t.Context(), token, "", 100); !errors.Is(err, ErrDomainState) || len(page.Data) != 0 {
					t.Fatal("corrupt category read published a partial page", err)
				}
				if page, err := taxonomy.PageCategories(t.Context(), token, "", 100); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
					t.Fatal("corrupt category read published signed continuation data", err)
				}
			} else {
				execSQL(t, s.db, "UPDATE tags SET name='' WHERE id=?", tag.ID)
				if value, err := taxonomy.GetTag(t.Context(), token, tag.ID); !errors.Is(err, ErrDomainState) || value.ID != "" {
					t.Fatal("corrupt tag escaped runtime validation", err)
				}
				if page, err := taxonomy.ListTags(t.Context(), token, "", 100); !errors.Is(err, ErrDomainState) || len(page.Data) != 0 {
					t.Fatal("corrupt tag read published a partial page", err)
				}
				if page, err := taxonomy.PageTags(t.Context(), token, "", 100); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
					t.Fatal("corrupt tag read published signed continuation data", err)
				}
			}
		})
	}
}

func TestTaxonomyRenameRollsBackEveryDependentWriteAndReusesOnlyRolledBackKeys(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"categories", "UPDATE"}, {"projection_revisions", "UPDATE"}, {"items", "UPDATE"},
		{"search_documents", "INSERT"}, {"search_documents", "UPDATE"},
		{"current_projections", "INSERT"}, {"current_projections", "UPDATE"},
		{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"},
		{"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			s, taxonomy, auth, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "BeforeClass", nil)
			seedCatalogItem(t, taxonomy, token, "item-1", &category.ID)
			category = getCategory(t, taxonomy, token, category.ID)
			before := domainState(t, s)
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			execSQL(t, s.db, "CREATE TRIGGER fail_taxonomy BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'dependent failure'); END")
			result, err := taxonomy.PatchCategory(t.Context(), token, "rollback-taxonomy-key", category.ID, category.Revision, CategoryPatch{Name: testPointer("AfterClass")})
			if err == nil || result.ID != "" {
				t.Fatal("failed dependent write returned a successful category")
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'BeforeClass'") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AfterClass'") != 0 {
				t.Fatal("failed source/projection publication leaked FTS terms")
			}
			execSQL(t, s.db, "DROP TRIGGER fail_taxonomy")
			if _, err := taxonomy.PatchCategory(t.Context(), token, "rollback-taxonomy-key", category.ID, category.Revision, CategoryPatch{Name: testPointer("AfterClass")}); err != nil {
				t.Fatal("rolled-back key, proof or pooled connection could not be explicitly retried", err)
			}
		})
	}
}

func TestTaxonomyDeletionRollsBackSourceLinksTombstonesAndSearchTogether(t *testing.T) {
	for _, kind := range []string{"category", "tag"} {
		for _, failure := range []struct{ table, event string }{
			{"items", "UPDATE"}, {"current_projections", "DELETE"}, {"search_documents", "UPDATE"},
			{"sync_changes", "INSERT"}, {"mutation_events", "INSERT"},
		} {
			t.Run(kind+failure.table, func(t *testing.T) {
				s, taxonomy, auth, token := taxonomyFixture(t)
				category := createCategory(t, taxonomy, token, "Category", nil)
				tag := createTag(t, taxonomy, token, "Tag")
				seedCatalogItem(t, taxonomy, token, "item-1", &category.ID, tag.ID)
				category = getCategory(t, taxonomy, token, category.ID)
				currentTag, err := taxonomy.GetTag(t.Context(), token, tag.ID)
				if err != nil {
					t.Fatal(err)
				}
				before := domainState(t, s)
				now := auth.now().Add(time.Minute)
				auth.now = func() time.Time { return now }
				execSQL(t, s.db, "CREATE TRIGGER fail_taxonomy_delete BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'delete failure'); END")
				var result ActionAck
				if kind == "category" {
					result, err = taxonomy.DeleteCategory(t.Context(), token, "rollback-delete-key", category.ID, category.Revision)
				} else {
					result, err = taxonomy.DeleteTag(t.Context(), token, "rollback-delete-key", tag.ID, currentTag.Revision)
				}
				if err == nil || result.OK {
					t.Fatal("partially failed delete returned an acknowledgement")
				}
				requireDomainState(t, s, before)
			})
		}
	}
	for _, table := range []string{"item_tags", "tags", "categories"} {
		t.Run("source-delete-"+table, func(t *testing.T) {
			s, taxonomy, _, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "Category", nil)
			tag := createTag(t, taxonomy, token, "Tag")
			seedCatalogItem(t, taxonomy, token, "item-1", &category.ID, tag.ID)
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_source_delete BEFORE DELETE ON "+table+" BEGIN SELECT RAISE(ABORT,'source failure'); END")
			var err error
			if table == "categories" {
				_, err = taxonomy.DeleteCategory(t.Context(), token, "rollback-source-delete", category.ID, 2)
			} else {
				_, err = taxonomy.DeleteTag(t.Context(), token, "rollback-source-delete", tag.ID, 2)
			}
			if err == nil {
				t.Fatal("source deletion failure was ignored")
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestTaxonomyDeferredCommitAndLateSearchFailuresWithholdEveryEffect(t *testing.T) {
	for _, failure := range []string{"deferred commit", "later search"} {
		t.Run(failure, func(t *testing.T) {
			s, taxonomy, _, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "BeforeClass", nil)
			seedCatalogItem(t, taxonomy, token, "item-1", &category.ID)
			seedCatalogItem(t, taxonomy, token, "item-2", &category.ID)
			category = getCategory(t, taxonomy, token, category.ID)
			before := domainState(t, s)
			if failure == "deferred commit" {
				execSQL(t, s.db, "CREATE TABLE taxonomy_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_taxonomy_commit AFTER INSERT ON mutation_events BEGIN INSERT INTO taxonomy_commit_guard VALUES('missing'); END")
			} else {
				execSQL(t, s.db, "CREATE TRIGGER fail_later_search BEFORE INSERT ON sync_changes WHEN new.entity_type='search_doc' AND new.entity_id=json_array('item','item-2') BEGIN SELECT RAISE(ABORT,'later search failure'); END")
			}
			result, err := taxonomy.PatchCategory(t.Context(), token, "late-failure-category", category.ID, category.Revision, CategoryPatch{Name: testPointer("AfterClass")})
			if err == nil || result.ID != "" {
				t.Fatal("late failure exposed a successful-looking DTO")
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'BeforeClass'") != 2 || scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AfterClass'") != 0 {
				t.Fatal("late failure committed a partially rebuilt FTS index")
			}
		})
	}
}

func TestTaxonomySourcePublicationRejectsInvalidStateAndUnboundedCycles(t *testing.T) {
	for _, corruption := range []string{"invalid item", "invalid text", "category cycle"} {
		t.Run(corruption, func(t *testing.T) {
			s, taxonomy, _, token := taxonomyFixture(t)
			root := createCategory(t, taxonomy, token, "Root", nil)
			child := createCategory(t, taxonomy, token, "Child", &root.ID)
			tag := createTag(t, taxonomy, token, "Tag")
			seedCatalogItem(t, taxonomy, token, "item-1", &child.ID, tag.ID)
			switch corruption {
			case "invalid item":
				execSQL(t, s.db, "UPDATE items SET unit_code='' WHERE id='item-1'")
			case "invalid text":
				execSQL(t, s.db, "INSERT INTO item_aliases VALUES('bad-alias','item-1',?,'2026')", string([]byte{0xff}))
			case "category cycle":
				execSQL(t, s.db, "UPDATE categories SET parent_id=? WHERE id=?", child.ID, root.ID)
			}
			before := domainState(t, s)
			result, err := taxonomy.PatchTag(t.Context(), token, "invalid-source-key", tag.ID, 2, "Changed")
			if !errors.Is(err, ErrDomainState) || result.ID != "" {
				t.Fatal("invalid source state was silently repaired or published", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestTaxonomyLateAuthorizationChangesRollBackDerivedPublication(t *testing.T) {
	for _, change := range []struct{ name, statement string }{
		{"revocation", "UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'"},
		{"epoch", "UPDATE sync_state SET dataset_epoch='changed-epoch'"},
		{"mandatory", "UPDATE admin_accounts SET must_change_password=1"},
		{"revision", "UPDATE admin_accounts SET revision=revision+1"},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, taxonomy, _, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "BeforeClass", nil)
			seedCatalogItem(t, taxonomy, token, "item-1", &category.ID)
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER alter_taxonomy_auth AFTER UPDATE ON categories BEGIN "+change.statement+"; END")
			result, err := taxonomy.PatchCategory(t.Context(), token, "late-auth-taxonomy", category.ID, 2, CategoryPatch{Name: testPointer("AfterClass")})
			if err == nil || result.ID != "" {
				t.Fatal("late authorization change published a domain mutation")
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestTaxonomyOverflowRollsBackApprovedSourceAndFrozenState(t *testing.T) {
	for _, scope := range []string{"category", "search_doc", "catalog", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			s, taxonomy, _, token := taxonomyFixture(t)
			category := createCategory(t, taxonomy, token, "BeforeClass", nil)
			seedCatalogItem(t, taxonomy, token, "item-1", &category.ID)
			revision := int64(2)
			switch scope {
			case "category", "search_doc":
				id := category.ID
				if scope == "search_doc" {
					id = projectionPair("item", "item-1")
				} else {
					revision = maxSafeInteger
				}
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, statement := range []string{"UPDATE projection_revisions SET revision=? WHERE entity_type=? AND entity_id=?", "UPDATE current_projections SET entity_revision=? WHERE entity_type=? AND entity_id=?"} {
					if _, err := tx.ExecContext(t.Context(), statement, maxSafeInteger, scope, id); err != nil {
						t.Fatal(err)
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			case "catalog":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			}
			before := domainState(t, s)
			result, err := taxonomy.PatchCategory(t.Context(), token, "overflow-taxonomy-key", category.ID, revision, CategoryPatch{Name: testPointer("AfterClass")})
			if !errors.Is(err, ErrRevisionOverflow) || result.ID != "" {
				t.Fatal("unsafe revision/sequence publication did not fail closed", fmt.Sprintf("%s: %v", scope, err))
			}
			requireDomainState(t, s, before)
		})
	}
}
