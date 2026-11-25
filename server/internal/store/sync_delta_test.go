package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testSyncDeltaCursor(t *testing.T, s *Store, a *Auth, after int64) string {
	t.Helper()
	scope := syncCursorScope{
		AdminID:           scalar[string](t, s.db, "SELECT id FROM admin_accounts"),
		ServerInstanceID:  scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state"),
		DatasetEpoch:      scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state"),
		ProjectionVersion: 1,
	}
	cursor, err := encodeSyncDeltaCursor(a, syncDeltaCursor{Version: 1, Scope: scope, After: after})
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

func TestSyncDeltaReadsFrozenChangesInOrderWithoutRepairingHistory(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	created := createLocation(t, locations, token, "SYNC-DELTA", nil)
	first := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=1")
	updated, err := locations.Patch(t.Context(), token, "sync-delta-edit", created.ID, created.Revision, LocationPatch{Name: testPointer("Changed source")})
	if err != nil {
		t.Fatal(err)
	}
	second := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=2")
	if _, err = locations.Delete(t.Context(), token, "sync-delta-delete", updated.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	var changes []syncChange
	for index := 0; index < 3; index++ {
		page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
		if err != nil || page.HighWatermark != 3 || len(page.Changes) != 1 || page.HasMore != (index < 2) || page.NextCursor == "" {
			t.Fatal("unexpected frozen change page", index, page, err)
		}
		if page.DatasetEpoch != scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") ||
			page.ServerInstanceID != scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state") || page.ProjectionVersion != 1 {
			t.Fatal("page lost its actual dataset scope", page)
		}
		changes = append(changes, page.Changes[0])
		cursor = page.NextCursor
	}
	for index, change := range changes {
		if change.Seq != int64(index+1) || change.Record.EntityType != "location" || change.Record.EntityID != created.ID ||
			change.Record.EntityRevision != int64(index+1) || change.Record.ProjectionVersion != 1 {
			t.Fatal("wrong frozen identity or revision", change)
		}
	}
	if changes[0].Record.Operation != "upsert" || string(changes[0].Record.Payload) != first ||
		changes[1].Record.Operation != "upsert" || string(changes[1].Record.Payload) != second ||
		changes[2].Record.Operation != "delete" || changes[2].Record.Payload != nil {
		t.Fatal("frozen bytes or tombstone were changed", changes)
	}
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
	if err != nil || page.HighWatermark != 3 || page.HasMore || len(page.Changes) != 0 || page.NextCursor != cursor {
		t.Fatal("empty page advanced or changed its cursor", page, err)
	}
	if scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=1") != first ||
		scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=2") != second {
		t.Fatal("read altered retained history")
	}
}

func TestSyncDeltaEnforcesCursorScopeRetentionAndInput(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-CURSOR", nil)
	limits := syncPageLimits{Records: 10, PayloadBytes: 4096, PageBytes: 8192}
	start := testSyncDeltaCursor(t, s, auth, 0)
	scope := syncCursorScope{
		AdminID: scalar[string](t, s.db, "SELECT id FROM admin_accounts"), ServerInstanceID: scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state"),
		DatasetEpoch: scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state"), ProjectionVersion: 1,
	}
	wrongAdmin := scope
	wrongAdmin.AdminID = "different-admin"
	wrongEpoch := scope
	wrongEpoch.DatasetEpoch = "different-epoch"
	sign := func(value syncCursorScope, after int64) string {
		cursor, err := encodeSyncDeltaCursor(auth, syncDeltaCursor{Version: 1, Scope: value, After: after})
		if err != nil {
			t.Fatal(err)
		}
		return cursor
	}
	for _, test := range []struct {
		name, cursor string
		want         error
	}{
		{"empty", "", ErrInvalidCursor},
		{"tampered", start + "a", ErrInvalidCursor},
		{"wrong administrator", sign(wrongAdmin, 0), ErrInvalidCursor},
		{"wrong epoch", sign(wrongEpoch, 0), ErrSyncResetRequired},
		{"ahead", sign(scope, 2), ErrSyncResetRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			page, err := auth.readSyncDeltaPage(t.Context(), token, test.cursor, limits)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(page, syncDeltaPage{}) {
				t.Fatal("invalid cursor returned a page", page, err)
			}
		})
	}
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq<=1; UPDATE sync_state SET min_retained_seq=1")
	if page, err := auth.readSyncDeltaPage(t.Context(), token, start, limits); !errors.Is(err, ErrSyncResetRequired) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("a cursor below retention survived", page, err)
	}
	atFloor := testSyncDeltaCursor(t, s, auth, 1)
	if page, err := auth.readSyncDeltaPage(t.Context(), token, atFloor, limits); err != nil || len(page.Changes) != 0 || page.NextCursor != atFloor || page.HighWatermark != 1 {
		t.Fatal("the inclusive retention floor was rejected", page, err)
	}
	for _, invalid := range []syncPageLimits{
		{Records: 0, PayloadBytes: 1, PageBytes: 1}, {Records: 501, PayloadBytes: 1, PageBytes: 1},
		{Records: 1, PayloadBytes: 0, PageBytes: 1}, {Records: 1, PayloadBytes: 2, PageBytes: 1},
		{Records: 1, PayloadBytes: 1, PageBytes: maxSyncPageBytes + 1},
	} {
		if page, err := auth.readSyncDeltaPage(t.Context(), token, atFloor, invalid); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncDeltaPage{}) {
			t.Fatal("invalid read limits returned data", invalid, page, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if page, err := auth.readSyncDeltaPage(ctx, token, atFloor, limits); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("canceled read returned data", page, err)
	}
	if page, err := auth.readSyncDeltaPage(nil, token, atFloor, limits); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("nil context returned data", page, err)
	}
	var absent *Auth
	if page, err := absent.readSyncDeltaPage(t.Context(), token, atFloor, limits); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("nil authentication returned data", page, err)
	}
	if page, err := auth.readSyncDeltaPage(t.Context(), "bad-token", atFloor, limits); !errors.Is(err, ErrAuthentication) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("unauthenticated read returned data", page, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
	if page, err := auth.readSyncDeltaPage(t.Context(), token, atFloor, limits); !errors.Is(err, ErrPasswordChangeRequired) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("an incomplete session read changes", page, err)
	}
}

func TestSyncDeltaWithholdsInvalidLookaheadAndUnprovenSearch(t *testing.T) {
	for _, test := range []struct {
		name, kind, identity, operation string
		payload                         any
		want                            error
	}{
		{"unknown kind", "unknown", "hidden", "delete", nil, ErrDomainState},
		{"invalid payload", "location", "hidden", "upsert", `{}`, ErrDomainState},
		{"unreviewed AI value", "ai_review", "hidden", "upsert", `{"id":"hidden","item_id":"item","state":"needs_review","effective_value":"private","field_key":"title","media_id":null}`, ErrDomainState},
		{"unproven historical search", "search_doc", projectionPair("item", "hidden"), "upsert", `{"entity_type":"item","entity_id":"hidden","approved_text":"unreviewed-private-text"}`, errSyncSearchUnproven},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, locations, auth, token := locationFixture(t)
			createLocation(t, locations, token, "SYNC-LOOKAHEAD", nil)
			cursor := testSyncDeltaCursor(t, s, auth, 0)
			execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES(?,?,?,1,?,'2026-01-01T00:00:00Z')`, test.kind, test.identity, test.operation, test.payload)
			before := domainState(t, s)
			page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
			if !errors.Is(err, test.want) || !reflect.DeepEqual(page, syncDeltaPage{}) {
				t.Fatal("invalid or unproven lookahead returned a prefix", page, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestSyncDeltaBoundsPayloadBeforeReturningAnyPrefix(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-SIZE", nil)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	payload := `{"id":"large","parent_id":null,"code":"` + strings.Repeat("x", 1024) + `","name":"x","status":"active","kind":"other","sort_order":0,"description":""}`
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('location','large','upsert',1,?,'2026-01-01T00:00:00Z')`, payload)
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 512, PageBytes: 2048})
	if !errors.Is(err, errSyncPayloadTooLarge) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("oversized lookahead returned a prefix", page, err)
	}
}

func TestSyncDeltaPagesByByteBudgetAndSeesLaterAppends(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	one := createLocation(t, locations, token, "SYNC-PAGE-ONE", nil)
	two := createLocation(t, locations, token, "SYNC-PAGE-TWO", nil)
	first := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=1")
	second := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=2")
	payloadLimit := max(len(first), len(second))
	pageLimit := max(len(first)+len(one.ID), len(second)+len(two.ID)) + len("location") + len("upsert")
	limits := syncPageLimits{Records: 3, PayloadBytes: payloadLimit, PageBytes: pageLimit}
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
	if err != nil || page.HighWatermark != 2 || !page.HasMore || len(page.Changes) != 1 || page.Changes[0].Seq != 1 {
		t.Fatal("byte-bounded page did not retain a continuation", page, err)
	}
	three := createLocation(t, locations, token, "SYNC-PAGE-THREE", nil)
	page, err = auth.readSyncDeltaPage(t.Context(), token, page.NextCursor, syncPageLimits{Records: 3, PayloadBytes: 4096, PageBytes: 8192})
	if err != nil || page.HighWatermark != 3 || page.HasMore || len(page.Changes) != 2 ||
		page.Changes[0].Record.EntityID != two.ID || page.Changes[1].Record.EntityID != three.ID {
		t.Fatal("later committed changes did not follow the original cursor", page, err)
	}
	tooSmall := syncPageLimits{Records: 1, PayloadBytes: len(first), PageBytes: len(first)}
	if value, err := auth.readSyncDeltaPage(t.Context(), token, cursor, tooSmall); !errors.Is(err, errSyncPayloadTooLarge) || !reflect.DeepEqual(value, syncDeltaPage{}) {
		t.Fatal("an unrepresentable first record returned a stuck page", value, err)
	}
}

func TestSyncDeltaKeepsAllocatedWatermarkAndSequenceGaps(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-GAP-ONE", nil)
	two := createLocation(t, locations, token, "SYNC-GAP-TWO", nil)
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq=1; UPDATE sqlite_sequence SET seq=10 WHERE name='sync_changes'")
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	limits := syncPageLimits{Records: 5, PayloadBytes: 4096, PageBytes: 8192}
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
	if err != nil || page.HighWatermark != 10 || page.HasMore || len(page.Changes) != 1 || page.Changes[0].Seq != 2 || page.Changes[0].Record.EntityID != two.ID {
		t.Fatal("a retained gap changed the real sequence or watermark", page, err)
	}
	cursor = page.NextCursor
	page, err = auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
	if err != nil || page.HighWatermark != 10 || page.HasMore || len(page.Changes) != 0 || page.NextCursor != cursor {
		t.Fatal("an empty page crossed an allocated gap", page, err)
	}
	three := createLocation(t, locations, token, "SYNC-GAP-THREE", nil)
	page, err = auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
	if err != nil || page.HighWatermark != 11 || len(page.Changes) != 1 || page.Changes[0].Seq != 11 || page.Changes[0].Record.EntityID != three.ID {
		t.Fatal("a later append lost its allocated sequence", page, err)
	}
}

func TestSyncDeltaReadsAfterActivityAndRechecksAuthorization(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-ACTIVITY", nil)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	limits := syncPageLimits{Records: 10, PayloadBytes: 4096, PageBytes: 8192}
	execSQL(t, s.db, `CREATE TRIGGER sync_delta_activity AFTER UPDATE OF last_seen_at ON sessions BEGIN
 INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('location','late-from-activity','delete',1,NULL,'2026-01-01T00:00:00Z'); END`)
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
	if err != nil || page.HighWatermark != 2 || len(page.Changes) != 2 || page.Changes[1].Record.EntityID != "late-from-activity" {
		t.Fatal("changes were read before session activity", page, err)
	}
	execSQL(t, s.db, "DROP TRIGGER sync_delta_activity")
	for _, test := range []struct {
		name, timing, statement string
		want                    error
	}{
		{"ignored activity", "BEFORE", "SELECT RAISE(IGNORE)", ErrDomainState},
		{"revoked", "AFTER", "UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id", ErrAuthentication},
		{"epoch changed", "AFTER", "UPDATE sync_state SET dataset_epoch='changed-during-sync-delta' WHERE singleton=1", ErrAuthentication},
		{"mandatory password", "AFTER", "UPDATE admin_accounts SET must_change_password=1", ErrPasswordChangeRequired},
		{"signer replaced", "AFTER", "UPDATE auth_server_secret SET key_bytes=zeroblob(32)", ErrAuthState},
		{"signer removed", "AFTER", "DELETE FROM auth_server_secret", ErrAuthState},
		{"signer proof changed", "AFTER", "INSERT INTO auth_keyed_receipt_secret(singleton,key_proof) VALUES(1,zeroblob(32))", ErrAuthState},
	} {
		t.Run(test.name, func(t *testing.T) {
			execSQL(t, s.db, "CREATE TRIGGER sync_delta_failure "+test.timing+" UPDATE OF last_seen_at ON sessions BEGIN "+test.statement+"; END")
			defer execSQL(t, s.db, "DROP TRIGGER sync_delta_failure")
			before := domainState(t, s)
			secret := scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret WHERE singleton=1")
			proofCount := scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret")
			page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(page, syncDeltaPage{}) {
				t.Fatal("failed activity exposed a change page", page, err)
			}
			requireDomainState(t, s, before)
			if !reflect.DeepEqual(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret WHERE singleton=1"), secret) ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != proofCount {
				t.Fatal("failed read committed a signer or proof change")
			}
		})
	}
	execSQL(t, s.db, "CREATE TABLE sync_delta_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	execSQL(t, s.db, "CREATE TRIGGER sync_delta_failure AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO sync_delta_guard VALUES('missing'); END")
	before := domainState(t, s)
	if value, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits); err == nil || !reflect.DeepEqual(value, syncDeltaPage{}) {
		t.Fatal("a failed commit exposed a change page", value, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_delta_guard") != 0 {
		t.Fatal("a failed commit retained its deferred guard row")
	}
	execSQL(t, s.db, "DROP TRIGGER sync_delta_failure")
	base, calls := auth.now(), 0
	auth.now = func() time.Time {
		calls++
		if calls > 3 {
			return base.Add(31 * 24 * time.Hour)
		}
		return base
	}
	if value, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits); !errors.Is(err, ErrAuthentication) || !reflect.DeepEqual(value, syncDeltaPage{}) {
		t.Fatal("a session expiring during the read exposed a page", value, err)
	}
}

func TestSyncDeltaCursorSurvivesFreshLoginButNotAnotherEpoch(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-LOGIN", nil)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	limits := syncPageLimits{Records: 10, PayloadBytes: 4096, PageBytes: 8192}
	freshLogin := func() string {
		completedFixtureAuthSetup.Lock()
		defer completedFixtureAuthSetup.Unlock()
		_, fresh := login(t, auth)
		return fresh
	}
	fresh := freshLogin()
	if page, err := auth.readSyncDeltaPage(t.Context(), fresh, cursor, limits); err != nil || len(page.Changes) != 1 {
		t.Fatal("same-principal fresh session could not use its cursor", page, err)
	}
	execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='new-sync-epoch'")
	fresh = freshLogin()
	if page, err := auth.readSyncDeltaPage(t.Context(), fresh, cursor, limits); !errors.Is(err, ErrSyncResetRequired) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("a cursor from the prior epoch survived", page, err)
	}
}

func TestSyncDeltaPreservesCompositeControlIdentitiesAndFrozenBytes(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	item, location := strings.Repeat("\x00", 128), strings.Repeat("\x1f", 128)
	identity := projectionPair(item, location)
	if len(identity) != maxSyncIdentityBytes {
		t.Fatal("fixture did not reach the exact composite-identity bound", len(identity))
	}
	encodedItem, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	encodedLocation, err := json.Marshal(location)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{ "item_id":` + string(encodedItem) + `,"location_id":` + string(encodedLocation) + `,"quantity_base":0,"unit_code":"pcs","base_unit":"pcs","scale_factor":1 }`
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('stock',?,'upsert',1,?,'2026-01-01T00:00:00Z')`, identity, payload)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
	if err != nil || page.HasMore || len(page.Changes) != 1 || page.Changes[0].Record.EntityID != identity || string(page.Changes[0].Record.Payload) != payload {
		t.Fatal("an exact composite identity or frozen payload changed", page, err)
	}
}

func TestSyncDeltaRejectsCorruptRetainedRowsWithoutAPrefix(t *testing.T) {
	for _, test := range []struct {
		name, statement string
		arguments       []any
	}{
		{"oversized identity", `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('location',?,'delete',1,NULL,'2026-01-01T00:00:00Z')`, []any{strings.Repeat("x", maxSyncIdentityBytes+1)}},
		{"oversized kind", `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES(?,'bad','delete',1,NULL,'2026-01-01T00:00:00Z')`, []any{strings.Repeat("x", 33)}},
		{"unsafe revision", `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('location','bad','delete',9007199254740992,NULL,'2026-01-01T00:00:00Z')`, nil},
		{"duplicate JSON key", `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('location','bad','upsert',1,?,'2026-01-01T00:00:00Z')`, []any{`{"id":"bad","id":"bad","parent_id":null,"code":"bad","name":"bad","status":"active","kind":"other","sort_order":0,"description":""}`}},
		{"invalid surrogate", `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('location','bad','upsert',1,?,'2026-01-01T00:00:00Z')`, []any{`{"id":"bad","parent_id":null,"code":"bad","name":"\ud800","status":"active","kind":"other","sort_order":0,"description":""}`}},
		{"negative sequence", `INSERT INTO sync_changes(seq,entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES(-2,'location','bad','delete',1,NULL,'2026-01-01T00:00:00Z')`, nil},
		{"invalid retained time", `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('location','bad','delete',1,NULL,?)`, []any{strings.Repeat("x", 36)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, locations, auth, token := locationFixture(t)
			createLocation(t, locations, token, "SYNC-CORRUPT", nil)
			cursor := testSyncDeltaCursor(t, s, auth, 0)
			execSQL(t, s.db, test.statement, test.arguments...)
			before := domainState(t, s)
			page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
			if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncDeltaPage{}) {
				t.Fatal("corrupt retained state returned a page", page, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestSyncDeltaCancelsWhileWaitingForSQLiteAndRejectsAnUnavailableSigner(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-WAIT", nil)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	page, err := auth.readSyncDeltaPage(ctx, token, cursor, limits)
	cancel()
	if err = errors.Join(err, held.Rollback()); !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("a canceled waiting read returned data", page, err)
	}
	auth.serverSecret = nil
	before := domainState(t, s)
	if page, err = auth.readSyncDeltaPage(t.Context(), token, cursor, limits); !errors.Is(err, ErrAuthState) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("an unavailable signer returned a page", page, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncDeltaPagesFiveHundredAndValidatesLookahead(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(t.Context(), `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('location',?,'delete',1,NULL,'2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 501; index++ {
		if _, err = statement.ExecContext(t.Context(), fmt.Sprintf("tombstone-%03d", index)); err != nil {
			statement.Close()
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	limits := syncPageLimits{Records: 500, PayloadBytes: 4096, PageBytes: 32768}
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits)
	if err != nil || page.HighWatermark != 501 || !page.HasMore || len(page.Changes) != 500 || page.Changes[0].Seq != 1 || page.Changes[499].Seq != 500 {
		t.Fatal("the maximum-size page lost its lookahead", len(page.Changes), page.HasMore, err)
	}
	page, err = auth.readSyncDeltaPage(t.Context(), token, page.NextCursor, limits)
	if err != nil || page.HighWatermark != 501 || page.HasMore || len(page.Changes) != 1 || page.Changes[0].Seq != 501 || page.Changes[0].Record.Payload != nil {
		t.Fatal("the continuation lost its last tombstone", page, err)
	}
	page, err = auth.readSyncDeltaPage(t.Context(), token, page.NextCursor, limits)
	if err != nil || page.Changes == nil || len(page.Changes) != 0 {
		t.Fatal("empty changes were not an explicit array", page, err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || !strings.Contains(string(encoded), `"changes":[]`) {
		t.Fatal("empty changes did not serialize as an array", string(encoded), err)
	}
}

func TestSyncDeltaAllowsAValidatedSearchTombstoneWithoutText(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	identity := projectionPair("item", "retained-item")
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',?,'delete',4,NULL,'2026-01-01T00:00:00Z')`, identity)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
	if err != nil || len(page.Changes) != 1 || page.Changes[0].Record.EntityID != identity ||
		page.Changes[0].Record.Operation != "delete" || page.Changes[0].Record.Payload != nil {
		t.Fatal("a text-free search tombstone was withheld", page, err)
	}
}

func TestSyncDeltaBoundsDatasetIdentityBeforeReading(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-DATASET", nil)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	maximum := strings.Repeat("😀", 128)
	execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch=?", maximum)
	execSQL(t, s.db, "UPDATE sessions SET dataset_epoch=?", maximum)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	if page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits); err != nil || page.DatasetEpoch != maximum || len(page.Changes) != 1 {
		t.Fatal("the maximum valid UTF-8 epoch was rejected", page, err)
	}
	oversized := strings.Repeat("😀", 129)
	execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch=?", oversized)
	execSQL(t, s.db, "UPDATE sessions SET dataset_epoch=?", oversized)
	before := domainState(t, s)
	if page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, limits); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("an oversized dataset epoch returned a page", page, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncDeltaAcceptsTheMatchingPinnedSignerProof(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-PROOF", nil)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	proof := keyedReceiptSecretProof(auth.serverSecret)
	execSQL(t, s.db, "INSERT INTO auth_keyed_receipt_secret(singleton,key_proof) VALUES(1,?)", proof)
	page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
	if err != nil || len(page.Changes) != 1 || !reflect.DeepEqual(scalar[[]byte](t, s.db, "SELECT key_proof FROM auth_keyed_receipt_secret"), proof) {
		t.Fatal("the matching pinned signer proof did not survive", page, err)
	}
}

func TestSyncDeltaWithholdsDataWhenTheChangeSourceIsUnavailable(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "SYNC-MISSING", nil)
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	before := domainState(t, s)
	func() {
		execSQL(t, s.db, "ALTER TABLE sync_changes RENAME TO unavailable_sync_changes")
		defer execSQL(t, s.db, "ALTER TABLE unavailable_sync_changes RENAME TO sync_changes")
		page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
		if err == nil || !reflect.DeepEqual(page, syncDeltaPage{}) {
			t.Fatal("a missing frozen source returned a page", page, err)
		}
	}()
	requireDomainState(t, s, before)
}
