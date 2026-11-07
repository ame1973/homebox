package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func profileFailureFixture(t *testing.T, create bool) (holdingFixture, AssetProfileWrite) {
	t.Helper()
	if create {
		fixture := holdingsFixture(t)
		return fixture, manualProfileInput(fixture.individual.Revision, 1)
	}
	fixture, before := manualAssetFixture(t, 0)
	input := retainedProfileInput(fixture.individual.Revision, before)
	input.ManualAcquisitionMinor = testPointer(int64(1))
	return fixture, input
}

func TestAssetProfileEveryActualStatementFailureRollsBackSourceContextReceiptAndPublication(t *testing.T) {
	for _, failure := range []struct {
		table, verb string
		create      bool
	}{
		{"asset_profiles", "INSERT", true}, {"asset_profiles", "UPDATE", false}, {"items", "UPDATE", false},
		{"asset_profile_changes", "INSERT", false}, {"projection_revisions", "INSERT", false}, {"projection_revisions", "UPDATE", false},
		{"current_projections", "INSERT", true}, {"current_projections", "UPDATE", false},
		{"sync_changes", "INSERT", false}, {"data_revision", "UPDATE", false}, {"api_idempotency", "INSERT", false},
		{"mutation_events", "INSERT", false}, {"sessions", "UPDATE", false},
	} {
		t.Run(failure.table+failure.verb, func(t *testing.T) {
			fixture, input := profileFailureFixture(t, failure.create)
			s := fixture.store
			execSQL(t, s.db, "CREATE TRIGGER fail_profile_statement BEFORE "+failure.verb+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'profile statement failure'); END")
			before := assetProfileState(t, s)
			if value, err := assetReader(t, fixture).PutProfile(t.Context(), fixture.token, "profile-failed-statement", fixture.unit.ID, fixture.unit.Revision, input); err == nil || value != (Asset{}) {
				t.Fatal("actual failed statement exposed a financial profile result", failure, value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetProfileIgnoredOrRewrittenWritesCannotPublishIncompleteIntent(t *testing.T) {
	for _, failure := range []string{"ignored create", "ignored update", "ignored Item", "ignored context", "ignored no-op context", "rewritten profile", "corrupt stored profile"} {
		t.Run(failure, func(t *testing.T) {
			fixture, input := profileFailureFixture(t, failure == "ignored create")
			s := fixture.store
			table, verb := "asset_profiles", "UPDATE"
			switch failure {
			case "ignored create":
				verb = "INSERT"
			case "ignored Item":
				table = "items"
			case "ignored context", "ignored no-op context":
				table, verb = "asset_profile_changes", "INSERT"
				if failure == "ignored no-op context" {
					input.ManualAcquisitionMinor = testPointer(int64(0))
				}
			}
			if failure == "rewritten profile" {
				execSQL(t, s.db, "CREATE TRIGGER fail_profile_write AFTER UPDATE ON asset_profiles BEGIN UPDATE asset_profiles SET manual_acquisition_minor=999 WHERE item_unit_id=NEW.item_unit_id; END")
			} else if failure == "corrupt stored profile" {
				execSQL(t, s.db, "CREATE TRIGGER fail_profile_write AFTER UPDATE ON asset_profiles BEGIN UPDATE asset_profiles SET currency='jpy' WHERE item_unit_id=NEW.item_unit_id; END")
			} else {
				execSQL(t, s.db, "CREATE TRIGGER fail_profile_write BEFORE "+verb+" ON "+table+" BEGIN SELECT RAISE(IGNORE); END")
			}
			before := assetProfileState(t, s)
			if value, err := assetReader(t, fixture).PutProfile(t.Context(), fixture.token, "profile-ignored-write", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrDomainState) || value != (Asset{}) {
				t.Fatal("ignored/rewritten exact source or before-image became successful money", failure, value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetProfileFinalAuthorizationCancellationAndDeferredAuditCommitWithholdEveryEffect(t *testing.T) {
	for _, failure := range []string{"mandatory", "revoked", "epoch", "canceled", "late expiry", "deferred commit", "missing event"} {
		t.Run(failure, func(t *testing.T) {
			fixture, input := profileFailureFixture(t, false)
			s := fixture.store
			ctx := t.Context()
			want := ErrAuthentication
			switch failure {
			case "mandatory":
				execSQL(t, s.db, "CREATE TRIGGER fail_profile_final AFTER INSERT ON mutation_events BEGIN UPDATE admin_accounts SET must_change_password=1; END")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "CREATE TRIGGER fail_profile_final AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
			case "epoch":
				execSQL(t, s.db, "CREATE TRIGGER fail_profile_final AFTER INSERT ON mutation_events BEGIN UPDATE sync_state SET dataset_epoch='rotated-during-profile'; END")
			case "deferred commit":
				execSQL(t, s.db, "CREATE TABLE profile_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_profile_final AFTER UPDATE ON sessions BEGIN INSERT INTO profile_commit_guard VALUES('missing'); END")
				want = nil
			case "missing event":
				execSQL(t, s.db, "CREATE TRIGGER fail_profile_final BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END")
				want = nil
			case "canceled", "late expiry":
				base := fixture.auth.now()
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				calls := 0
				fixture.auth.now = func() time.Time {
					calls++
					if calls > 1 {
						if failure == "canceled" {
							cancel()
						} else {
							return base.Add(31 * 24 * time.Hour)
						}
					}
					return base
				}
				if failure == "canceled" {
					want = context.Canceled
				}
			}
			before := assetProfileState(t, s)
			if value, err := assetReader(t, fixture).PutProfile(ctx, fixture.token, "profile-late-failure", fixture.unit.ID, fixture.unit.Revision, input); err == nil || want != nil && !errors.Is(err, want) || value != (Asset{}) {
				t.Fatal("late authorization/audit/COMMIT failure exposed profile success", failure, value, err)
			}
			requireAssetProfileState(t, s, before)
			if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM profile_commit_guard") != 0 {
				t.Fatal("failed COMMIT leaked deferred guard rows")
			}
		})
	}
}

func TestAssetProfileRevisionAndSequenceExhaustionNeverExposePartialFinancialChange(t *testing.T) {
	for _, scope := range []string{"asset", "item", "catalog revision", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			fixture, input := profileFailureFixture(t, false)
			s := fixture.store
			switch scope {
			case "asset":
				setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, maxSafeInteger)
				input.AssetRevision = testPointer(maxSafeInteger)
			case "item":
				setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, maxSafeInteger)
				input.ItemRevision = maxSafeInteger
			case "catalog revision":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			}
			before := assetProfileState(t, s)
			if value, err := assetReader(t, fixture).PutProfile(t.Context(), fixture.token, "profile-exhausted-counter", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrRevisionOverflow) || value != (Asset{}) {
				t.Fatal("profile exhausted a counter/sequence but returned success", scope, value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}
