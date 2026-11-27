package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func snapshotMetadataFixture(t *testing.T) (*Store, *Auth, *time.Time, string, syncSnapshotCursor, string) {
	t.Helper()
	s, auth, now, session, token := completedAuthFixture(t)
	cursor := syncSnapshotCursor{Version: 1, Scope: syncCursorScope{
		AdminID: session.Admin.ID, ServerInstanceID: scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state"),
		DatasetEpoch: scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state"), ProjectionVersion: 1,
	}, SnapshotID: "snapshot-家", CreatedAt: stamp(*now), ExpiresAt: stamp(now.Add(syncSnapshotLifetime))}
	execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 VALUES(?,?,?,?,?,?,?,'ready')`, cursor.SnapshotID, cursor.Scope.ServerInstanceID, cursor.Scope.DatasetEpoch, cursor.Scope.ProjectionVersion,
		cursor.HighWatermark, cursor.CreatedAt, cursor.ExpiresAt)
	signed, err := encodeSyncSnapshotCursor(auth, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return s, auth, now, token, cursor, signed
}

func addSnapshotMetadataRow(t *testing.T, s *Store, id string, ordinal int64, kind, entity, payload string) {
	t.Helper()
	execSQL(t, s.db, `INSERT INTO sync_snapshot_rows(snapshot_id,ordinal,entity_type,entity_id,entity_revision,payload_json)
 VALUES(?,?,?,?,1,?)`, id, ordinal, kind, entity, payload)
}

func TestSyncSnapshotMetadataReadsOnlyBoundFrozenMetadata(t *testing.T) {
	s, auth, _, token, cursor, signed := snapshotMetadataFixture(t)
	addSnapshotMetadataRow(t, s, cursor.SnapshotID, 0, "location", "location-id", `{"private":"fixture-location"}`)
	addSnapshotMetadataRow(t, s, cursor.SnapshotID, 1, "search_doc", projectionPair("item", "item-id"), `{"private":"unreviewed-fixture-ocr"}`)
	value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
	if err != nil || value.Cursor != cursor || value.TotalRows != 2 {
		t.Fatal("snapshot metadata did not retain its signed scope and actual ordinal count", value, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil || strings.Contains(string(encoded), "unreviewed-fixture-ocr") || strings.Contains(string(encoded), "fixture-location") {
		t.Fatal("a metadata-only observation exposed frozen payload", err)
	}
	cursor.NextOrdinal = 2
	end, err := encodeSyncSnapshotCursor(auth, cursor)
	if err != nil {
		t.Fatal(err)
	}
	value, err = auth.readSyncSnapshotMetadata(t.Context(), token, end)
	if err != nil || value.Cursor != cursor || value.TotalRows != 2 {
		t.Fatal("a position at the end of the snapshot was not retained", value, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_rows WHERE snapshot_id=?", cursor.SnapshotID) != 2 {
		t.Fatal("metadata observation changed frozen rows")
	}
}

func TestSyncSnapshotMetadataRejectsScopeAndStoredManifestMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		alter  func(*testing.T, *Store, *Auth, *syncSnapshotCursor)
		resign bool
		want   error
	}{
		{"missing snapshot", func(t *testing.T, s *Store, _ *Auth, c *syncSnapshotCursor) {
			execSQL(t, s.db, "DELETE FROM sync_snapshots WHERE id=?", c.SnapshotID)
		}, false, ErrDomainState},
		{"mismatched instance", func(t *testing.T, s *Store, _ *Auth, c *syncSnapshotCursor) {
			other := strings.Repeat("a", 32)
			if other == c.Scope.ServerInstanceID {
				other = strings.Repeat("b", 32)
			}
			execSQL(t, s.db, "UPDATE sync_snapshots SET server_instance_id=? WHERE id=?", other, c.SnapshotID)
		}, false, ErrDomainState},
		{"mismatched epoch", func(t *testing.T, s *Store, _ *Auth, c *syncSnapshotCursor) {
			execSQL(t, s.db, "UPDATE sync_snapshots SET dataset_epoch='other-epoch' WHERE id=?", c.SnapshotID)
		}, false, ErrDomainState},
		{"mismatched watermark", func(t *testing.T, s *Store, _ *Auth, c *syncSnapshotCursor) {
			execSQL(t, s.db, "UPDATE sync_snapshots SET high_watermark=1 WHERE id=?", c.SnapshotID)
		}, false, ErrDomainState},
		{"mismatched creation", func(t *testing.T, s *Store, _ *Auth, c *syncSnapshotCursor) {
			execSQL(t, s.db, "UPDATE sync_snapshots SET created_at='2000-01-01T00:00:00.000000000Z' WHERE id=?", c.SnapshotID)
		}, false, ErrDomainState},
		{"mismatched expiry", func(t *testing.T, s *Store, _ *Auth, c *syncSnapshotCursor) {
			execSQL(t, s.db, "UPDATE sync_snapshots SET expires_at='2000-01-01T00:00:00.000000000Z' WHERE id=?", c.SnapshotID)
		}, false, ErrDomainState},
		{"prematurely expired status", func(t *testing.T, s *Store, _ *Auth, c *syncSnapshotCursor) {
			execSQL(t, s.db, "UPDATE sync_snapshots SET status='expired' WHERE id=?", c.SnapshotID)
		}, false, ErrDomainState},
		{"ordinal gap", func(t *testing.T, s *Store, _ *Auth, c *syncSnapshotCursor) {
			addSnapshotMetadataRow(t, s, c.SnapshotID, 0, "location", "first", `{}`)
			addSnapshotMetadataRow(t, s, c.SnapshotID, 2, "location", "third", `{}`)
		}, false, ErrDomainState},
		{"position beyond rows", func(_ *testing.T, _ *Store, _ *Auth, c *syncSnapshotCursor) { c.NextOrdinal = 1 }, true, ErrInvalidCursor},
		{"different principal", func(_ *testing.T, _ *Store, _ *Auth, c *syncSnapshotCursor) { c.Scope.AdminID = "other-admin" }, true, ErrInvalidCursor},
		{"different instance", func(_ *testing.T, _ *Store, _ *Auth, c *syncSnapshotCursor) {
			other := strings.Repeat("f", 32)
			if other == c.Scope.ServerInstanceID {
				other = strings.Repeat("e", 32)
			}
			c.Scope.ServerInstanceID = other
		}, true, ErrSyncResetRequired},
		{"different epoch", func(_ *testing.T, _ *Store, _ *Auth, c *syncSnapshotCursor) { c.Scope.DatasetEpoch = "other-epoch" }, true, ErrSyncResetRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, auth, _, token, cursor, signed := snapshotMetadataFixture(t)
			test.alter(t, s, auth, &cursor)
			if test.resign {
				var err error
				signed, err = encodeSyncSnapshotCursor(auth, cursor)
				if err != nil {
					t.Fatal(err)
				}
			}
			value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(value, syncSnapshotMetadata{}) {
				t.Fatal("invalid snapshot metadata returned a result", value, err)
			}
		})
	}
}

func TestSyncSnapshotMetadataRechecksSessionAndExpiry(t *testing.T) {
	s, auth, now, token, cursor, signed := snapshotMetadataFixture(t)
	if value, err := auth.readSyncSnapshotMetadata(t.Context(), "invalid-token", signed); !errors.Is(err, ErrAuthentication) || value != (syncSnapshotMetadata{}) {
		t.Fatal("invalid session received snapshot metadata", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
	if value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed); !errors.Is(err, ErrPasswordChangeRequired) || value != (syncSnapshotMetadata{}) {
		t.Fatal("incomplete session received snapshot metadata", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=0")
	for _, invalid := range []string{"", signed + "a", strings.Repeat("x", maxSyncCursorBytes+1)} {
		if value, err := auth.readSyncSnapshotMetadata(t.Context(), token, invalid); !errors.Is(err, ErrInvalidCursor) || value != (syncSnapshotMetadata{}) {
			t.Fatal("invalid signed cursor returned metadata", value, err)
		}
	}
	for _, test := range []struct {
		name, timing, statement string
		want                    error
	}{
		{"ignored activity", "BEFORE", "SELECT RAISE(IGNORE)", ErrDomainState},
		{"revoked", "AFTER", "UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id", ErrAuthentication},
		{"epoch changed", "AFTER", "UPDATE sync_state SET dataset_epoch='changed-during-snapshot-metadata' WHERE singleton=1", ErrAuthentication},
		{"password", "AFTER", "UPDATE admin_accounts SET must_change_password=1", ErrPasswordChangeRequired},
		{"signer replaced", "AFTER", "UPDATE auth_server_secret SET key_bytes=zeroblob(32)", ErrAuthState},
		{"signer removed", "AFTER", "DELETE FROM auth_server_secret", ErrAuthState},
		{"receipt proof changed", "AFTER", "INSERT INTO auth_keyed_receipt_secret(singleton,key_proof) VALUES(1,zeroblob(32))", ErrAuthState},
		{"search signer changed", "AFTER", "INSERT INTO sync_search_signer(singleton,key_proof) VALUES(1,zeroblob(32))", ErrAuthState},
	} {
		t.Run(test.name, func(t *testing.T) {
			execSQL(t, s.db, "CREATE TRIGGER snapshot_metadata_activity "+test.timing+" UPDATE OF last_seen_at ON sessions BEGIN "+test.statement+"; END")
			defer execSQL(t, s.db, "DROP TRIGGER snapshot_metadata_activity")
			before := domainState(t, s)
			secret := scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret WHERE singleton=1")
			receiptCount := scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret")
			signerCount := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer")
			value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
			if !errors.Is(err, test.want) || value != (syncSnapshotMetadata{}) {
				t.Fatal("metadata survived failed authorization", value, err)
			}
			requireDomainState(t, s, before)
			if !reflect.DeepEqual(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret WHERE singleton=1"), secret) ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != receiptCount ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_signer") != signerCount {
				t.Fatal("failed metadata read retained signer changes")
			}
		})
	}
	*now = now.Add(syncSnapshotLifetime - time.Nanosecond)
	if value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed); err != nil || value.Cursor != cursor {
		t.Fatal("snapshot expired before its exact deadline", value, err)
	}
	*now = now.Add(time.Nanosecond)
	if value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed); !errors.Is(err, ErrSyncSnapshotExpired) || value != (syncSnapshotMetadata{}) {
		t.Fatal("snapshot survived its exact expiry", value, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshots WHERE id=?", cursor.SnapshotID) != 1 {
		t.Fatal("an expired metadata read deleted the snapshot")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := auth.readSyncSnapshotMetadata(ctx, token, signed); !errors.Is(err, context.Canceled) || value != (syncSnapshotMetadata{}) {
		t.Fatal("canceled read returned metadata", value, err)
	}
	if value, err := auth.readSyncSnapshotMetadata(nil, token, signed); !errors.Is(err, ErrDomainInput) || value != (syncSnapshotMetadata{}) {
		t.Fatal("nil context returned metadata", value, err)
	}
	var absent *Auth
	if value, err := absent.readSyncSnapshotMetadata(t.Context(), token, signed); !errors.Is(err, ErrDomainInput) || value != (syncSnapshotMetadata{}) {
		t.Fatal("nil authentication returned metadata", value, err)
	}
}

func TestSyncSnapshotMetadataTracksRetentionWithoutMovingItsWatermark(t *testing.T) {
	s, auth, _, token, cursor, signed := snapshotMetadataFixture(t)
	locations, err := s.NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	createLocation(t, locations, token, "AFTER-SNAPSHOT", nil)
	value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
	if err != nil || value.Cursor.HighWatermark != 0 || value.TotalRows != 0 {
		t.Fatal("a later append changed frozen snapshot metadata", value, err)
	}
	// The inclusive floor is the last pruned sequence. A snapshot at H=0
	// cannot be completed if the delta immediately after it was pruned.
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq<=1; UPDATE sync_state SET min_retained_seq=1")
	if value, err = auth.readSyncSnapshotMetadata(t.Context(), token, signed); !errors.Is(err, ErrSyncResetRequired) || value != (syncSnapshotMetadata{}) {
		t.Fatal("metadata survived lost post-snapshot changes", value, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshots WHERE id=?", cursor.SnapshotID) != 1 {
		t.Fatal("retention check deleted a snapshot")
	}
}

func TestSyncSnapshotMetadataAcceptsInclusiveRetentionFloor(t *testing.T) {
	s, auth, _, token, cursor, _ := snapshotMetadataFixture(t)
	locations, err := s.NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	createLocation(t, locations, token, "BEFORE-BOUNDARY", nil)
	cursor.HighWatermark = 1
	execSQL(t, s.db, "UPDATE sync_snapshots SET high_watermark=1 WHERE id=?", cursor.SnapshotID)
	signed, err := encodeSyncSnapshotCursor(auth, cursor)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq<=1; UPDATE sync_state SET min_retained_seq=1")
	createLocation(t, locations, token, "AFTER-BOUNDARY", nil)
	value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
	if err != nil || value.Cursor != cursor || value.TotalRows != 0 {
		t.Fatal("a snapshot at the inclusive retention floor was rejected", value, err)
	}
}

func TestSyncSnapshotMetadataRejectsWatermarkAheadOfActualDataset(t *testing.T) {
	s, auth, _, token, cursor, _ := snapshotMetadataFixture(t)
	cursor.HighWatermark = 1
	execSQL(t, s.db, "UPDATE sync_snapshots SET high_watermark=1 WHERE id=?", cursor.SnapshotID)
	signed, err := encodeSyncSnapshotCursor(auth, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed); !errors.Is(err, ErrSyncResetRequired) || value != (syncSnapshotMetadata{}) {
		t.Fatal("snapshot ahead of the allocated watermark was accepted", value, err)
	}
}

func TestSyncSnapshotMetadataRejectsExpiryDuringObservation(t *testing.T) {
	s, auth, now, token, _, signed := snapshotMetadataFixture(t)
	base, calls := *now, 0
	auth.now = func() time.Time {
		calls++
		if calls >= 5 {
			return base.Add(syncSnapshotLifetime)
		}
		return base
	}
	before := domainState(t, s)
	value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
	if !errors.Is(err, ErrSyncSnapshotExpired) || value != (syncSnapshotMetadata{}) || calls < 5 {
		t.Fatal("snapshot expiry during observation returned metadata", value, err, calls)
	}
	requireDomainState(t, s, before)
}

func TestSyncSnapshotMetadataWithholdsResultsWhenCommitFails(t *testing.T) {
	s, auth, _, token, _, signed := snapshotMetadataFixture(t)
	execSQL(t, s.db, "CREATE TABLE snapshot_metadata_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	execSQL(t, s.db, "CREATE TRIGGER snapshot_metadata_commit_failure AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO snapshot_metadata_commit_guard VALUES('missing'); END")
	before := domainState(t, s)
	value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
	if err == nil || value != (syncSnapshotMetadata{}) {
		t.Fatal("failed commit returned snapshot metadata", value, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM snapshot_metadata_commit_guard") != 0 {
		t.Fatal("failed commit retained its deferred write")
	}
}

func TestSyncSnapshotMetadataCancellationWhileWaitingForSQLite(t *testing.T) {
	s, auth, _, token, _, signed := snapshotMetadataFixture(t)
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	value, err := auth.readSyncSnapshotMetadata(ctx, token, signed)
	if !errors.Is(err, context.DeadlineExceeded) || value != (syncSnapshotMetadata{}) {
		t.Fatal("a canceled waiting read returned metadata", value, err)
	}
}
