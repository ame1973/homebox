package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncLocationEventCoverageIncludesDeletedOwnersAndHistoricalParents(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	first := createLocation(t, locations, token, "EVENT-COVERAGE-FIRST", nil)
	second := createLocation(t, locations, token, "EVENT-COVERAGE-SECOND", nil)
	subject := createLocation(t, locations, token, "EVENT-COVERAGE-SUBJECT", &first.ID)
	moved, err := locations.Move(t.Context(), token, "event-coverage-move-one", subject.ID, subject.Revision, &second.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err = locations.Move(t.Context(), token, "event-coverage-move-two", subject.ID, moved.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = locations.Delete(t.Context(), token, "event-coverage-delete-subject", subject.ID, moved.Revision); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []Location{first, second} {
		current := getLocation(t, locations, token, parent.ID)
		if _, err = locations.Delete(t.Context(), token, "event-coverage-delete-"+parent.Code, parent.ID, current.Revision); err != nil {
			t.Fatal(err)
		}
	}
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncLocationEventCoverage(t.Context(), tx)
		if err != nil || count != 2 {
			t.Fatal("retained movements were not covered", count, err)
		}
	})
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("location event coverage enabled public synchronization", err)
	}
}

func TestSyncLocationEventCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	parent := createLocation(t, locations, token, "EVENT-DAMAGE-PARENT", nil)
	subject := createLocation(t, locations, token, "EVENT-DAMAGE-SUBJECT", nil)
	if _, err := locations.Move(t.Context(), token, "event-damage-move", subject.ID, subject.Revision, &parent.ID); err != nil {
		t.Fatal(err)
	}
	id := scalar[string](t, s.db, "SELECT id FROM location_events WHERE location_id=?", subject.ID)
	before := domainState(t, s)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing projection", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "DELETE FROM current_projections WHERE entity_type='location_event' AND entity_id=?", id)
		}, errSyncLocationEventUncovered},
		{"changed projection", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.occurred_at','different') WHERE entity_type='location_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='location_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"missing counter", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "DELETE FROM current_projections WHERE entity_type='location_event' AND entity_id=?", id)
			execSyncEventDamage(t, tx, "DELETE FROM projection_revisions WHERE entity_type='location_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"orphan projection", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "INSERT INTO projection_revisions VALUES('location_event','orphan',1)")
			execSyncEventDamage(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('location_event','orphan',1,'{"id":"orphan"}','2026')`)
		}, ErrDomainState},
		{"invalid event text", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "DROP TRIGGER location_events_no_update")
			execSyncEventDamage(t, tx, "UPDATE location_events SET occurred_at='' WHERE id=?", id)
		}, ErrDomainState},
		{"oversized event text", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "DROP TRIGGER location_events_no_update")
			execSyncEventDamage(t, tx, "UPDATE location_events SET occurred_at=? WHERE id=?", strings.Repeat("家", 13334), id)
		}, ErrDomainState},
		{"invalid UTF-8 event text", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "DROP TRIGGER location_events_no_update")
			execSyncEventDamage(t, tx, "UPDATE location_events SET occurred_at=CAST(X'ff' AS TEXT) WHERE id=?", id)
		}, ErrDomainState},
		{"owner projection differs", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "UPDATE locations SET name='changed actual owner' WHERE id=?", subject.ID)
		}, ErrDomainState},
		{"oversized historical parent", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncEventDamage(t, tx, "DROP TRIGGER location_events_no_update")
			execSyncEventDamage(t, tx, "UPDATE location_events SET to_parent_id=? WHERE id=?", strings.Repeat("家", 171), id)
		}, ErrDomainState},
		{"missing historical parent", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncEventDamage(t, tx, "DROP TRIGGER location_events_no_update")
			execSyncEventDamage(t, tx, "UPDATE location_events SET to_parent_id='missing-parent' WHERE id=?", id)
			execSyncEventDamage(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.to_parent_id','missing-parent') WHERE entity_type='location_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"oversized owner identity", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncEventDamage(t, tx, "DROP TRIGGER location_events_no_update")
			execSyncEventDamage(t, tx, "UPDATE location_events SET location_id=? WHERE id=?", strings.Repeat("家", 171), id)
		}, ErrDomainState},
		{"missing retained owner", func(t *testing.T, tx *sql.Tx) {
			execSyncEventDamage(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncEventDamage(t, tx, "DROP TRIGGER location_events_no_update")
			execSyncEventDamage(t, tx, "UPDATE location_events SET location_id='missing-owner' WHERE id=?", id)
			execSyncEventDamage(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.location_id','missing-owner') WHERE entity_type='location_event' AND entity_id=?", id)
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
			count, err := verifySyncLocationEventCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || count != 0 {
				t.Fatal("damaged event returned coverage", count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func execSyncEventDamage(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestSyncLocationEventCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	owner := createLocation(t, locations, token, "EVENT-COVERAGE-BOUNDARY", nil)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncLocationEventCoverage(t.Context(), tx)
		if err != nil || count != 0 {
			t.Fatal("empty event coverage failed", count, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncLocationCoverageEvents(t, s, owner.ID, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncLocationEventCoverage(t.Context(), tx)
		if err != nil || count != 100 {
			t.Fatal("exact-boundary event coverage failed", count, err)
		}
	})
}

func TestSyncLocationEventCoverageAcceptsMaximumWidthSourceFields(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	owner := createLocation(t, locations, token, "EVENT-MAX-OWNER", nil)
	parent := strings.Repeat("😀", 128)
	seedSyncCoverageLocations(t, s, []string{parent})
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	execSyncEventDamage(t, tx, "INSERT INTO location_events(id,location_id,from_parent_id,occurred_at) VALUES('max-event',?,?,?)", owner.ID, parent, strings.Repeat("😀", 10000))
	execSyncEventDamage(t, tx, "INSERT INTO projection_revisions VALUES('location_event','max-event',1)")
	execSyncEventDamage(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'location_event',id,1,json_object('id',id,'location_id',location_id,'from_parent_id',from_parent_id,'to_parent_id',to_parent_id,'occurred_at',occurred_at),'2026'
 FROM location_events WHERE id='max-event'`)
	if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
			t.Error("restore query-only mode:", err)
		}
	}()
	count, err := verifySyncLocationEventCoverage(t.Context(), tx)
	if err != nil || count != 1 {
		t.Fatal("maximum-width event source fields were rejected", count, err)
	}
}

func TestSyncLocationEventCoverageWithholdsOversizedCurrent(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	parent := createLocation(t, locations, token, "EVENT-COVERAGE-LARGE-PARENT", nil)
	owner := createLocation(t, locations, token, "EVENT-COVERAGE-LARGE-OWNER", nil)
	if _, err := locations.Move(t.Context(), token, "event-large-move", owner.ID, owner.Revision, &parent.ID); err != nil {
		t.Fatal(err)
	}
	id := scalar[string](t, s.db, "SELECT id FROM location_events WHERE location_id=?", owner.ID)
	before := domainState(t, s)
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncEventDamage(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type='location_event' AND entity_id=?", oversized, id)
		count, err := verifySyncLocationEventCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncPayloadTooLarge) || count != 0 {
			t.Fatal("an oversized event projection returned coverage", count, err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestSyncLocationEventCoveragePagesExactIDsAndWithholdsLateMissingRow(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	owner := createLocation(t, locations, token, "EVENT-COVERAGE-BULK", nil)
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("event-%03d", index))
	}
	ids = append(ids, "event-098\x00-家庭", "event-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncLocationCoverageEvents(t, s, owner.ID, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "location_event", "", false)
		if err != nil || len(first) != 100 || first[99] != "event-098\x00-家庭" {
			t.Fatal("the first event page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncCoverageIDs(t.Context(), tx, "location_event", first[99], true)
		if err != nil || len(second) != 100 || second[99] != "event-197\x00-第二" {
			t.Fatal("the second event page changed its embedded-NUL boundary", second, err)
		}
		count, err := verifySyncLocationEventCoverage(t.Context(), tx)
		if err != nil || count != int64(len(ids)) {
			t.Fatal("multi-page events changed exact identities", count, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncEventDamage(t, tx, "DELETE FROM current_projections WHERE entity_type='location_event' AND entity_id='zz-uncovered'")
		count, err := verifySyncLocationEventCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncLocationEventUncovered) || count != 0 {
			t.Fatal("a late missing event returned partial coverage", count, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncLocationCoverageEvents(t *testing.T, s *Store, owner string, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		execSyncEventDamage(t, tx, "INSERT INTO location_events(id,location_id,occurred_at) VALUES(?,?,'legacy')", id, owner)
		execSyncEventDamage(t, tx, "INSERT INTO projection_revisions VALUES('location_event',?,1)", id)
		execSyncEventDamage(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'location_event',id,1,json_object('id',id,'location_id',location_id,'from_parent_id',from_parent_id,'to_parent_id',to_parent_id,'occurred_at',occurred_at),'2026'
 FROM location_events WHERE id=?`, id)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncLocationEventCoverageRejectsInvalidIdentitiesAndTransactions(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("bytes-%d", len(id)), func(t *testing.T) {
			s, locations, _, token := locationFixture(t)
			owner := createLocation(t, locations, token, "EVENT-BAD-ID", nil)
			seedLocationMovement(t, s, id, owner.ID, nil, nil, "legacy", false)
			withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
				count, err := verifySyncLocationEventCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || count != 0 {
					t.Fatal("an invalid event identity was skipped", count, err)
				}
			})
		})
	}
	s, _, _, _ := locationFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := readSyncCoverageIDs(t.Context(), tx, "location_events;DROP TABLE locations", "", false); !errors.Is(err, ErrDomainState) || ids != nil {
		t.Fatal("an unsupported coverage source was queried", ids, err)
	}
	if result, err := verifySyncLocationEventCoverage(nil, tx); !errors.Is(err, ErrDomainState) || result != 0 {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncLocationEventCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || result != 0 {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncLocationEventCoverage(ctx, tx); !errors.Is(err, context.Canceled) || result != 0 {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncLocationEventCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || result != 0 {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}
