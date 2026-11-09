package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Capture complete rows, not just counts: a rollback must also preserve names,
// revisions, frozen bytes, media ownership, session activity, and sequence state.
func domainState(t *testing.T, s *Store) map[string]string {
	t.Helper()
	state := make(map[string]string)
	for _, table := range []string{"locations", "location_events", "categories", "tags", "items", "item_units", "stock_balances", "item_tags", "item_aliases", "item_attributes", "item_barcodes", "purchases", "purchase_lines", "asset_profiles", "asset_usage_events", "asset_cost_events", "asset_disposals", "inventory_events", "media", "media_links", "ai_jobs", "ai_review_entries", "ocr_results", "search_documents", "unit_search_documents", "projection_revisions", "current_projections", "sync_changes", "data_revision", "api_idempotency", "mutation_events", "sessions", "admin_accounts", "sync_state", "sqlite_sequence"} {
		rows, err := s.db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1,2")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var contents [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err = rows.Scan(destinations...); err != nil {
				t.Fatal(err)
			}
			contents = append(contents, values)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(contents)
		if err != nil {
			t.Fatal(err)
		}
		state[table] = string(encoded)
	}
	return state
}

func requireDomainState(t *testing.T, s *Store, before map[string]string) {
	t.Helper()
	for table, current := range domainState(t, s) {
		if current != before[table] {
			t.Fatal("failed mutation leaked changes into", table)
		}
	}
}

func TestDomainMutationRollsBackEveryDependentCreateWrite(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"locations", "INSERT"}, {"projection_revisions", "INSERT"}, {"current_projections", "INSERT"},
		{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"},
		{"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table, func(t *testing.T) {
			s, locations, a, token := locationFixture(t)
			before := domainState(t, s)
			now := a.now().Add(time.Minute)
			a.now = func() time.Time { return now }
			execSQL(t, s.db, "CREATE TRIGGER fail_domain_write BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'write failure'); END")
			input := LocationCreate{Code: "BOX", Name: "Never partially committed"}
			if result, err := locations.Create(t.Context(), token, "rollback-create-key", 0, input); err == nil || result.ID != "" {
				t.Fatal("failed dependent write returned success")
			}
			requireDomainState(t, s, before)
			execSQL(t, s.db, "DROP TRIGGER fail_domain_write")
			if _, err := locations.Create(t.Context(), token, "rollback-create-key", 0, input); err != nil {
				t.Fatal("rolled-back key or connection could not be explicitly reused", err)
			}
		})
	}
}

func TestDomainMutationDeferredCommitFailureWithholdsEveryEffectAndResult(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	before := domainState(t, s)
	execSQL(t, s.db, "CREATE TABLE domain_commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	execSQL(t, s.db, "CREATE TRIGGER fail_domain_commit AFTER INSERT ON mutation_events BEGIN INSERT INTO domain_commit_guard VALUES('missing'); END")
	if result, err := locations.Create(t.Context(), token, "failed-commit-key", 0, LocationCreate{Code: "BOX", Name: "Uncommitted"}); err == nil || result.ID != "" {
		t.Fatal("deferred COMMIT failure returned a resource")
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM domain_commit_guard") != 0 {
		t.Fatal("COMMIT failure retained a deferred guard row")
	}
	execSQL(t, s.db, "DROP TRIGGER fail_domain_commit")
	if _, err := locations.Create(t.Context(), token, "failed-commit-key", 0, LocationCreate{Code: "BOX", Name: "Uncommitted"}); err != nil {
		t.Fatal("pooled connection was not reusable after failed COMMIT", err)
	}
}

func TestDomainMutationUsesOneRevisionAndFinalStatePerIdentity(t *testing.T) {
	s, locations, a, token := locationFixture(t)
	location := createLocation(t, locations, token, "BOX", nil)
	result, err := runDomainMutation(t.Context(), a, token, "staged-final-state", "test_final_state", struct{}{}, func(m *mutationTx) (Location, error) {
		for _, name := range []string{"intermediate state", "final state"} {
			if _, err := m.tx.ExecContext(m.ctx, "UPDATE locations SET name=? WHERE id=?", name, location.ID); err != nil {
				return Location{}, err
			}
			if _, err := m.touch("location", location.ID, false); err != nil {
				return Location{}, err
			}
		}
		return loadLocation(m.ctx, m.tx, location.ID)
	})
	if err != nil || result.Revision != location.Revision+1 || result.Name != "final state" {
		t.Fatal("same-transaction changes were not coalesced", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_id=?", location.ID) != 2 || scalar[int](t, s.db, "SELECT revision FROM data_revision WHERE scope='catalog'") != 2 {
		t.Fatal("one identity received multiple revisions in one transaction")
	}
	var payload LocationData
	latest := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes ORDER BY seq DESC LIMIT 1")
	if json.Unmarshal([]byte(latest), &payload) != nil || payload.Name != "final state" || strings.Contains(latest, "intermediate state") {
		t.Fatal("outbox captured an intermediate source state")
	}
}

func TestDomainMutationPayloadFailuresRollBackSourceAndCounters(t *testing.T) {
	for _, state := range []string{"missing source", "invalid source", "unsupported upsert", "unknown kind", "invalid identity", "invalid result"} {
		t.Run(state, func(t *testing.T) {
			s, locations, a, token := locationFixture(t)
			location := createLocation(t, locations, token, "BOX", nil)
			before := domainState(t, s)
			if result, err := runDomainMutation(t.Context(), a, token, "bad-payload-key", "test_bad_payload", state, func(m *mutationTx) (Location, error) {
				if _, err := m.tx.ExecContext(m.ctx, "UPDATE locations SET name='uncommitted' WHERE id=?", location.ID); err != nil {
					return Location{}, err
				}
				switch state {
				case "missing source":
					_, err := m.touch("location", "missing", false)
					if err != nil {
						return Location{}, err
					}
				case "invalid source":
					if _, err := m.touch("location", location.ID, false); err != nil {
						return Location{}, err
					}
					if _, err := m.tx.ExecContext(m.ctx, "UPDATE locations SET name='' WHERE id=?", location.ID); err != nil {
						return Location{}, err
					}
				case "unsupported upsert":
					if _, err := m.touch("media_link", "unimplemented-builder", false); err != nil {
						return Location{}, err
					}
				case "unknown kind":
					_, err := m.touch("unknown", "id", true)
					return Location{}, err
				case "invalid identity":
					_, err := m.touch("location", "", false)
					return Location{}, err
				case "invalid result":
					return Location{}, nil
				}
				return location, nil
			}); err == nil || result.ID != "" {
				t.Fatal("invalid publication returned success")
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestDomainMutationRechecksAuthorizationBeforeCommit(t *testing.T) {
	for _, state := range []string{"expiry", "revoked", "epoch", "mandatory", "revision"} {
		t.Run(state, func(t *testing.T) {
			s, a, now, _, token := completedAuthFixture(t)
			locations, err := s.NewLocations(a)
			if err != nil {
				t.Fatal(err)
			}
			location := createLocation(t, locations, token, "BOX", nil)
			before := domainState(t, s)
			result, err := runDomainMutation(t.Context(), a, token, "recheck-session-key", "test_session_recheck", state, func(m *mutationTx) (Location, error) {
				if _, err := m.tx.ExecContext(m.ctx, "UPDATE locations SET name='uncommitted' WHERE id=?", location.ID); err != nil {
					return Location{}, err
				}
				if _, err := m.touch("location", location.ID, false); err != nil {
					return Location{}, err
				}
				switch state {
				case "expiry":
					*now = now.Add(a.idle)
				case "revoked":
					_, err = m.tx.ExecContext(m.ctx, "UPDATE sessions SET revoked_at=?", stamp(*now))
				case "epoch":
					_, err = m.tx.ExecContext(m.ctx, "UPDATE sync_state SET dataset_epoch='changed-in-transaction'")
				case "mandatory":
					_, err = m.tx.ExecContext(m.ctx, "UPDATE admin_accounts SET must_change_password=1")
				case "revision":
					_, err = m.tx.ExecContext(m.ctx, "UPDATE admin_accounts SET revision=revision+1")
				}
				if err != nil {
					return Location{}, err
				}
				return loadLocation(m.ctx, m.tx, location.ID)
			})
			if err == nil || result.ID != "" {
				t.Fatal("authorization changed before COMMIT but mutation succeeded")
			}
			requireDomainState(t, s, before)
			if state == "epoch" && scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") == "changed-in-transaction" {
				t.Fatal("failed authorization retained an epoch mutation")
			}
		})
	}
}

func TestDomainMutationDeleteFailureRestoresOwnerLinksAndCurrentProjection(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"media_links", "DELETE"}, {"locations", "UPDATE"}, {"projection_revisions", "UPDATE"},
		{"current_projections", "DELETE"}, {"sync_changes", "INSERT"}, {"mutation_events", "INSERT"},
	} {
		t.Run(failure.table, func(t *testing.T) {
			s, locations, _, token := locationFixture(t)
			location := createLocation(t, locations, token, "BOX", nil)
			execSQL(t, s.db, "INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','private-photo',?,'image/jpeg',1,1,10,'2026')", strings.Repeat("a", 64))
			seedMediaProjection(t, s, "photo")
			execSQL(t, s.db, "INSERT INTO media_links(id,media_id,location_id) VALUES('link','photo',?)", location.ID)
			seedMediaLinkProjection(t, s, "link")
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_domain_delete BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'write failure'); END")
			if ack, err := locations.Delete(t.Context(), token, "failed-delete-key", location.ID, location.Revision); err == nil || ack.OK || !strings.Contains(err.Error(), "write failure") {
				t.Fatal("targeted failed deletion returned an acknowledgement or missed its injected statement", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestDomainMutationMovementFailureRollsBackTheWholeGraph(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	parent := createLocation(t, locations, token, "PARENT", nil)
	child := createLocation(t, locations, token, "CHILD", &parent.ID)
	before := domainState(t, s)
	execSQL(t, s.db, "CREATE TRIGGER fail_movement BEFORE INSERT ON location_events BEGIN SELECT RAISE(ABORT,'movement failure'); END")
	if _, err := locations.Move(t.Context(), token, "failed-movement-key", child.ID, child.Revision, nil); err == nil {
		t.Fatal("movement event failure returned success")
	}
	requireDomainState(t, s, before)
}

func TestDomainMutationSubtreeArchiveRollsBackEarlierNodeWrites(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	parent := createLocation(t, locations, token, "PARENT", nil)
	child := createLocation(t, locations, token, "CHILD", &parent.ID)
	parent = getLocation(t, locations, token, parent.ID)
	lastID := parent.ID
	if child.ID > lastID {
		lastID = child.ID
	}
	before := domainState(t, s)
	// The service's stable ID ordering ensures another node is updated before
	// this failure. The final state must still be byte-for-byte unchanged.
	execSQL(t, s.db, fmt.Sprintf("CREATE TRIGGER fail_later_archive BEFORE UPDATE ON locations WHEN new.id='%s' BEGIN SELECT RAISE(ABORT,'later node failure'); END", lastID))
	if _, err := locations.Archive(t.Context(), token, "failed-subtree-key", parent.ID, parent.Revision, true, "not committed"); err == nil {
		t.Fatal("partial subtree archive returned success")
	}
	requireDomainState(t, s, before)
}

func TestDomainMutationSafeIntegerLimitsRollBackAllWrites(t *testing.T) {
	for _, counter := range []string{"projection", "catalog", "sequence"} {
		t.Run(counter, func(t *testing.T) {
			s, locations, _, token := locationFixture(t)
			location := createLocation(t, locations, token, "BOX", nil)
			switch counter {
			case "projection":
				execSQL(t, s.db, fmt.Sprintf("BEGIN IMMEDIATE; UPDATE projection_revisions SET revision=%d; UPDATE current_projections SET entity_revision=%d; COMMIT", maxSafeInteger, maxSafeInteger))
				location.Revision = maxSafeInteger
			case "catalog":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			}
			before := domainState(t, s)
			if result, err := locations.Patch(t.Context(), token, "overflow-patch-key", location.ID, location.Revision, LocationPatch{Name: testPointer("Uncommitted")}); !errors.Is(err, ErrRevisionOverflow) || result.ID != "" {
				t.Fatal("unsafe integer revision or sequence was published", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestDomainCurrentProjectionSurvivesPruningAndOldChangesStayFrozen(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "BOX", nil)
	first := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes")
	updated, err := locations.Patch(t.Context(), token, "update-before-prune", location.ID, location.Revision, LocationPatch{Name: testPointer("Current name")})
	if err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=1") != first {
		t.Fatal("a later write reconstructed an earlier delta from live joins")
	}
	before := scalar[string](t, s.db, "SELECT payload_json FROM current_projections")
	execSQL(t, s.db, "DELETE FROM sync_changes; UPDATE sync_state SET min_retained_seq=2")
	if scalar[string](t, s.db, "SELECT payload_json FROM current_projections") != before || getLocation(t, locations, token, location.ID).Name != updated.Name {
		t.Fatal("current materialization depended on retained change-log rows")
	}
	if _, err = locations.Patch(t.Context(), token, "update-after-prune", location.ID, updated.Revision, LocationPatch{Description: testPointer("After retention")}); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.db, "SELECT seq FROM sync_changes") != 3 {
		t.Fatal("pruning reused a committed change sequence")
	}
}

func TestDomainIdempotencyBindsTargetAndConcurrencyPreconditions(t *testing.T) {
	_, locations, _, token := locationFixture(t)
	first := createLocation(t, locations, token, "FIRST", nil)
	second := createLocation(t, locations, token, "SECOND", nil)
	patch := LocationPatch{Name: testPointer("Changed")}
	updated, err := locations.Patch(t.Context(), token, "bound-precondition-key", first.ID, first.Revision, patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		id       string
		revision int64
	}{{first.ID, updated.Revision}, {second.ID, second.Revision}} {
		if _, err = locations.Patch(t.Context(), token, "bound-precondition-key", input.id, input.revision, patch); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatal("a changed target/precondition reused an old key", err)
		}
	}
	replayed, err := locations.Patch(t.Context(), token, "bound-precondition-key", first.ID, first.Revision, patch)
	if err != nil || !reflect.DeepEqual(updated, replayed) {
		t.Fatal("an exact retry was rejected by the now-stale precondition", err)
	}
}

func TestDomainConcurrentChildrenCannotBothUseAStaleParentProof(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	parent := createLocation(t, locations, token, "PARENT", nil)
	results := make(chan error, 2)
	var group sync.WaitGroup
	for i := range 2 {
		group.Go(func() {
			_, err := locations.Create(t.Context(), token, fmt.Sprintf("child-key-%d", i), parent.Revision, LocationCreate{ParentID: &parent.ID, Code: fmt.Sprintf("CHILD-%d", i), Name: "Child"})
			results <- err
		})
	}
	group.Wait()
	close(results)
	committed := 0
	for err := range results {
		if err == nil {
			committed++
		} else if !errors.Is(err, ErrRevisionConflict) {
			t.Fatal(err)
		}
	}
	if committed != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM locations") != 2 || getLocation(t, locations, token, parent.ID).Revision != 2 {
		t.Fatal("concurrent child creations did not enforce parent concurrency")
	}
}

func TestDomainFrozenRepliesRejectCorruptedRowsRatherThanApplyingAgain(t *testing.T) {
	for _, change := range []string{
		"response_json='null'", "response_json='not json'", "response_json='{}'", "response_status=201",
		"response_status=NULL", "response_json=NULL", "created_at='invalid'", "expires_at='invalid'",
		"created_at='2099-01-01T00:00:00Z'", "expires_at=created_at",
	} {
		t.Run(change, func(t *testing.T) {
			s, locations, _, token := locationFixture(t)
			input := LocationCreate{Code: "BOX", Name: "Original"}
			if _, err := locations.Create(t.Context(), token, "corrupt-reply-key", 0, input); err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, "UPDATE api_idempotency SET "+change)
			before := domainState(t, s)
			if result, err := locations.Create(t.Context(), token, "corrupt-reply-key", 0, input); !errors.Is(err, ErrDomainState) || result.ID != "" {
				t.Fatal("corrupt reply returned success or applied a new mutation", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestDomainReadAndReplaySessionTouchFailuresDoNotReturnData(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "BOX", nil)
	before := domainState(t, s)
	execSQL(t, s.db, "CREATE TRIGGER fail_domain_touch BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'activity failure'); END")
	if result, err := locations.Get(t.Context(), token, location.ID); err == nil || result.ID != "" {
		t.Fatal("read returned data after its activity transaction failed")
	}
	if result, err := locations.Create(t.Context(), token, "create-BOX", 0, LocationCreate{Code: "BOX", Name: "收納 BOX"}); err == nil || result.ID != "" {
		t.Fatal("replay returned data after its activity transaction failed")
	}
	requireDomainState(t, s, before)
}

func TestDomainKeyEncodingSeparatesIdentityAndEpochBoundaries(t *testing.T) {
	if domainReplyKey("a\x00b", "c", "key-1234") == domainReplyKey("a", "b\x00c", "key-1234") ||
		domainReplyKey("admin", "epoch", "key-1234") == authReplyKey(AuthSession{ID: "admin"}, "key-1234") {
		t.Fatal("domain key concatenation or auth namespace collision")
	}
}

func TestDomainProjectionCountersAndFrozenRowsHaveDatabaseGuards(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "BOX", nil)
	before := domainState(t, s)
	if _, err := s.db.ExecContext(t.Context(), "UPDATE sync_changes SET projection_json='{}'"); err == nil {
		t.Fatal("frozen change payload accepted an update")
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE current_projections SET entity_revision=2"); err == nil {
		t.Fatal("current projection revision diverged from its authoritative counter")
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), "UPDATE projection_revisions SET revision=revision+1 WHERE entity_id=?", location.ID); err != nil {
		t.Fatal("deferred constraint rejected an intermediate transaction state", err)
	}
	if err = tx.Commit(); err == nil {
		t.Fatal("a counter without its matching current projection committed")
	}
	requireDomainState(t, s, before)
}

func TestDomainCurrentProjectionBackfillPreservesLegacyRowsAndRevisions(t *testing.T) {
	path, old := legacyFixture(t, 2)
	execSQL(t, old, "INSERT INTO projection_revisions VALUES('location','loc-1',7)")
	execSQL(t, old, "INSERT INTO location_events(id,location_id,occurred_at) VALUES('legacy-move','loc-1','2026-01-01T00:00:00Z')")
	_ = old.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Count this migration's location identities, not unrelated domains added by
	// later backfills; their source/revision preservation has dedicated coverage.
	if scalar[int](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type='location'") != 7 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type IN('location','location_event')") != 2 ||
		scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances") != 1500 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 0 {
		t.Fatal("backfill rescaled inventory, lost revisions, or pretended to publish sync")
	}
	var payload LocationData
	encoded := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location'")
	if json.Unmarshal([]byte(encoded), &payload) != nil || payload.ID != "loc-1" || payload.Description != "" || payload.Code != "BOX-1" {
		t.Fatal("legacy location did not materialize the exact new payload")
	}
	if _, err = s.db.ExecContext(t.Context(), "DELETE FROM location_events"); err == nil {
		t.Fatal("legacy movement history was not made immutable")
	}
}

// Verify that a read COMMIT error also discards data rather than returning a
// successful-looking DTO alongside an error that a caller might overlook.
func TestDomainReadDeferredCommitFailureDiscardsResult(t *testing.T) {
	s, locations, a, token := locationFixture(t)
	location := createLocation(t, locations, token, "BOX", nil)
	execSQL(t, s.db, "CREATE TABLE read_commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	execSQL(t, s.db, "CREATE TRIGGER fail_read_commit AFTER UPDATE ON sessions BEGIN INSERT INTO read_commit_guard VALUES('missing'); END")
	if result, err := readDomain(t.Context(), a, token, func(tx *sql.Tx) (Location, error) { return loadLocation(t.Context(), tx, location.ID) }); err == nil || result.ID != "" {
		t.Fatal("read returned a DTO before COMMIT succeeded")
	}
}
