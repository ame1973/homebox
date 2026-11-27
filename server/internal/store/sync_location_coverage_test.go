package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSyncLocationCoverageChecksLiveAndRetainedDeletedSources(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	root := createLocation(t, locations, token, "COVERAGE-ROOT", nil)
	createLocation(t, locations, token, "COVERAGE-CHILD", &root.ID)
	removed := createLocation(t, locations, token, "COVERAGE-DELETED", nil)
	if _, err := locations.Delete(t.Context(), token, "coverage-delete", removed.ID, removed.Revision); err != nil {
		t.Fatal(err)
	}
	root = getLocation(t, locations, token, root.ID)
	if _, err := locations.Archive(t.Context(), token, "coverage-archive", root.ID, root.Revision, true, ""); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncLocationCoverage(t.Context(), tx)
		if err != nil || coverage.Live != 2 || coverage.Deleted != 1 {
			t.Fatal("location source coverage was not established", coverage, err)
		}
	})
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("location-only coverage enabled public synchronization", err)
	}
}

func TestSyncLocationCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	root := createLocation(t, locations, token, "COVERAGE-DAMAGE-ROOT", nil)
	child := createLocation(t, locations, token, "COVERAGE-DAMAGE-CHILD", &root.ID)
	deleted := createLocation(t, locations, token, "COVERAGE-DAMAGE-DELETED", nil)
	if _, err := locations.Delete(t.Context(), token, "coverage-damage-delete", deleted.ID, deleted.Revision); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing current", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "DELETE FROM current_projections WHERE entity_type='location' AND entity_id=?", child.ID); err != nil {
				t.Fatal(err)
			}
		}, errSyncLocationUncovered},
		{"changed projection", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "UPDATE current_projections SET payload_json=json_set(payload_json,'$.name','wrong') WHERE entity_type='location' AND entity_id=?", child.ID); err != nil {
				t.Fatal(err)
			}
		}, ErrDomainState},
		{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='location' AND entity_id=?", child.ID); err != nil {
				t.Fatal(err)
			}
		}, ErrDomainState},
		{"deleted source missing counter", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "DELETE FROM projection_revisions WHERE entity_type='location' AND entity_id=?", deleted.ID); err != nil {
				t.Fatal(err)
			}
		}, ErrDomainState},
		{"deleted source has current", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'location',l.id,r.revision,json_object('id',l.id,'parent_id',l.parent_id,'code',l.code,'name',l.name,
 'status',l.status,'kind',l.kind,'sort_order',l.sort_order,'description',l.description),'2026'
 FROM locations l JOIN projection_revisions r ON r.entity_type='location' AND r.entity_id=l.id WHERE l.id=?`, deleted.ID); err != nil {
				t.Fatal(err)
			}
		}, ErrDomainState},
		{"orphan current", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('location','orphan',1)"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('location','orphan',1,'{"id":"orphan"}','2026')`); err != nil {
				t.Fatal(err)
			}
		}, ErrDomainState},
		{"invalid deletion text", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "UPDATE locations SET deleted_at='' WHERE id=?", deleted.ID); err != nil {
				t.Fatal(err)
			}
		}, ErrDomainState},
		{"oversized deletion text", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "UPDATE locations SET deleted_at=? WHERE id=?", strings.Repeat("家", 13334), deleted.ID); err != nil {
				t.Fatal(err)
			}
		}, ErrDomainState},
		{"ancestor cycle", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "UPDATE locations SET parent_id=? WHERE id=?", child.ID, root.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(t.Context(), "UPDATE current_projections SET payload_json=json_set(payload_json,'$.parent_id',?) WHERE entity_type='location' AND entity_id=?", child.ID, root.ID); err != nil {
				t.Fatal(err)
			}
		}, ErrDomainState},
		{"deleted live parent", func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), "UPDATE locations SET deleted_at='2026' WHERE id=?", root.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(t.Context(), "DELETE FROM current_projections WHERE entity_type='location' AND entity_id=?", root.ID); err != nil {
				t.Fatal(err)
			}
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
			coverage, err := verifySyncLocationCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(coverage, syncLocationCoverage{}) {
				t.Fatal("damaged location source returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncLocationCoverageEmptyAndExactPageBoundary(t *testing.T) {
	for _, count := range []int{0, 100, 200} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, _, _, _ := locationFixture(t)
			ids := make([]string, 0, count)
			for index := 0; index < count; index++ {
				ids = append(ids, fmt.Sprintf("boundary-%03d", index))
			}
			seedSyncCoverageLocations(t, s, ids)
			withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
				coverage, err := verifySyncLocationCoverage(t.Context(), tx)
				if err != nil || coverage.Live != int64(count) || coverage.Deleted != 0 {
					t.Fatal("an empty or exact-boundary source set failed", coverage, err)
				}
			})
		})
	}
}

func TestSyncLocationCoveragePagesExactIDsAndWithholdsALateMissingRow(t *testing.T) {
	s, _, _, _ := locationFixture(t)
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("source-%03d", index))
	}
	firstBoundary := "source-098\x00-家庭"
	secondBoundary := "source-197\x00-第二"
	ids = append(ids, firstBoundary, secondBoundary, strings.Repeat("😀", 128), "zz-uncovered")
	seedSyncCoverageLocations(t, s, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncLocationCoverageIDs(t.Context(), tx, "", false)
		if err != nil || len(first) != 100 || first[99] != firstBoundary {
			t.Fatal("the first page did not preserve its embedded-NUL boundary", first, err)
		}
		second, err := readSyncLocationCoverageIDs(t.Context(), tx, first[99], true)
		if err != nil || len(second) != 100 || second[99] != secondBoundary {
			t.Fatal("the second page did not preserve its embedded-NUL boundary", second, err)
		}
		coverage, err := verifySyncLocationCoverage(t.Context(), tx)
		if err != nil || coverage.Live != int64(len(ids)) || coverage.Deleted != 0 {
			t.Fatal("multi-page source coverage changed exact identities", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(t.Context(), "DELETE FROM current_projections WHERE entity_type='location' AND entity_id='zz-uncovered'"); err != nil {
			t.Fatal(err)
		}
		coverage, err := verifySyncLocationCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncLocationUncovered) || !reflect.DeepEqual(coverage, syncLocationCoverage{}) {
			t.Fatal("a late missing projection returned a partial count", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncCoverageLocations(t *testing.T, s *Store, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, id := range ids {
		code := fmt.Sprintf("COVERAGE-%03d", index)
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO locations(id,parent_id,code,name,kind,status,sort_order,description,created_at,updated_at)
 VALUES(?,NULL,?,?,'other','active',0,'','2026','2026')`, id, code, code); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('location',?,1)", id); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'location',id,1,json_object('id',id,'parent_id',parent_id,'code',code,'name',name,'status',status,'kind',kind,
 'sort_order',sort_order,'description',description),'2026' FROM locations WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncLocationCoverageRejectsInvalidSourceIDs(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("bytes-%d", len(id)), func(t *testing.T) {
			s, _, _, _ := locationFixture(t)
			execSQL(t, s.db, `INSERT INTO locations(id,code,name,kind,status,sort_order,description,created_at,updated_at)
 VALUES(?,'BAD-SOURCE','Bad source','other','active',0,'','2026','2026')`, id)
			withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
				coverage, err := verifySyncLocationCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(coverage, syncLocationCoverage{}) {
					t.Fatal("an invalid source identity was skipped", coverage, err)
				}
			})
		})
	}
}

func TestSyncLocationCoverageWithholdsOversizedCurrent(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "COVERAGE-LARGE", nil)
	before := domainState(t, s)
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		if _, err = tx.ExecContext(t.Context(), "UPDATE current_projections SET payload_json=? WHERE entity_type='location' AND entity_id=?", oversized, location.ID); err != nil {
			t.Fatal(err)
		}
		coverage, err := verifySyncLocationCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncPayloadTooLarge) || !reflect.DeepEqual(coverage, syncLocationCoverage{}) {
			t.Fatal("an oversized current projection returned coverage", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestSyncLocationCoverageRejectsInvalidContextAndClosedTransaction(t *testing.T) {
	s, _, _, _ := locationFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncLocationCoverage(nil, tx); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(result, syncLocationCoverage{}) {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncLocationCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(result, syncLocationCoverage{}) {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncLocationCoverage(ctx, tx); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, syncLocationCoverage{}) {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncLocationCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || !reflect.DeepEqual(result, syncLocationCoverage{}) {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}

func TestSyncLocationCoverageRejectsMissingProjectionWithoutPartialCounts(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	createLocation(t, locations, token, "COVERAGE-VALID", nil)
	missing := createLocation(t, locations, token, "COVERAGE-MISSING", nil)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id=?", missing.ID)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncLocationCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncLocationUncovered) || !reflect.DeepEqual(coverage, syncLocationCoverage{}) {
			t.Fatal("missing location projection returned coverage", coverage, err)
		}
	})
	requireDomainState(t, s, before)
}
