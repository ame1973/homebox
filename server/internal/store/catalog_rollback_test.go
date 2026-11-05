package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCatalogCreateRollsBackEverySourceDerivedProjectionAndReplyWrite(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"items", "INSERT"}, {"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
		{"current_projections", "INSERT"}, {"search_documents", "INSERT"}, {"sync_changes", "INSERT"},
		{"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			taxonomy, err := NewTaxonomy(auth)
			if err != nil {
				t.Fatal(err)
			}
			category := createCategory(t, taxonomy, token, "Category", nil)
			input := ItemCreate{Name: "NeverPartiallyCreated", CategoryID: &category.ID}
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_catalog_create BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'creation failure'); END")
			if value, err := catalog.Create(t.Context(), token, "rollback-create-item", input); err == nil || value.ID != "" {
				t.Fatal("failed creation exposed a successful resource", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'NeverPartiallyCreated'") != 0 {
				t.Fatal("failed creation left a searchable partial item")
			}
			execSQL(t, s.db, "DROP TRIGGER fail_catalog_create")
			if value, err := catalog.Create(t.Context(), token, "rollback-create-item", input); err != nil || value.Revision != 1 {
				t.Fatal("rolled-back key or connection could not be explicitly reused", err)
			}
		})
	}
}

func TestCatalogPatchRollsBackEverySourceFTSProjectionAndReplyWrite(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"items", "UPDATE"}, {"projection_revisions", "UPDATE"}, {"current_projections", "INSERT"}, {"current_projections", "UPDATE"},
		{"search_documents", "INSERT"}, {"search_documents", "UPDATE"}, {"sync_changes", "INSERT"},
		{"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "BeforeTitle"})
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_catalog_patch BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'patch failure'); END")
			if value, err := catalog.Patch(t.Context(), token, "rollback-patch-item", item.ID, 1, ItemPatch{Name: testPointer("AfterTitle")}); err == nil || value.ID != "" {
				t.Fatal("failed patch exposed a successful resource", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'BeforeTitle'") != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AfterTitle'") != 0 {
				t.Fatal("failed patch partially replaced FTS terms")
			}
			execSQL(t, s.db, "DROP TRIGGER fail_catalog_patch")
			if value, err := catalog.Patch(t.Context(), token, "rollback-patch-item", item.ID, 1, ItemPatch{Name: testPointer("AfterTitle")}); err != nil || value.Revision != 2 {
				t.Fatal("rolled-back original patch could not be explicitly retried", err)
			}
		})
	}
}

func TestCatalogDeleteRollsBackEveryOwnedCascadeCounterTombstoneAndReply(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"items", "DELETE"}, {"item_tags", "DELETE"}, {"item_barcodes", "DELETE"}, {"item_aliases", "DELETE"},
		{"item_attributes", "DELETE"}, {"media_links", "DELETE"}, {"ai_review_entries", "DELETE"}, {"ai_jobs", "DELETE"},
		{"search_documents", "DELETE"}, {"projection_revisions", "UPDATE"}, {"current_projections", "DELETE"},
		{"sync_changes", "INSERT"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			fixture := catalogWithOwnedRows(t)
			s, catalog, item := fixture.store, fixture.catalog, fixture.item
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_catalog_delete BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'delete failure'); END")
			if ack, err := catalog.Delete(t.Context(), fixture.token, "rollback-delete-item", item.ID, item.Revision); err == nil || ack.OK {
				t.Fatal("a failed owned cascade/deletion exposed an acknowledgement", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'DeleteCatalogTitle'") != 1 {
				t.Fatal("failed deletion removed published terms")
			}
			execSQL(t, s.db, "DROP TRIGGER fail_catalog_delete")
			if ack, err := catalog.Delete(t.Context(), fixture.token, "rollback-delete-item", item.ID, item.Revision); err != nil || !ack.OK {
				t.Fatal("rolled-back deletion key/proof/connection could not be explicitly retried", err)
			}
		})
	}
}

func TestCatalogLateAuthorizationAndDeferredCommitFailureWithholdAllMutationResults(t *testing.T) {
	for _, operation := range []string{"create", "patch", "archive", "delete"} {
		for _, failure := range []string{"late revocation", "deferred commit"} {
			t.Run(operation+failure, func(t *testing.T) {
				s, catalog, _, token := catalogFixture(t)
				item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Before"})
				if failure == "late revocation" {
					execSQL(t, s.db, "CREATE TRIGGER fail_catalog_late AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
				} else {
					execSQL(t, s.db, "CREATE TABLE catalog_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
					execSQL(t, s.db, "CREATE TRIGGER fail_catalog_late AFTER INSERT ON mutation_events BEGIN INSERT INTO catalog_commit_guard VALUES('missing'); END")
				}
				before := domainState(t, s)
				var err error
				var successful bool
				switch operation {
				case "create":
					var value Item
					value, err = catalog.Create(t.Context(), token, "late-catalog-failure", ItemCreate{Name: "After"})
					successful = value.ID != ""
				case "patch":
					var value Item
					value, err = catalog.Patch(t.Context(), token, "late-catalog-failure", item.ID, 1, ItemPatch{Name: testPointer("After")})
					successful = value.ID != ""
				case "archive":
					var value Item
					value, err = catalog.Archive(t.Context(), token, "late-catalog-failure", item.ID, 1, "Reason")
					successful = value.ID != ""
				case "delete":
					var value ActionAck
					value, err = catalog.Delete(t.Context(), token, "late-catalog-failure", item.ID, 1)
					successful = value.OK
				}
				if err == nil || successful || failure == "late revocation" && !errors.Is(err, ErrAuthentication) {
					t.Fatal("a late failure exposed a committed-looking resource/acknowledgement", err)
				}
				requireDomainState(t, s, before)
				if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM catalog_commit_guard") != 0 {
					t.Fatal("failed COMMIT retained a deferred guard row")
				}
			})
		}
	}
}

func setCatalogProjectionRevision(t *testing.T, s *Store, kind, id string, revision int64) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{"UPDATE projection_revisions SET revision=? WHERE entity_type=? AND entity_id=?", "UPDATE current_projections SET entity_revision=? WHERE entity_type=? AND entity_id=?"} {
		if _, err := tx.ExecContext(t.Context(), statement, revision, kind, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogSafeCounterAndSequenceOverflowRollbackAllDependentState(t *testing.T) {
	for _, scope := range []string{"item", "search", "catalog", "sequence", "category", "deleted item", "deleted tag"} {
		t.Run(scope, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Before"})
			var category Category
			switch scope {
			case "item":
				setCatalogProjectionRevision(t, s, "item", item.ID, maxSafeInteger)
				item.Revision = maxSafeInteger
			case "search":
				setCatalogProjectionRevision(t, s, "search_doc", projectionPair("item", item.ID), maxSafeInteger)
			case "catalog":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			case "category":
				taxonomy, err := NewTaxonomy(auth)
				if err != nil {
					t.Fatal(err)
				}
				category = createCategory(t, taxonomy, token, "Category", nil)
				setCatalogProjectionRevision(t, s, "category", category.ID, maxSafeInteger)
			case "deleted item", "deleted tag":
				fixture := catalogWithOwnedRows(t)
				s, catalog, token, item = fixture.store, fixture.catalog, fixture.token, fixture.item
				if scope == "deleted item" {
					setCatalogProjectionRevision(t, s, "item", item.ID, maxSafeInteger)
					item.Revision = maxSafeInteger
				} else {
					setCatalogProjectionRevision(t, s, "tag", fixture.tag.ID, maxSafeInteger)
				}
			}
			before := domainState(t, s)
			var err error
			var successful bool
			if scope == "deleted item" || scope == "deleted tag" {
				var ack ActionAck
				ack, err = catalog.Delete(t.Context(), token, "overflow-catalog-item", item.ID, item.Revision)
				successful = ack.OK
			} else {
				input := ItemPatch{Name: testPointer("After")}
				if scope == "category" {
					input.CategoryID = NullableID{Set: true, Value: &category.ID}
				}
				var value Item
				value, err = catalog.Patch(t.Context(), token, "overflow-catalog-item", item.ID, item.Revision, input)
				successful = value.ID != ""
			}
			if !errors.Is(err, ErrRevisionOverflow) || successful {
				t.Fatal("unsafe integer counter/sequence was returned as a resource", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogDeleteRejectsCorruptOwnedIdentitiesWithoutPartialCleanup(t *testing.T) {
	for _, table := range []string{"item_barcodes", "item_aliases", "item_attributes", "media_links", "ai_review_entries"} {
		t.Run(table, func(t *testing.T) {
			fixture := catalogWithOwnedRows(t)
			s, item := fixture.store, fixture.item
			execSQL(t, s.db, "UPDATE "+table+" SET id=? WHERE item_id=?", strings.Repeat("x", 129), item.ID)
			before := domainState(t, s)
			if ack, err := fixture.catalog.Delete(t.Context(), fixture.token, "corrupt-owned-identity", item.ID, item.Revision); !errors.Is(err, ErrDomainState) || ack.OK {
				t.Fatal("invalid owned identity escaped the independent tombstone boundary", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogDeleteExhaustedOwnedAndSearchCountersRollbackEarlierTombstones(t *testing.T) {
	for _, scope := range []string{"barcode", "item_tag", "category", "search_doc"} {
		t.Run(scope, func(t *testing.T) {
			fixture := catalogWithOwnedRows(t)
			s, item := fixture.store, fixture.item
			id := "owned-barcode"
			switch scope {
			case "item_tag":
				id = projectionPair(item.ID, fixture.tag.ID)
			case "category":
				id = fixture.category.ID
			case "search_doc":
				id = projectionPair("item", item.ID)
			}
			setCatalogProjectionRevision(t, s, scope, id, maxSafeInteger)
			before := domainState(t, s)
			if ack, err := fixture.catalog.Delete(t.Context(), fixture.token, "exhausted-owned-identity", item.ID, item.Revision); !errors.Is(err, ErrRevisionOverflow) || ack.OK {
				t.Fatal("exhausted independent counter exposed a partial cleanup", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogReadCommitFailureWithholdsResourcesAndPageContinuations(t *testing.T) {
	for _, operation := range []string{"get", "list"} {
		t.Run(operation, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Read"})
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			execSQL(t, s.db, "CREATE TABLE catalog_read_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
			execSQL(t, s.db, "CREATE TRIGGER fail_catalog_read AFTER UPDATE ON sessions BEGIN INSERT INTO catalog_read_guard VALUES('missing'); END")
			before := domainState(t, s)
			if operation == "get" {
				if value, err := catalog.Get(t.Context(), token, item.ID); err == nil || value.ID != "" {
					t.Fatal("failed read COMMIT exposed a resource", err)
				}
			} else if page, err := catalog.List(t.Context(), token, "", 1); err == nil || page.Data != nil || page.NextID != "" {
				t.Fatal("failed read COMMIT exposed a page/continuation", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM catalog_read_guard") != 0 {
				t.Fatal("read COMMIT failure retained its deferred guard row")
			}
		})
	}
}

func TestCatalogCreationCannotReuseAnAlreadyRetainedIdentity(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	before := domainState(t, s)
	execSQL(t, s.db, "CREATE TRIGGER reserve_catalog_identity BEFORE INSERT ON items BEGIN INSERT INTO projection_revisions VALUES('item',new.id,7); END")
	if value, err := catalog.Create(t.Context(), token, "retained-item-identity", ItemCreate{Name: "NeverReuse"}); !errors.Is(err, ErrDomainState) || value.ID != "" {
		t.Fatal("creation acquired a retained/tombstoned identity at a later revision", err)
	}
	requireDomainState(t, s, before)
}
