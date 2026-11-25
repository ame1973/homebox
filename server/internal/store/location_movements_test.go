package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func seedLocationMovement(t *testing.T, s *Store, id, locationID string, from, to *string, occurred string, counter bool) {
	t.Helper()
	execSQL(t, s.db, `INSERT INTO location_events(id,location_id,from_parent_id,to_parent_id,occurred_at) VALUES(?,?,?,?,?)`, id, locationID, from, to, occurred)
	if counter {
		execSQL(t, s.db, `INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES('location_event',?,1)`, id)
	}
}

func TestLocationMovementsReadsActualSourceAndRetainedDeletedOwner(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	first := createLocation(t, locations, token, "HISTORY-FIRST", nil)
	second := createLocation(t, locations, token, "HISTORY-SECOND", nil)
	subject := createLocation(t, locations, token, "HISTORY-SUBJECT", &first.ID)
	moved, err := locations.Move(t.Context(), token, "history-move-one", subject.ID, subject.Revision, &second.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err = locations.Move(t.Context(), token, "history-move-two", subject.ID, moved.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = locations.Move(t.Context(), token, "history-move-noop", subject.ID, moved.Revision, nil); err != nil {
		t.Fatal(err)
	}
	before := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes")
	one, err := locations.PageMovements(t.Context(), token, subject.ID, "", 1)
	if err != nil || one.LocationID != subject.ID || one.Deleted || one.Revision != moved.Revision || len(one.Data) != 1 || one.NextCursor == nil {
		t.Fatal("first actual movement page failed", one, err)
	}
	two, err := locations.PageMovements(t.Context(), token, subject.ID, *one.NextCursor, 1)
	if err != nil || len(two.Data) != 1 || two.NextCursor != nil || one.Data[0].ID >= two.Data[0].ID {
		t.Fatal("movement IDs did not advance", two, err)
	}
	seen := map[string]bool{}
	for _, event := range append(one.Data, two.Data...) {
		if event.LocationID != subject.ID || event.OccurredAt == "" || seen[event.ID] {
			t.Fatal("history changed or duplicated source", event)
		}
		seen[event.ID] = true
		if event.FromParentID != nil && *event.FromParentID == first.ID && event.ToParentID != nil && *event.ToParentID == second.ID {
			seen["first-move"] = true
		}
		if event.FromParentID != nil && *event.FromParentID == second.ID && event.ToParentID == nil {
			seen["root-move"] = true
		}
	}
	if !seen["first-move"] || !seen["root-move"] || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != before {
		t.Fatal("history invented or published movement data", seen)
	}
	body, err := json.Marshal(one)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &fields) != nil || len(fields) != 4 || fields["revision"] != nil || fields["next_cursor"] == nil || strings.Contains(string(body), token) {
		t.Fatal("history leaked credentials or omitted fields", string(body), err)
	}
	ack, err := locations.Delete(t.Context(), token, "history-delete-subject", subject.ID, moved.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := locations.PageMovements(t.Context(), token, subject.ID, *one.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(result, LocationMovements{}) {
		t.Fatal("old cursor crossed a deleted owner revision", result, err)
	}
	retained, err := locations.PageMovements(t.Context(), token, subject.ID, "", 100)
	if err != nil || !retained.Deleted || retained.Revision != ack.Revision || len(retained.Data) != 2 || retained.NextCursor != nil {
		t.Fatal("logical deletion discarded actual retained history", retained, err)
	}
	if result, err := locations.PageMovements(t.Context(), token, "missing-location", "", 100); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(result, LocationMovements{}) {
		t.Fatal("a missing owner became an empty history", result, err)
	}
}

func TestLocationMovementsPagesExactLegacyTextAndIDs(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "HISTORY-BULK", nil)
	ids := []string{strings.Repeat("\x00", 128), strings.Repeat("\x01", 128)}
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("event-%03d", index))
	}
	for _, id := range ids {
		seedLocationMovement(t, s, id, location.ID, nil, nil, "legacy\x00‮exact", true)
	}
	sort.Strings(ids)
	var actual []string
	cursor, pages := "", 0
	for {
		page, err := locations.PageMovements(t.Context(), token, location.ID, cursor, 100)
		if err != nil || page.Data == nil || len(page.Data) > 100 || page.Revision != location.Revision {
			t.Fatal("retained history page failed", page, err)
		}
		pages++
		for _, event := range page.Data {
			if event.OccurredAt != "legacy\x00‮exact" || event.FromParentID != nil || event.ToParentID != nil {
				t.Fatal("retained source text or nullable parent changed", event)
			}
			actual = append(actual, event.ID)
		}
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == cursor || len(*page.NextCursor) > 2048 || pages > 3 {
			t.Fatal("cursor did not advance within its byte limit")
		}
		cursor = *page.NextCursor
	}
	if pages != 3 || !reflect.DeepEqual(actual, ids) {
		t.Fatal("large retained history was incomplete or reordered", pages, len(actual))
	}
	first, err := locations.PageMovements(t.Context(), token, location.ID, "", 1)
	if err != nil || len(first.Data) != 1 || first.Data[0].ID != ids[0] || first.NextCursor == nil || len(*first.NextCursor) > 2048 {
		t.Fatal("maximum control-character identity could not produce a bounded cursor", first, err)
	}
	second, err := locations.PageMovements(t.Context(), token, location.ID, *first.NextCursor, 1)
	if err != nil || len(second.Data) != 1 || second.Data[0].ID != ids[1] {
		t.Fatal("maximum control-character cursor did not preserve the next identity", second, err)
	}
}

func TestLocationMovementsRejectsCorruptLookaheadAndOwner(t *testing.T) {
	for _, state := range []string{"missing counter", "invalid time", "oversized time", "invalid unicode", "missing owner counter", "missing historical parent"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s, locations, _, token := locationFixture(t)
			location := createLocation(t, locations, token, "HISTORY-INVALID", nil)
			seedLocationMovement(t, s, "event-a", location.ID, nil, nil, "2026-01-01T00:00:00Z", true)
			seedLocationMovement(t, s, "event-b", location.ID, nil, nil, "legacy", state != "missing counter")
			switch state {
			case "invalid time":
				execSQL(t, s.db, `DROP TRIGGER location_events_no_update`)
				execSQL(t, s.db, `UPDATE location_events SET occurred_at='' WHERE id='event-b'`)
			case "oversized time":
				execSQL(t, s.db, `DROP TRIGGER location_events_no_update`)
				execSQL(t, s.db, `UPDATE location_events SET occurred_at=? WHERE id='event-b'`, strings.Repeat("x", 10001))
			case "invalid unicode":
				execSQL(t, s.db, `DROP TRIGGER location_events_no_update`)
				execSQL(t, s.db, `UPDATE location_events SET occurred_at=CAST(X'ff' AS TEXT) WHERE id='event-b'`)
			case "missing historical parent":
				execSQL(t, s.db, `PRAGMA foreign_keys=OFF`)
				execSQL(t, s.db, `DROP TRIGGER location_events_no_update`)
				execSQL(t, s.db, `UPDATE location_events SET from_parent_id='missing-parent' WHERE id='event-b'`)
				execSQL(t, s.db, `PRAGMA foreign_keys=ON`)
			case "missing owner counter":
				execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='location' AND entity_id=?`, location.ID)
				execSQL(t, s.db, `DELETE FROM projection_revisions WHERE entity_type='location' AND entity_id=?`, location.ID)
			}
			result, err := locations.PageMovements(t.Context(), token, location.ID, "", 1)
			if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(result, LocationMovements{}) {
				t.Fatal("corrupt owner or lookahead yielded a history prefix", result, err)
			}
		})
	}
}

func TestLocationMovementsCursorScopeAndCompletedSession(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	location := createLocation(t, locations, token, "HISTORY-SCOPED", nil)
	other := createLocation(t, locations, token, "HISTORY-OTHER", nil)
	seedLocationMovement(t, s, "event-a", location.ID, nil, nil, "legacy", true)
	seedLocationMovement(t, s, "event-b", location.ID, nil, nil, "legacy", true)
	first, err := locations.PageMovements(t.Context(), token, location.ID, "", 1)
	if err != nil || first.NextCursor == nil {
		t.Fatal("first scoped page failed", first, err)
	}
	for _, test := range []struct {
		id, cursor string
		limit      int
	}{
		{other.ID, *first.NextCursor, 1}, {location.ID, *first.NextCursor, 2}, {location.ID, *first.NextCursor + "x", 1},
	} {
		if page, err := locations.PageMovements(t.Context(), token, test.id, test.cursor, test.limit); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, LocationMovements{}) {
			t.Fatal("cross-scope or tampered cursor returned data", page, err)
		}
	}
	if _, err = auth.Logout(t.Context(), token, "history-logout"); err != nil {
		t.Fatal(err)
	}
	if _, err = locations.PageMovements(t.Context(), token, location.ID, *first.NextCursor, 1); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a cursor authenticated a revoked session", err)
	}
	_, fresh := login(t, auth)
	if _, err = locations.PageMovements(t.Context(), fresh, location.ID, *first.NextCursor, 1); err != nil {
		t.Fatal("fresh same-principal session could not continue", err)
	}
	expired := auth.now().Add(metadataCursorLifetime)
	auth.now = func() time.Time { return expired }
	if page, err := locations.PageMovements(t.Context(), fresh, location.ID, *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, LocationMovements{}) {
		t.Fatal("expired history cursor returned data", page, err)
	}
	for _, input := range []struct {
		id, cursor string
		limit      int
	}{
		{"", "", 1}, {location.ID, strings.Repeat("x", 2049), 1}, {location.ID, "", 0}, {location.ID, "", 101},
	} {
		if _, err := locations.PageMovements(t.Context(), fresh, input.id, input.cursor, input.limit); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid bounded input was accepted", err)
		}
	}
	if _, err := locations.PageMovements(nil, fresh, location.ID, "", 1); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil context was accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := locations.PageMovements(ctx, fresh, location.ID, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read was accepted", err)
	}
}

func TestLocationMovementsReadsActualSourceAfterSessionActivity(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "HISTORY-ACTIVITY", nil)
	seedLocationMovement(t, s, "event-a", location.ID, nil, nil, "original", true)
	execSQL(t, s.db, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('location_event','event-a',1,'{"untrusted":"projection"}','legacy')`)
	trigger := fmt.Sprintf(`CREATE TRIGGER history_activity AFTER UPDATE OF last_seen_at ON sessions BEGIN
 INSERT INTO location_events(id,location_id,occurred_at) VALUES('event-b','%s','post-activity');
 INSERT INTO projection_revisions VALUES('location_event','event-b',1); END`, location.ID)
	execSQL(t, s.db, trigger)
	defer execSQL(t, s.db, "DROP TRIGGER history_activity")
	page, err := locations.PageMovements(t.Context(), token, location.ID, "", 10)
	if err != nil || len(page.Data) != 2 || page.Data[0].OccurredAt != "original" || page.Data[1].OccurredAt != "post-activity" {
		t.Fatal("history copied an untrusted projection or read before session activity", page, err)
	}
}

func TestLocationMovementsRechecksActivityAndCommitBeforeReturning(t *testing.T) {
	for _, test := range []struct {
		name, setup, trigger string
		want                 error
	}{
		{"ignored activity", "", `BEFORE UPDATE OF last_seen_at ON sessions BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"revocation", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, ErrAuthentication},
		{"password gate", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE admin_accounts SET must_change_password=1; END`, ErrPasswordChangeRequired},
		{"epoch", "", `AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sync_state SET dataset_epoch='other'; END`, ErrAuthentication},
		{"deferred commit", `CREATE TABLE history_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED)`, `AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO history_guard VALUES('missing'); END`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, locations, _, token := locationFixture(t)
			location := createLocation(t, locations, token, "HISTORY-SESSION", nil)
			seedLocationMovement(t, s, "event-a", location.ID, nil, nil, "legacy", true)
			if test.setup != "" {
				execSQL(t, s.db, test.setup)
			}
			execSQL(t, s.db, "CREATE TRIGGER history_late "+test.trigger)
			defer execSQL(t, s.db, "DROP TRIGGER history_late")
			before := domainState(t, s)
			page, err := locations.PageMovements(t.Context(), token, location.ID, "", 10)
			if err == nil || test.want != nil && !errors.Is(err, test.want) || !reflect.DeepEqual(page, LocationMovements{}) {
				t.Fatal("late auth or commit failure returned a history page", page, err)
			}
			requireDomainState(t, s, before)
		})
	}
}
