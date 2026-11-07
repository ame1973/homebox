package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAssetLifecycleEveryActualStatementFailureRollsBackPhysicalAndFinancialSource(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		failures := []struct{ table, verb string }{
			{"asset_profiles", "UPDATE"}, {"item_units", "UPDATE"}, {"items", "UPDATE"},
			{"inventory_events", "INSERT"}, {"asset_profile_changes", "INSERT"}, {"unit_search_documents", "INSERT"},
			{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
			{"current_projections", "INSERT"}, {"current_projections", "UPDATE"}, {"sync_changes", "INSERT"},
			{"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
		}
		if kind == "dispose" {
			failures = append(failures, struct{ table, verb string }{"asset_disposals", "INSERT"})
		}
		for _, failure := range failures {
			t.Run(kind+"/"+failure.table+"/"+failure.verb, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				service := assetReader(t, fixture)
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_lifecycle BEFORE "+failure.verb+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'lifecycle statement failure'); END")
				before := assetProfileState(t, s)
				if value, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-failed-statement", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); err == nil || !zeroAssetEvent(value) {
					t.Fatal("failed actual physical/financial statement returned partial lifecycle success", value, err)
				}
				requireAssetEventState(t, s, before)
				execSQL(t, s.db, "DROP TRIGGER fail_asset_lifecycle")
				if _, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-failed-statement", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); err != nil {
					t.Fatal("rolled-back lifecycle key or pooled connection was not explicitly reusable", err)
				}
			})
		}
	}
}

func TestAssetLifecycleIgnoredOrRewrittenSourceAndContextCannotClaimSuccess(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		for _, failure := range []string{"ignored Unit", "ignored profile", "ignored Item", "ignored context", "rewritten Unit", "rewritten profile", "removed original Unit counter", "removed original Asset counter"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				switch failure {
				case "ignored Unit":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent BEFORE UPDATE ON item_units BEGIN SELECT RAISE(IGNORE); END")
				case "ignored profile":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent BEFORE UPDATE ON asset_profiles BEGIN SELECT RAISE(IGNORE); END")
				case "ignored Item":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent BEFORE UPDATE ON items BEGIN SELECT RAISE(IGNORE); END")
				case "ignored context":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent BEFORE INSERT ON asset_profile_changes BEGIN SELECT RAISE(IGNORE); END")
				case "rewritten Unit":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent AFTER UPDATE ON item_units BEGIN UPDATE item_units SET notes='rewritten' WHERE id=NEW.id; END")
				case "rewritten profile":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent AFTER UPDATE ON asset_profiles BEGIN UPDATE asset_profiles SET retired_at='2026-11-08T00:00:00Z' WHERE item_unit_id=NEW.item_unit_id; END")
				case "removed original Unit counter":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent AFTER UPDATE ON asset_profiles BEGIN DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=NEW.item_unit_id; DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=NEW.item_unit_id; END")
				case "removed original Asset counter":
					// A future trigger must not turn an already reviewed identity into a
					// revision-one creation merely because its real counter disappeared.
					if kind == "retire" {
						execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent AFTER UPDATE ON asset_profiles WHEN NEW.retired_at IS NOT OLD.retired_at BEGIN DELETE FROM current_projections WHERE entity_type='asset' AND entity_id=NEW.item_unit_id; DELETE FROM projection_revisions WHERE entity_type='asset' AND entity_id=NEW.item_unit_id; END")
					} else {
						execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_intent AFTER INSERT ON asset_disposals BEGIN DELETE FROM current_projections WHERE entity_type='asset' AND entity_id=NEW.item_unit_id; DELETE FROM projection_revisions WHERE entity_type='asset' AND entity_id=NEW.item_unit_id; END")
					}
				}
				before := assetProfileState(t, s)
				if value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-no-incomplete-intent", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); !errors.Is(err, ErrDomainState) || !zeroAssetEvent(value) {
					t.Fatal("ignored/rewritten source or reseeded original authority became lifecycle success", value, err)
				}
				requireAssetEventState(t, s, before)
			})
		}
	}
}

func TestAssetLifecycleNewDisposalCounterAndAggregateLimitsNeverPartiallyPublish(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		for _, failure := range []string{"Asset maximum", "Unit maximum", "Item maximum", "catalog maximum", "sequence maximum", "missing Asset", "missing Unit", "missing Item", "counter-only Disposal"} {
			if failure == "counter-only Disposal" && kind == "retire" {
				continue
			}
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				assetProof, unitProof, want := asset.Revision, fixture.unit.Revision, ErrRevisionOverflow
				switch failure {
				case "Asset maximum":
					setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, maxSafeInteger)
					assetProof = maxSafeInteger
				case "Unit maximum":
					setCatalogProjectionRevision(t, s, "unit", fixture.unit.ID, maxSafeInteger)
					unitProof = maxSafeInteger
				case "Item maximum":
					setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, maxSafeInteger)
				case "catalog maximum":
					execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
				case "sequence maximum":
					execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
				case "counter-only Disposal":
					execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,4)", fixture.unit.ID)
					want = ErrDomainState
				default:
					entity, id := "asset", fixture.unit.ID
					if failure == "missing Unit" {
						entity = "unit"
					} else if failure == "missing Item" {
						entity, id = "item", fixture.individual.ID
					}
					execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", entity, id)
					execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", entity, id)
					want = ErrDomainState
				}
				before := assetProfileState(t, s)
				if value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-limited-authority", fixture.unit.ID, assetProof, unitProof, kind); !errors.Is(err, want) || !zeroAssetEvent(value) {
					t.Fatal("unsafe/missing counter or old disposal identity returned partial lifecycle success", value, err)
				}
				requireAssetEventState(t, s, before)
			})
		}
	}
}

func TestAssetLifecycleFinalAuthorizationCancellationAndDeferredCommitWithholdEveryEffect(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		for _, failure := range []string{"mandatory", "revoked", "epoch", "canceled", "late expiry", "deferred commit", "missing audit"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				ctx := t.Context()
				want := ErrAuthentication
				switch failure {
				case "mandatory":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_final AFTER INSERT ON mutation_events BEGIN UPDATE admin_accounts SET must_change_password=1; END")
					want = ErrPasswordChangeRequired
				case "revoked":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_final AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
				case "epoch":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_final AFTER INSERT ON mutation_events BEGIN UPDATE sync_state SET dataset_epoch='changed-during-lifecycle'; END")
				case "deferred commit":
					execSQL(t, s.db, "CREATE TABLE lifecycle_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_final AFTER UPDATE ON sessions BEGIN INSERT INTO lifecycle_commit_guard VALUES('missing'); END")
					want = nil
				case "missing audit":
					execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_final BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END")
					want = ErrDomainState
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
				if value, err := recordAssetLifecycle(ctx, assetReader(t, fixture), fixture.token, "lifecycle-final-failure", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); err == nil || want != nil && !errors.Is(err, want) || !zeroAssetEvent(value) {
					t.Fatal("late authorization/audit/COMMIT failure returned physical or financial success", value, err)
				}
				requireAssetEventState(t, s, before)
				if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM lifecycle_commit_guard") != 0 {
					t.Fatal("failed lifecycle COMMIT retained deferred rows")
				}
			})
		}
	}
}

func TestAssetLifecycleFailedDisposalReadIsNotAbsenceAndFTSFailureCannotLeakState(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind+"/disposal read", func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			before := assetProfileState(t, s)
			execSQL(t, s.db, "ALTER TABLE asset_disposals RENAME TO actual_disposals")
			execSQL(t, s.db, `CREATE VIEW asset_disposals AS SELECT json_extract('invalid-json','$') AS item_unit_id,'' AS kind,'' AS disposed_at,0 AS realized_proceeds_minor,'JPY' AS currency,'' AS note,'legacy' AS created_at,NULL AS mutation_event_id`)
			if value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-failed-absence-read", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); err == nil || errors.Is(err, ErrNotFound) || !zeroAssetEvent(value) {
				t.Fatal("failed disposal source query became reviewed absence", value, err)
			}
			execSQL(t, s.db, "DROP VIEW asset_disposals")
			execSQL(t, s.db, "ALTER TABLE actual_disposals RENAME TO asset_disposals")
			requireAssetEventState(t, s, before)
		})
		t.Run(kind+"/FTS publication", func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			execSQL(t, s.db, "DROP TABLE unit_search_fts")
			before := assetProfileState(t, s)
			if value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-failed-approved-search", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); err == nil || !zeroAssetEvent(value) {
				t.Fatal("failed actual Unit-only search publication leaked a terminal/source result", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetLifecycleEqualDecisionStillRequiresItsExactContext(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			original, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-before-noop-context", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind)
			if err != nil {
				t.Fatal(err)
			}
			current, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
			if err != nil {
				t.Fatal(err)
			}
			unit, err := loadUnit(t.Context(), s.db, fixture.unit.ID)
			if err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, "CREATE TRIGGER fail_lifecycle_context BEFORE INSERT ON asset_profile_changes BEGIN SELECT RAISE(IGNORE); END")
			before := assetProfileState(t, s)
			if value, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-ignored-noop-context", unit.ID, current.Revision, unit.Revision, kind); !errors.Is(err, ErrDomainState) || !zeroAssetEvent(value) {
				t.Fatal("equal lifecycle omitted exact context but acknowledged financial intent", value, err)
			}
			requireAssetEventState(t, s, before)
			execSQL(t, s.db, "DROP TRIGGER fail_lifecycle_context")
			if value, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-ignored-noop-context", unit.ID, current.Revision, unit.Revision, kind); err != nil || !reflect.DeepEqual(value, original) {
				t.Fatal("explicit retry after context rollback lost its original current decision", value, err)
			}
		})
	}
}

func TestAssetLifecycleRewrittenDisposalCannotFreezeAnUnrequestedDecision(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	execSQL(t, s.db, "DROP TRIGGER asset_disposals_no_update")
	execSQL(t, s.db, "CREATE TRIGGER rewrite_disposal AFTER INSERT ON asset_disposals BEGIN UPDATE asset_disposals SET note='rewritten' WHERE item_unit_id=NEW.item_unit_id; END")
	before := assetProfileState(t, s)
	if value, err := assetReader(t, fixture).Dispose(t.Context(), fixture.token, "lifecycle-rewritten-final-disposal", fixture.unit.ID, asset.Revision, disposalInput(fixture.unit.Revision, 0)); !errors.Is(err, ErrDomainState) || value != (Disposal{}) {
		t.Fatal("rewritten valid source became an unrequested frozen disposal", value, err)
	}
	requireAssetEventState(t, s, before)
}

func TestAssetLifecycleContextEncodingFailureCannotInsertAnAuditRecord(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	before := assetProfileState(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mutation := &mutationTx{ctx: t.Context(), tx: tx}
	if err = mutation.recordAssetLifecycleChange(assetLifecycleContext{Input: func() {}}); err == nil {
		t.Fatal("unserializable private context became a successful audit insert")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	requireAssetEventState(t, s, before)
}

func TestAssetLifecycleLateAuthorityAndFinalDisposalCorruptionCannotPassSourceChecks(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		failures := []string{"late Asset disappearance"}
		if kind == "dispose" {
			failures = append(failures, "Unit counter reseeded during insertion", "invalid final Disposal")
		}
		for _, failure := range failures {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				if failure == "Unit counter reseeded during insertion" {
					execSQL(t, s.db, "CREATE TRIGGER corrupt_lifecycle_late AFTER INSERT ON asset_disposals BEGIN DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=NEW.item_unit_id; DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=NEW.item_unit_id; END")
				} else {
					condition := "AFTER UPDATE ON items WHEN EXISTS(SELECT 1 FROM asset_profiles p JOIN item_units u ON u.id=p.item_unit_id WHERE u.item_id=NEW.id AND p.updated_at=NEW.updated_at)"
					if failure == "invalid final Disposal" {
						execSQL(t, s.db, "DROP TRIGGER asset_disposals_no_update")
						execSQL(t, s.db, "CREATE TRIGGER corrupt_lifecycle_late "+condition+" BEGIN UPDATE asset_disposals SET disposed_at='invalid-final-disposal'; END")
					} else {
						execSQL(t, s.db, "CREATE TRIGGER corrupt_lifecycle_late "+condition+" BEGIN DELETE FROM current_projections WHERE entity_type='asset' AND entity_id IN(SELECT id FROM item_units WHERE item_id=NEW.id); DELETE FROM projection_revisions WHERE entity_type='asset' AND entity_id IN(SELECT id FROM item_units WHERE item_id=NEW.id); END")
					}
				}
				before := assetProfileState(t, s)
				if value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-final-source-integrity", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); !errors.Is(err, ErrDomainState) || !zeroAssetEvent(value) {
					t.Fatal("late authority disappearance/reseed or corrupt final source returned lifecycle success", value, err)
				}
				requireAssetEventState(t, s, before)
			})
		}
	}
}
