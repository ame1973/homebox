package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncTaxonomyCoverageChecksRetainedSourcesAndDeletedCounters(t *testing.T) {
	s, taxonomy, auth, token := taxonomyFixture(t)
	root := createCategory(t, taxonomy, token, "COVERAGE-ROOT", nil)
	createCategory(t, taxonomy, token, "COVERAGE-CHILD", &root.ID)
	createTag(t, taxonomy, token, "COVERAGE-TAG")
	removedCategory := createCategory(t, taxonomy, token, "COVERAGE-REMOVED-CATEGORY", nil)
	removedTag := createTag(t, taxonomy, token, "COVERAGE-REMOVED-TAG")
	if _, err := taxonomy.DeleteCategory(t.Context(), token, "coverage-delete-category", removedCategory.ID, removedCategory.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := taxonomy.DeleteTag(t.Context(), token, "coverage-delete-tag", removedTag.ID, removedTag.Revision); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncTaxonomyCoverage(t.Context(), tx)
		if err != nil || coverage.Categories != 2 || coverage.Tags != 1 {
			t.Fatal("taxonomy sources and retained deletion counters were not handled", coverage, err)
		}
	})
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("taxonomy-only coverage enabled public synchronization", err)
	}
}

func TestSyncTaxonomyCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	root := createCategory(t, taxonomy, token, "DAMAGE-ROOT", nil)
	child := createCategory(t, taxonomy, token, "DAMAGE-CHILD", &root.ID)
	tag := createTag(t, taxonomy, token, "DAMAGE-TAG")
	before := domainState(t, s)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing category", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='category' AND entity_id=?", child.ID)
		}, errSyncTaxonomyUncovered},
		{"missing tag after categories", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='tag' AND entity_id=?", tag.ID)
		}, errSyncTaxonomyUncovered},
		{"stale category", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE categories SET name='changed source' WHERE id=?", child.ID)
		}, ErrDomainState},
		{"stale tag", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.name','wrong') WHERE entity_type='tag' AND entity_id=?", tag.ID)
		}, ErrDomainState},
		{"invalid category name", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE categories SET name='' WHERE id=?", child.ID)
		}, ErrDomainState},
		{"invalid tag name", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE tags SET name=CAST(X'ff' AS TEXT) WHERE id=?", tag.ID)
		}, ErrDomainState},
		{"category counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='category' AND entity_id=?", child.ID)
		}, ErrDomainState},
		{"tag counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='tag' AND entity_id=?", tag.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='tag' AND entity_id=?", tag.ID)
		}, ErrDomainState},
		{"category cycle", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE categories SET parent_id=? WHERE id=?", child.ID, root.ID)
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.parent_id',?) WHERE entity_type='category' AND entity_id=?", child.ID, root.ID)
		}, ErrDomainState},
		{"missing parent", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE categories SET parent_id='missing-parent' WHERE id=?", child.ID)
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.parent_id','missing-parent') WHERE entity_type='category' AND entity_id=?", child.ID)
		}, ErrDomainState},
		{"orphan category", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('category','orphan-category',1)")
			execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('category','orphan-category',1,'{"id":"orphan-category"}','2026')`)
		}, ErrDomainState},
		{"orphan tag", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('tag','orphan-tag',1)")
			execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('tag','orphan-tag',1,'{"id":"orphan-tag"}','2026')`)
		}, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			test.damage(t, tx)
			if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
					t.Error("restore query-only mode:", err)
				}
			}()
			coverage, err := verifySyncTaxonomyCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || coverage != (syncTaxonomyCoverage{}) {
				t.Fatal("damaged taxonomy returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncTaxonomyCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, _, _, _ := taxonomyFixture(t)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncTaxonomyCoverage(t.Context(), tx)
		if err != nil || coverage != (syncTaxonomyCoverage{}) {
			t.Fatal("empty taxonomy coverage failed", coverage, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncTaxonomyCoverage(t, s, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncTaxonomyCoverage(t.Context(), tx)
		if err != nil || coverage.Categories != 100 || coverage.Tags != 100 {
			t.Fatal("exact-boundary taxonomy coverage failed", coverage, err)
		}
	})
}

func TestSyncTaxonomyCoverageWithholdsOversizedCurrent(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	category := createCategory(t, taxonomy, token, "LARGE-CATEGORY", nil)
	tag := createTag(t, taxonomy, token, "LARGE-TAG")
	before := domainState(t, s)
	oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
	for _, target := range []struct{ kind, id string }{{"category", category.ID}, {"tag", tag.ID}} {
		t.Run(target.kind, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type=? AND entity_id=?", oversized, target.kind, target.id)
			coverage, err := verifySyncTaxonomyCoverage(t.Context(), tx)
			if !errors.Is(err, errSyncPayloadTooLarge) || coverage != (syncTaxonomyCoverage{}) {
				t.Fatal("an oversized taxonomy projection returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncTaxonomyCoveragePagesExactIDsAndWithholdsLateMissingRow(t *testing.T) {
	s, _, _, _ := taxonomyFixture(t)
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("source-%03d", index))
	}
	ids = append(ids, "source-098\x00-家庭", "source-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncTaxonomyCoverage(t, s, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		for _, kind := range []string{"category", "tag"} {
			first, err := readSyncCoverageIDs(t.Context(), tx, kind, "", false)
			if err != nil || len(first) != 100 || first[99] != "source-098\x00-家庭" {
				t.Fatal("the first taxonomy page changed its embedded-NUL boundary", kind, first, err)
			}
			second, err := readSyncCoverageIDs(t.Context(), tx, kind, first[99], true)
			if err != nil || len(second) != 100 || second[99] != "source-197\x00-第二" {
				t.Fatal("the second taxonomy page changed its embedded-NUL boundary", kind, second, err)
			}
		}
		coverage, err := verifySyncTaxonomyCoverage(t.Context(), tx)
		if err != nil || coverage.Categories != int64(len(ids)) || coverage.Tags != int64(len(ids)) {
			t.Fatal("multi-page taxonomy changed exact identities", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='tag' AND entity_id='zz-uncovered'")
		coverage, err := verifySyncTaxonomyCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncTaxonomyUncovered) || coverage != (syncTaxonomyCoverage{}) {
			t.Fatal("a late missing tag returned partial coverage", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncTaxonomyCoverage(t *testing.T, s *Store, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, id := range ids {
		name := fmt.Sprintf("coverage-%03d", index)
		execSyncCoverageSQL(t, tx, "INSERT INTO categories(id,name) VALUES(?,?)", id, name)
		execSyncCoverageSQL(t, tx, "INSERT INTO tags(id,name) VALUES(?,?)", id, name)
		for _, kind := range []string{"category", "tag"} {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?,?,1)", kind, id)
		}
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('category',?,1,json_object('id',?,'parent_id',NULL,'name',?),'2026')`, id, id, name)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('tag',?,1,json_object('id',?,'name',?),'2026')`, id, id, name)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncTaxonomyCoverageRejectsInvalidIDsAndTransactions(t *testing.T) {
	for _, kind := range []string{"category", "tag"} {
		for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
			t.Run(fmt.Sprintf("%s-bytes-%d", kind, len(id)), func(t *testing.T) {
				s, _, _, _ := taxonomyFixture(t)
				if kind == "category" {
					execSQL(t, s.db, "INSERT INTO categories(id,name) VALUES(?,'invalid identity')", id)
				} else {
					execSQL(t, s.db, "INSERT INTO tags(id,name) VALUES(?,'invalid identity')", id)
				}
				withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
					coverage, err := verifySyncTaxonomyCoverage(t.Context(), tx)
					if !errors.Is(err, ErrDomainState) || coverage != (syncTaxonomyCoverage{}) {
						t.Fatal("invalid taxonomy identity was skipped", coverage, err)
					}
				})
			})
		}
	}
	s, _, _, _ := taxonomyFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySyncTaxonomyCoverageEntry(t.Context(), tx, "not-a-kind", "id"); !errors.Is(err, ErrDomainState) {
		t.Fatal("unsupported taxonomy kind was accepted", err)
	}
	if result, err := verifySyncTaxonomyCoverage(nil, tx); !errors.Is(err, ErrDomainState) || result != (syncTaxonomyCoverage{}) {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncTaxonomyCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || result != (syncTaxonomyCoverage{}) {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncTaxonomyCoverage(ctx, tx); !errors.Is(err, context.Canceled) || result != (syncTaxonomyCoverage{}) {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncTaxonomyCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || result != (syncTaxonomyCoverage{}) {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}
