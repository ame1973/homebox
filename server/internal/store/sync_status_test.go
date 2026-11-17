package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSyncStatusReportsMaterializedStateWithoutEnablingSync(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if status.ServerInstanceID != scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state") ||
		status.DatasetEpoch != scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") || status.ProjectionVersion != 1 ||
		status.MinRetainedSeq != 0 || status.HighWatermark != 0 || status.LastChangeAt != nil ||
		!reflect.DeepEqual(status.Counts, map[string]int64{}) || status.CountsScope != "materialized_projections" ||
		status.BootstrapReady || status.RetentionEnabled || status.RetentionDays != 30 {
		t.Fatal("empty status is inconsistent or claims unavailable sync", status)
	}
	location := createLocation(t, locations, token, "STATUS", nil)
	updated, err := locations.Patch(t.Context(), token, "status-location-update", location.ID, location.Revision, LocationPatch{Name: testPointer("Renamed")})
	if err != nil {
		t.Fatal(err)
	}
	status, err = auth.ReadSyncStatus(t.Context(), token)
	last := scalar[string](t, s.db, "SELECT created_at FROM sync_changes ORDER BY seq DESC LIMIT 1")
	if err != nil || status.HighWatermark != 2 || status.MinRetainedSeq != 0 || status.LastChangeAt == nil || *status.LastChangeAt != last ||
		!reflect.DeepEqual(status.Counts, map[string]int64{"location": 1}) || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("status did not read the actual materialization and change log", status, err)
	}
	// The floor is the last pruned position, not the first retained one.
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq<=1; UPDATE sync_state SET min_retained_seq=1")
	status, err = auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.MinRetainedSeq != 1 || status.HighWatermark != 2 || status.LastChangeAt == nil || *status.LastChangeAt != last {
		t.Fatal("partial retention changed the real high watermark", status, err)
	}
	execSQL(t, s.db, "DELETE FROM sync_changes; UPDATE sync_state SET min_retained_seq=2")
	status, err = auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.MinRetainedSeq != 2 || status.HighWatermark != 2 || status.LastChangeAt != nil || status.Counts["location"] != 1 {
		t.Fatal("a pruned log lost its durable sequence or current projection", status, err)
	}
	if _, err = locations.Patch(t.Context(), token, "status-after-pruning", updated.ID, updated.Revision, LocationPatch{Name: testPointer("After pruning")}); err != nil {
		t.Fatal(err)
	}
	status, err = auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.MinRetainedSeq != 2 || status.HighWatermark != 3 || status.LastChangeAt == nil || status.Counts["location"] != 1 {
		t.Fatal("a post-pruning append did not use the durable sequence", status, err)
	}
}

func TestSyncStatusCountsEveryOfflineKindWithoutClaimingSourceCompleteness(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	kinds := []string{"location", "item", "unit", "stock", "barcode", "media", "media_link", "category", "tag", "item_tag", "search_doc", "purchase", "purchase_line", "asset", "usage", "cost", "disposal", "alias", "attribute", "location_event", "inventory_event", "ai_review"}
	for _, kind := range kinds {
		id := "id-家\x00"
		switch kind {
		case "stock", "item_tag":
			id = projectionPair("item-家\x00", "owner-😀")
		case "search_doc":
			id = projectionPair("item", "item-家\x00")
		}
		execSQL(t, s.db, "INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)", kind, id)
		execSQL(t, s.db, "INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,1,'{}','2026-01-01T00:00:00Z')", kind, id)
	}
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || len(status.Counts) != len(kinds) || status.BootstrapReady {
		t.Fatal("partial projection counts implied a ready replica", status, err)
	}
	for _, kind := range kinds {
		if status.Counts[kind] != 1 {
			t.Fatal("status omitted a materialized projection kind", kind, status.Counts)
		}
	}
}

func TestSyncStatusUsesAllocatedWatermarkAcrossSequenceGaps(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "GAP", nil)
	execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=10 WHERE name='sync_changes'")
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.HighWatermark != 10 || status.LastChangeAt == nil {
		t.Fatal("allocated gap was mistaken for the last retained row", status, err)
	}
	execSQL(t, s.db, "DELETE FROM sync_changes")
	status, err = auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.HighWatermark != 10 || status.MinRetainedSeq != 0 || status.LastChangeAt != nil {
		t.Fatal("an empty retained log changed the allocated watermark", status, err)
	}
}

func TestSyncStatusRequiresAnActiveCompletedSessionAndRevalidatesActivity(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	zero := SyncStatus{}
	if value, err := auth.ReadSyncStatus(t.Context(), "invalid-token"); !errors.Is(err, ErrAuthentication) || !reflect.DeepEqual(value, zero) {
		t.Fatal("an invalid session read status", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
	if value, err := auth.ReadSyncStatus(t.Context(), token); !errors.Is(err, ErrPasswordChangeRequired) || !reflect.DeepEqual(value, zero) {
		t.Fatal("an incomplete session read status", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=0")
	for _, test := range []struct {
		name, timing, statement string
		want                    error
	}{
		{"ignored activity", "BEFORE", "SELECT RAISE(IGNORE)", ErrDomainState},
		{"revoked", "AFTER", "UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id", ErrAuthentication},
		{"epoch", "AFTER", "UPDATE sync_state SET dataset_epoch='changed-during-sync-status' WHERE singleton=1", ErrAuthentication},
		{"password", "AFTER", "UPDATE admin_accounts SET must_change_password=1", ErrPasswordChangeRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			execSQL(t, s.db, "CREATE TRIGGER sync_status_late "+test.timing+" UPDATE OF last_seen_at ON sessions BEGIN "+test.statement+"; END")
			defer execSQL(t, s.db, "DROP TRIGGER sync_status_late")
			before := domainState(t, s)
			value, err := auth.ReadSyncStatus(t.Context(), token)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(value, zero) {
				t.Fatal("status survived a failed final authorization", value, err)
			}
			requireDomainState(t, s, before)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := auth.ReadSyncStatus(ctx, token); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(value, zero) {
		t.Fatal("canceled status returned data", value, err)
	}
	if value, err := auth.ReadSyncStatus(nil, token); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(value, zero) {
		t.Fatal("nil context was accepted", value, err)
	}
	var absent *Auth
	if value, err := absent.ReadSyncStatus(t.Context(), token); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(value, zero) {
		t.Fatal("nil authentication was accepted", value, err)
	}
}

func TestSyncStatusCancellationWhileWaitingForSQLite(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	value, err := auth.ReadSyncStatus(ctx, token)
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(value, SyncStatus{}) {
		t.Fatal("a canceled waiting read returned diagnostics", value, err)
	}
}

func TestSyncStatusSeesStateAfterSessionActivity(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	execSQL(t, s.db, `CREATE TRIGGER sync_status_late AFTER UPDATE OF last_seen_at ON sessions BEGIN
 INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('location','late','delete',1,NULL,'2026-01-01T00:00:00Z'); END`)
	defer execSQL(t, s.db, "DROP TRIGGER sync_status_late")
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.HighWatermark != 1 || status.LastChangeAt == nil || *status.LastChangeAt != "2026-01-01T00:00:00Z" {
		t.Fatal("status was read before the final activity write", status, err)
	}
}

func TestSyncStatusRejectsInvalidSequenceAndProjectionState(t *testing.T) {
	for _, test := range []struct{ name, statement string }{
		{"missing sequence", "DELETE FROM sqlite_sequence WHERE name='sync_changes'"},
		{"duplicate sequence", "INSERT INTO sqlite_sequence(name,seq) VALUES('sync_changes',1)"},
		{"negative sequence", "UPDATE sqlite_sequence SET seq=-1 WHERE name='sync_changes'"},
		{"noninteger sequence", "UPDATE sqlite_sequence SET seq='broken' WHERE name='sync_changes'"},
		{"unsafe sequence", "UPDATE sqlite_sequence SET seq=9007199254740992 WHERE name='sync_changes'"},
		{"sequence before row", "UPDATE sqlite_sequence SET seq=0 WHERE name='sync_changes'"},
		{"negative floor", "UPDATE sync_state SET min_retained_seq=-1"},
		{"unsafe floor", "UPDATE sync_state SET min_retained_seq=9007199254740992"},
		{"floor after sequence", "UPDATE sync_state SET min_retained_seq=2"},
		{"floor includes retained row", "UPDATE sync_state SET min_retained_seq=1"},
		{"invalid instance", "UPDATE sync_state SET server_instance_id='invalid'"},
		{"negative retained row", `INSERT INTO sync_changes(seq,entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES(-2,'location','retained','delete',1,NULL,'2026-01-01T00:00:00Z')`},
		{"unknown projection kind", `INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES('private','hidden',1);
 INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('private','hidden',1,'{}','2026-01-01T00:00:00Z')`},
		{"invalid retained timestamp", `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('location','retained','delete',1,NULL,'not-an-instant')`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, locations, auth, token := locationFixture(t)
			createLocation(t, locations, token, "BAD", nil)
			execSQL(t, s.db, test.statement)
			before := domainState(t, s)
			value, err := auth.ReadSyncStatus(t.Context(), token)
			if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(value, SyncStatus{}) {
				t.Fatal("inconsistent status state returned a partial result", value, err)
			}
			requireDomainState(t, s, before)
		})
	}
}
