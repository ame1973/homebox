package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAssetEventsEveryActualStatementFailureRollsBackCompleteSourceAndPublication(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		for _, failure := range []struct{ table, verb string }{
			{"asset_" + kind + "_events", "INSERT"}, {"asset_profiles", "UPDATE"}, {"items", "UPDATE"},
			{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
			{"current_projections", "INSERT"}, {"current_projections", "UPDATE"}, {"sync_changes", "INSERT"},
			{"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
		} {
			t.Run(kind+"/"+failure.table+"/"+failure.verb, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				service := assetReader(t, fixture)
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_event BEFORE "+failure.verb+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'event statement failure'); END")
				before := assetProfileState(t, s)
				if value, err := recordAssetEvent(t.Context(), service, fixture.token, "event-failed-statement", fixture.unit.ID, asset.Revision, kind); err == nil || !zeroAssetEvent(value) {
					t.Fatal("failed actual financial statement returned a partial resource", value, err)
				}
				requireAssetEventState(t, s, before)
				execSQL(t, s.db, "DROP TRIGGER fail_asset_event")
				if _, err := recordAssetEvent(t.Context(), service, fixture.token, "event-failed-statement", fixture.unit.ID, asset.Revision, kind); err != nil {
					t.Fatal("rolled-back original key or pooled connection was not explicitly reusable", err)
				}
			})
		}
	}
}

func TestAssetEventsIgnoredOrRewrittenWritesCannotClaimFinancialSuccess(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		for _, failure := range []string{"source", "profile timestamp", "Item timestamp", "current", "outbox", "receipt", "audit", "session", "rewritten event", "corrupt final event", "late missing Unit counter"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				service := assetReader(t, fixture)
				table, verb := "asset_"+kind+"_events", "INSERT"
				switch failure {
				case "profile timestamp":
					table, verb = "asset_profiles", "UPDATE"
				case "Item timestamp":
					table, verb = "items", "UPDATE"
				case "current":
					table = "current_projections"
				case "outbox":
					table = "sync_changes"
				case "receipt":
					table = "api_idempotency"
				case "audit":
					table = "mutation_events"
				case "session":
					table, verb = "sessions", "UPDATE"
				}
				if failure == "rewritten event" || failure == "corrupt final event" {
					// Simulate invalid future triggers without weakening product DDL.
					execSQL(t, s.db, "DROP TRIGGER "+table+"_no_update")
					if failure == "rewritten event" {
						execSQL(t, s.db, "CREATE TRIGGER fail_asset_event AFTER INSERT ON "+table+" BEGIN UPDATE "+table+" SET note='rewritten' WHERE id=NEW.id; END")
					} else {
						execSQL(t, s.db, "CREATE TRIGGER fail_asset_event AFTER UPDATE ON items BEGIN UPDATE "+table+" SET occurred_at='invalid-final-instant'; END")
					}
				} else if failure == "late missing Unit counter" {
					execSQL(t, s.db, "CREATE TRIGGER fail_asset_event AFTER UPDATE ON asset_profiles BEGIN DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=NEW.item_unit_id; DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=NEW.item_unit_id; END")
				} else {
					execSQL(t, s.db, "CREATE TRIGGER fail_asset_event BEFORE "+verb+" ON "+table+" BEGIN SELECT RAISE(IGNORE); END")
				}
				before := assetProfileState(t, s)
				if value, err := recordAssetEvent(t.Context(), service, fixture.token, "event-skipped-or-rewritten", fixture.unit.ID, asset.Revision, kind); !errors.Is(err, ErrDomainState) || !zeroAssetEvent(value) {
					t.Fatal("ignored/rewritten financial work became a successful acknowledgement", value, err)
				}
				requireAssetEventState(t, s, before)
			})
		}
	}
}

func TestAssetEventsNewIdentitiesAndAggregateLimitsNeverReseedOrPartiallyPublish(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		for _, failure := range []string{"counter-only new identity", "Asset maximum", "Item maximum", "catalog maximum", "sequence maximum", "missing Asset counter", "missing Unit counter", "missing Item counter"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				service := assetReader(t, fixture)
				proof, want := asset.Revision, ErrRevisionOverflow
				switch failure {
				case "counter-only new identity":
					execSQL(t, s.db, "CREATE TRIGGER occupy_asset_event BEFORE INSERT ON asset_"+kind+"_events BEGIN INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES('"+kind+"',NEW.id,2); END")
					want = ErrDomainState
				case "Asset maximum":
					setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, maxSafeInteger)
					proof = maxSafeInteger
				case "Item maximum":
					setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, maxSafeInteger)
				case "catalog maximum":
					execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
				case "sequence maximum":
					execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
				default:
					entity, id := "asset", fixture.unit.ID
					if failure == "missing Unit counter" {
						entity = "unit"
					} else if failure == "missing Item counter" {
						entity, id = "item", fixture.individual.ID
					}
					execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", entity, id)
					execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", entity, id)
					want = ErrDomainState
				}
				before := assetProfileState(t, s)
				if value, err := recordAssetEvent(t.Context(), service, fixture.token, "event-unsafe-authority", fixture.unit.ID, proof, kind); !errors.Is(err, want) || !zeroAssetEvent(value) {
					t.Fatal("missing/unsafe authority or old identity returned partial event success", value, err)
				}
				requireAssetEventState(t, s, before)
			})
		}
	}
}

func TestAssetEventsFinalAuthorizationCancellationAndDeferredCommitWithholdEveryEffect(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		for _, failure := range []string{"mandatory", "revoked", "epoch", "canceled", "late expiry", "deferred commit"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				service := assetReader(t, fixture)
				ctx := t.Context()
				want := ErrAuthentication
				switch failure {
				case "mandatory":
					execSQL(t, s.db, "CREATE TRIGGER fail_asset_event_final AFTER INSERT ON mutation_events BEGIN UPDATE admin_accounts SET must_change_password=1; END")
					want = ErrPasswordChangeRequired
				case "revoked":
					execSQL(t, s.db, "CREATE TRIGGER fail_asset_event_final AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
				case "epoch":
					execSQL(t, s.db, "CREATE TRIGGER fail_asset_event_final AFTER INSERT ON mutation_events BEGIN UPDATE sync_state SET dataset_epoch='changed-during-event'; END")
				case "deferred commit":
					execSQL(t, s.db, "CREATE TABLE event_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
					execSQL(t, s.db, "CREATE TRIGGER fail_asset_event_final AFTER UPDATE ON sessions BEGIN INSERT INTO event_commit_guard VALUES('missing'); END")
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
				if value, err := recordAssetEvent(ctx, service, fixture.token, "event-final-failure", fixture.unit.ID, asset.Revision, kind); err == nil || want != nil && !errors.Is(err, want) || !zeroAssetEvent(value) {
					t.Fatal("late authorization/audit/COMMIT failure returned partial event success", value, err)
				}
				requireAssetEventState(t, s, before)
				if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM event_commit_guard") != 0 {
					t.Fatal("failed financial COMMIT retained deferred rows")
				}
			})
		}
	}
}
