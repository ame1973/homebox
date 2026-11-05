package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCatalogTagsEveryStatementFailureRollsBackCompleteReplacement(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"item_tags", "INSERT"}, {"item_tags", "DELETE"}, {"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
		{"current_projections", "INSERT"}, {"current_projections", "UPDATE"}, {"current_projections", "DELETE"},
		{"search_documents", "INSERT"}, {"search_documents", "UPDATE"}, {"unit_search_documents", "INSERT"},
		{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"}, {"items", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			_, tags := catalogTags(t, auth, token, 2)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			if _, err := catalog.ReplaceTags(t.Context(), token, "tag-rollback-baseline", item.ID, 1, []string{tags[0].ID}); err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unit',?,'UNIT-001','2026','2026')", item.ID)
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_tag_replacement BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'tag replacement failure'); END")
			if value, err := catalog.ReplaceTags(t.Context(), token, "tag-rollback-original", item.ID, 2, []string{tags[1].ID}); err == nil || value.ID != "" {
				t.Fatal("partial set replacement exposed a committed-looking Item", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH ?", tags[0].Name) != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH ?", tags[1].Name) != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts") != 0 {
				t.Fatal("failed replacement left a partial current/FTS membership")
			}
			execSQL(t, s.db, "DROP TRIGGER fail_tag_replacement")
			if value, err := catalog.ReplaceTags(t.Context(), token, "tag-rollback-original", item.ID, 2, []string{tags[1].ID}); err != nil || value.Revision != 3 {
				t.Fatal("rolled-back original set/key/proof could not be explicitly retried", err)
			}
		})
	}
}

func TestCatalogTagsLateAuthorizationAndCommitFailuresWithholdTheOwningItem(t *testing.T) {
	for _, failure := range []string{"late revocation", "deferred commit"} {
		t.Run(failure, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			_, tags := catalogTags(t, auth, token, 1)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			if failure == "late revocation" {
				execSQL(t, s.db, "CREATE TRIGGER fail_tag_late AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
			} else {
				execSQL(t, s.db, "CREATE TABLE tag_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_tag_late AFTER INSERT ON mutation_events BEGIN INSERT INTO tag_commit_guard VALUES('missing'); END")
			}
			before := domainState(t, s)
			if value, err := catalog.ReplaceTags(t.Context(), token, "late-tag-set-original", item.ID, 1, []string{tags[0].ID}); err == nil || value.ID != "" || failure == "late revocation" && !errors.Is(err, ErrAuthentication) {
				t.Fatal("late failure released the owning Item before COMMIT", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH ?", tags[0].Name) != 0 {
				t.Fatal("failed authorization/COMMIT left searchable membership")
			}
		})
	}
}

func TestCatalogTagsRejectExhaustedMissingAndCorruptDependentAuthority(t *testing.T) {
	for _, scope := range []string{"owner", "removed tag", "added tag", "removed link", "added retained link", "item search", "unit search", "catalog", "sequence", "missing owner", "missing removed tag", "missing added tag", "bad current ID"} {
		t.Run(scope, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			_, tags := catalogTags(t, auth, token, 2)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			if _, err := catalog.ReplaceTags(t.Context(), token, "authority-tag-set-baseline", item.ID, 1, []string{tags[0].ID}); err != nil {
				t.Fatal(err)
			}
			proof, want := int64(2), ErrRevisionOverflow
			switch scope {
			case "owner":
				setCatalogProjectionRevision(t, s, "item", item.ID, maxSafeInteger)
				proof = maxSafeInteger
			case "removed tag":
				setCatalogProjectionRevision(t, s, "tag", tags[0].ID, maxSafeInteger)
			case "added tag":
				setCatalogProjectionRevision(t, s, "tag", tags[1].ID, maxSafeInteger)
			case "removed link":
				setCatalogProjectionRevision(t, s, "item_tag", projectionPair(item.ID, tags[0].ID), maxSafeInteger)
			case "added retained link":
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item_tag',?,?)", projectionPair(item.ID, tags[1].ID), maxSafeInteger)
			case "item search":
				setCatalogProjectionRevision(t, s, "search_doc", projectionPair("item", item.ID), maxSafeInteger)
			case "unit search":
				execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unit',?,'UNIT-001','2026','2026')", item.ID)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('search_doc',?,?)", projectionPair("unit", "unit"), maxSafeInteger)
			case "catalog":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			case "missing owner", "missing removed tag", "missing added tag":
				kind, id := "tag", tags[0].ID
				if scope == "missing owner" {
					kind, id = "item", item.ID
				} else if scope == "missing added tag" {
					id = tags[1].ID
				}
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", kind, id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, id)
				want = ErrDomainState
			case "bad current ID":
				id := strings.Repeat("x", 129)
				execSQL(t, s.db, "INSERT INTO tags VALUES(?,'InvalidImportedIdentity')", id)
				execSQL(t, s.db, "INSERT INTO item_tags VALUES(?,?)", item.ID, id)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item_tag',?,1)", projectionPair(item.ID, id))
				want = ErrDomainState
			}
			before := domainState(t, s)
			if value, err := catalog.ReplaceTags(t.Context(), token, "failed-authority-tag-set", item.ID, proof, []string{tags[1].ID}); !errors.Is(err, want) || value.ID != "" {
				t.Fatal("invalid dependent authority escaped a complete replacement", err)
			}
			requireDomainState(t, s, before)
		})
	}
}
