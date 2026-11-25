package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"homebox/migrations"
)

func TestVersionStateReadsActualDatasetAndVerifiedMigrationHistory(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	value, err := auth.ReadVersionState(t.Context(), token)
	if err != nil || value.SchemaVersion != len(all) || value.ProjectionVersion != 1 ||
		value.DatasetEpoch != scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") ||
		value.ServerInstanceID != scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state") {
		t.Fatal("version state did not describe the actual dataset and migration history", value, err)
	}
}

func TestVersionStateRequiresAndRevalidatesACompletedSession(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	zero := VersionState{}
	if value, err := auth.ReadVersionState(t.Context(), "invalid-token"); !errors.Is(err, ErrAuthentication) || value != zero {
		t.Fatal("invalid session read version state", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
	if value, err := auth.ReadVersionState(t.Context(), token); !errors.Is(err, ErrPasswordChangeRequired) || value != zero {
		t.Fatal("incomplete session read version state", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=0")
	for _, test := range []struct {
		name, timing, statement string
		want                    error
	}{
		{"ignored activity", "BEFORE", "SELECT RAISE(IGNORE)", ErrDomainState},
		{"revoked", "AFTER", "UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id", ErrAuthentication},
		{"epoch changed", "AFTER", "UPDATE sync_state SET dataset_epoch='changed-during-version-read' WHERE singleton=1", ErrAuthentication},
		{"password changed", "AFTER", "UPDATE admin_accounts SET must_change_password=1", ErrPasswordChangeRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			execSQL(t, s.db, "CREATE TRIGGER version_state_late "+test.timing+" UPDATE OF last_seen_at ON sessions BEGIN "+test.statement+"; END")
			defer execSQL(t, s.db, "DROP TRIGGER version_state_late")
			before := domainState(t, s)
			value, err := auth.ReadVersionState(t.Context(), token)
			if !errors.Is(err, test.want) || value != zero {
				t.Fatal("version state survived failed session revalidation", value, err)
			}
			requireDomainState(t, s, before)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := auth.ReadVersionState(ctx, token); !errors.Is(err, context.Canceled) || value != zero {
		t.Fatal("canceled read returned version state", value, err)
	}
	if value, err := auth.ReadVersionState(nil, token); !errors.Is(err, ErrDomainInput) || value != zero {
		t.Fatal("nil context was accepted", value, err)
	}
	var absent *Auth
	if value, err := absent.ReadVersionState(t.Context(), token); !errors.Is(err, ErrDomainInput) || value != zero {
		t.Fatal("nil authentication was accepted", value, err)
	}
	if value, err := (&Auth{}).ReadVersionState(t.Context(), token); !errors.Is(err, ErrDomainInput) || value != zero {
		t.Fatal("authentication without a store was accepted", value, err)
	}
	if value, err := (&Auth{store: &Store{}}).ReadVersionState(t.Context(), token); !errors.Is(err, ErrDomainInput) || value != zero {
		t.Fatal("authentication without a database was accepted", value, err)
	}
}

func TestVersionStateRejectsExpiryAfterReadingTheDataset(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	before := domainState(t, s)
	base, calls := auth.now().UTC(), 0
	auth.now = func() time.Time {
		calls++
		if calls >= 4 {
			return base.Add(auth.lifetime + time.Minute)
		}
		return base
	}
	value, err := auth.ReadVersionState(t.Context(), token)
	if !errors.Is(err, ErrAuthentication) || value != (VersionState{}) || calls < 4 {
		t.Fatal("expired session received version state", value, err, calls)
	}
	requireDomainState(t, s, before)
}

func TestVersionStateCancellationWhileWaitingForSQLite(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	value, err := auth.ReadVersionState(ctx, token)
	if !errors.Is(err, context.DeadlineExceeded) || value != (VersionState{}) {
		t.Fatal("a canceled waiting read returned version state", value, err)
	}
}

func TestVersionStateWithholdsResultsWhenCommitFails(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	execSQL(t, s.db, "CREATE TABLE version_state_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	execSQL(t, s.db, "CREATE TRIGGER version_state_commit_failure AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO version_state_commit_guard VALUES('missing'); END")
	before := domainState(t, s)
	value, err := auth.ReadVersionState(t.Context(), token)
	if err == nil || value != (VersionState{}) {
		t.Fatal("failed commit returned version state", value, err)
	}
	requireDomainState(t, s, before)
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM version_state_commit_guard"); count != 0 {
		t.Fatal("failed commit retained a late write", count)
	}
}

func TestVersionStateReadsAfterSessionActivityAndRejectsLateLedgerChanges(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	execSQL(t, s.db, `CREATE TRIGGER version_state_late AFTER UPDATE OF last_seen_at ON sessions BEGIN
 UPDATE sync_state SET server_instance_id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE singleton=1; END`)
	value, err := auth.ReadVersionState(t.Context(), token)
	if err != nil || value.ServerInstanceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("version state was read before the activity write", value, err)
	}
	execSQL(t, s.db, "DROP TRIGGER version_state_late")
	original := scalar[string](t, s.db, "SELECT sha256 FROM schema_migration_checksums WHERE version=1")
	execSQL(t, s.db, `CREATE TRIGGER version_state_late AFTER UPDATE OF last_seen_at ON sessions BEGIN
 UPDATE schema_migration_checksums SET sha256='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE version=1; END`)
	defer execSQL(t, s.db, "DROP TRIGGER version_state_late")
	before := domainState(t, s)
	value, err = auth.ReadVersionState(t.Context(), token)
	if !errors.Is(err, ErrSchemaChanged) || !reflect.DeepEqual(value, VersionState{}) {
		t.Fatal("version state accepted a late changed migration ledger", value, err)
	}
	requireDomainState(t, s, before)
	if got := scalar[string](t, s.db, "SELECT sha256 FROM schema_migration_checksums WHERE version=1"); got != original {
		t.Fatal("failed version read did not roll back the changed ledger", got)
	}
}

func TestVersionStateRejectsInvalidDatasetAndMigrationHistory(t *testing.T) {
	for _, test := range []struct {
		name, statement string
		want            error
	}{
		{"invalid instance", "UPDATE sync_state SET server_instance_id='invalid'", ErrDomainState},
		{"negative floor", "UPDATE sync_state SET min_retained_seq=-1", ErrDomainState},
		{"unsafe floor", "UPDATE sync_state SET min_retained_seq=9007199254740992", ErrDomainState},
		{"missing checksum", "DELETE FROM schema_migration_checksums WHERE version=1", ErrSchemaChanged},
		{"missing ledger", "DROP TABLE schema_migration_checksums", ErrSchemaChanged},
		{"migration gap", "DELETE FROM schema_migration_checksums WHERE version=2; DELETE FROM schema_migrations WHERE version=2", ErrSchemaChanged},
		{"incomplete history", "DELETE FROM schema_migration_checksums WHERE version=(SELECT MAX(version) FROM schema_migrations); DELETE FROM schema_migrations WHERE version=(SELECT MAX(version) FROM schema_migrations)", ErrSchemaChanged},
		{"changed checksum", "UPDATE schema_migration_checksums SET sha256='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE version=1", ErrSchemaChanged},
		{"future migration", "INSERT INTO schema_migrations(version,name,applied_at) VALUES(999,'future','2026')", ErrNewerSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, _, auth, token := locationFixture(t)
			execSQL(t, s.db, test.statement)
			before := domainState(t, s)
			value, err := auth.ReadVersionState(t.Context(), token)
			if !errors.Is(err, test.want) || value != (VersionState{}) {
				t.Fatal("invalid version authority returned partial state", value, err)
			}
			requireDomainState(t, s, before)
		})
	}
}
