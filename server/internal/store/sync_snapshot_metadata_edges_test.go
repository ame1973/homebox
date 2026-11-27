package store

import (
	"errors"
	"testing"
	"time"
)

func TestSyncSnapshotMetadataRejectsInvalidActualDatasetAndWatermark(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *Store, *Auth, string)
	}{
		{"invalid instance", func(t *testing.T, s *Store, _ *Auth, _ string) {
			execSQL(t, s.db, "UPDATE sync_state SET server_instance_id='invalid' WHERE singleton=1")
		}},
		{"allocated watermark behind retained row", func(t *testing.T, s *Store, a *Auth, token string) {
			locations, err := s.NewLocations(a)
			if err != nil {
				t.Fatal(err)
			}
			createLocation(t, locations, token, "INVALID-WATERMARK", nil)
			execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=0 WHERE name='sync_changes'")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, auth, _, token, _, signed := snapshotMetadataFixture(t)
			test.damage(t, s, auth, token)
			before := domainState(t, s)
			value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
			if !errors.Is(err, ErrDomainState) || value != (syncSnapshotMetadata{}) {
				t.Fatal("invalid dataset returned snapshot metadata", value, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestSyncSnapshotMetadataRevalidatesSessionAfterObservation(t *testing.T) {
	s, auth, now, token, _, signed := snapshotMetadataFixture(t)
	base, calls := *now, 0
	auth.now = func() time.Time {
		calls++
		if calls >= 5 {
			return base.Add(auth.idle + time.Second)
		}
		return base
	}
	before := domainState(t, s)
	value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
	if !errors.Is(err, ErrAuthentication) || value != (syncSnapshotMetadata{}) || calls < 5 {
		t.Fatal("expired session received snapshot metadata", value, err, calls)
	}
	requireDomainState(t, s, before)
}

func TestSyncSnapshotMetadataRejectsClockReversalBeforeCreation(t *testing.T) {
	s, auth, now, token, cursor, _ := snapshotMetadataFixture(t)
	base, calls := *now, 0
	cursor.CreatedAt = stamp(base.Add(time.Second))
	cursor.ExpiresAt = stamp(base.Add(time.Second + syncSnapshotLifetime))
	execSQL(t, s.db, "UPDATE sync_snapshots SET created_at=?,expires_at=? WHERE id=?", cursor.CreatedAt, cursor.ExpiresAt, cursor.SnapshotID)
	signed, err := encodeSyncSnapshotCursor(auth, cursor)
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time {
		calls++
		if calls == 4 {
			return base.Add(time.Second)
		}
		return base
	}
	before := domainState(t, s)
	value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
	if !errors.Is(err, ErrInvalidCursor) || value != (syncSnapshotMetadata{}) || calls < 5 {
		t.Fatal("a clock reversal before snapshot creation returned metadata", value, err, calls)
	}
	requireDomainState(t, s, before)
}

func TestSyncSnapshotMetadataWithholdsResultsWhenTablesAreUnavailable(t *testing.T) {
	for _, table := range []string{"sync_snapshots", "sync_snapshot_rows"} {
		t.Run(table, func(t *testing.T) {
			s, auth, _, token, _, signed := snapshotMetadataFixture(t)
			execSQL(t, s.db, "ALTER TABLE "+table+" RENAME TO "+table+"_fixture_only")
			before := domainState(t, s)
			value, err := auth.readSyncSnapshotMetadata(t.Context(), token, signed)
			if err == nil || value != (syncSnapshotMetadata{}) {
				t.Fatal("missing snapshot table returned metadata", value, err)
			}
			requireDomainState(t, s, before)
		})
	}
}
