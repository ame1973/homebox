package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDisposalCorrectionEveryActualStatementFailureRollsBackAllAuthorityAndAllowsOriginalRetry(t *testing.T) {
	for _, action := range []string{"replace", "void"} {
		for _, failure := range []struct{ table, verb string }{
			{"asset_disposal_corrections", "INSERT"}, {"asset_profiles", "UPDATE"}, {"items", "UPDATE"},
			{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
			{"current_projections", "INSERT"}, {"current_projections", "UPDATE"},
			{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
		} {
			t.Run(action+"/"+failure.table+"/"+failure.verb, func(t *testing.T) {
				fixture, service, asset, record := disposalCommands(t)
				s := fixture.store
				input := disposalCorrectionInput(asset.Revision, 0)
				if action == "void" {
					input.Action, input.Replacement = "void", nil
				}
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction BEFORE "+failure.verb+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'actual disposal correction failure'); END")
				before := disposalCorrectionState(t, s)
				if value, err := service.CorrectDisposal(t.Context(), fixture.token, "original-failed-disposal-decision", fixture.unit.ID, record.Revision, input); err == nil || !reflect.ValueOf(value).IsZero() {
					t.Fatal("failed actual source/publication/audit/receipt/activity statement exposed partial success", err)
				}
				requireDisposalCorrectionState(t, s, before)
				execSQL(t, s.db, "DROP TRIGGER fail_disposal_correction")
				if _, err := service.CorrectDisposal(t.Context(), fixture.token, "original-failed-disposal-decision", fixture.unit.ID, record.Revision, input); err != nil {
					t.Fatal("rolled-back original key/proofs or pooled connection could not be explicitly retried", err)
				}
			})
		}
	}
}

func TestDisposalCorrectionIgnoredOrRewrittenSourceContextAndFinalFieldsNeverFreezeSuccess(t *testing.T) {
	for _, failure := range []string{"ignored journal", "ignored profile timestamp", "ignored Item timestamp", "ignored current", "ignored outbox", "ignored receipt", "ignored audit", "ignored session", "rewritten context", "deleted context", "rewritten original", "late rewritten context", "late profile fields", "late Item fields", "late Unit fields", "reseeded disposal", "reseeded Asset", "reseeded Item"} {
		t.Run(failure, func(t *testing.T) {
			fixture, service, asset, record := disposalCommands(t)
			s := fixture.store
			table, verb := "asset_disposal_corrections", "INSERT"
			switch failure {
			case "ignored profile timestamp":
				table, verb = "asset_profiles", "UPDATE"
			case "ignored Item timestamp":
				table, verb = "items", "UPDATE"
			case "ignored current":
				table = "current_projections"
			case "ignored outbox":
				table = "sync_changes"
			case "ignored receipt":
				table = "api_idempotency"
			case "ignored audit":
				table = "mutation_events"
			case "ignored session":
				table, verb = "sessions", "UPDATE"
			}
			switch failure {
			case "rewritten context", "late rewritten context":
				execSQL(t, s.db, "DROP TRIGGER asset_disposal_corrections_no_update")
				boundary := "INSERT ON asset_disposal_corrections"
				if failure == "late rewritten context" {
					boundary = "UPDATE ON items"
				}
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction AFTER "+boundary+" BEGIN UPDATE asset_disposal_corrections SET context_json='{}'; END")
			case "deleted context":
				execSQL(t, s.db, "DROP TRIGGER asset_disposal_corrections_no_delete")
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction AFTER INSERT ON asset_disposal_corrections BEGIN DELETE FROM asset_disposal_corrections WHERE mutation_event_id=NEW.mutation_event_id; END")
			case "rewritten original":
				execSQL(t, s.db, "DROP TRIGGER asset_disposals_no_update")
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction AFTER INSERT ON asset_disposal_corrections BEGIN UPDATE asset_disposals SET note='Rewritten original'; END")
			case "late profile fields":
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction AFTER UPDATE ON items BEGIN UPDATE asset_profiles SET manual_acquisition_minor=101; END")
			case "late Item fields":
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction AFTER UPDATE ON items WHEN NEW.name<>'Rewritten owner' BEGIN UPDATE items SET name='Rewritten owner' WHERE id=NEW.id; END")
			case "late Unit fields":
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction AFTER UPDATE ON items BEGIN UPDATE item_units SET notes='Rewritten physical metadata'; END")
			case "reseeded disposal", "reseeded Asset", "reseeded Item":
				kind, id := "disposal", fixture.unit.ID
				if failure == "reseeded Asset" {
					kind = "asset"
				} else if failure == "reseeded Item" {
					kind, id = "item", fixture.individual.ID
				}
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction AFTER INSERT ON asset_disposal_corrections BEGIN DELETE FROM current_projections WHERE entity_type='"+kind+"' AND entity_id='"+id+"'; DELETE FROM projection_revisions WHERE entity_type='"+kind+"' AND entity_id='"+id+"'; INSERT INTO projection_revisions VALUES('"+kind+"','"+id+"',1); END")
			default:
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_correction BEFORE "+verb+" ON "+table+" BEGIN SELECT RAISE(IGNORE); END")
			}
			before := disposalCorrectionState(t, s)
			if value, err := service.CorrectDisposal(t.Context(), fixture.token, "ignored-rewritten-financial-decision", fixture.unit.ID, record.Revision, disposalCorrectionInput(asset.Revision, 0)); err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("ignored/rewritten/reseeded source or private context froze financial success", failure, err)
			}
			requireDisposalCorrectionState(t, s, before)
		})
	}
}

func TestDisposalCorrectionCounterExhaustionAndMissingRealAuthorityNeverReseedOrPartiallyPublish(t *testing.T) {
	for _, failure := range []string{"Disposal maximum", "Asset maximum", "Item maximum", "catalog maximum", "sequence maximum", "missing Disposal counter", "missing Asset counter", "missing Unit counter", "missing Item counter"} {
		t.Run(failure, func(t *testing.T) {
			fixture, service, asset, record := disposalCommands(t)
			s := fixture.store
			input := disposalCorrectionInput(asset.Revision, 0)
			want := ErrRevisionOverflow
			switch failure {
			case "Disposal maximum":
				setCatalogProjectionRevision(t, s, "disposal", fixture.unit.ID, maxSafeInteger)
				record.Revision = maxSafeInteger
			case "Asset maximum":
				setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, maxSafeInteger)
				input.AssetRevision = maxSafeInteger
			case "Item maximum":
				setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, maxSafeInteger)
			case "catalog maximum":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence maximum":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			default:
				kind, id := "disposal", fixture.unit.ID
				if failure == "missing Asset counter" {
					kind = "asset"
				} else if failure == "missing Unit counter" {
					kind = "unit"
				} else if failure == "missing Item counter" {
					kind, id = "item", fixture.individual.ID
				}
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", kind, id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, id)
				want = ErrDomainState
			}
			before := disposalCorrectionState(t, s)
			if value, err := service.CorrectDisposal(t.Context(), fixture.token, "unsafe-real-financial-authority", fixture.unit.ID, record.Revision, input); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("missing/exhausted authority was reseeded or returned partial financial work", failure, err)
			}
			requireDisposalCorrectionState(t, s, before)
		})
	}
}

func TestDisposalCorrectionFinalAuthorizationCancellationAndDeferredCommitWithholdAllEffects(t *testing.T) {
	for _, failure := range []string{"mandatory", "revoked", "epoch", "canceled", "late expiry", "deferred commit"} {
		t.Run(failure, func(t *testing.T) {
			fixture, service, asset, record := disposalCommands(t)
			s := fixture.store
			ctx, want := t.Context(), ErrAuthentication
			switch failure {
			case "mandatory":
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_final AFTER INSERT ON mutation_events BEGIN UPDATE admin_accounts SET must_change_password=1; END")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_final AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
			case "epoch":
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_final AFTER INSERT ON mutation_events BEGIN UPDATE sync_state SET dataset_epoch='changed-during-disposal-correction'; END")
			case "deferred commit":
				execSQL(t, s.db, "CREATE TABLE disposal_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_final AFTER UPDATE ON sessions BEGIN INSERT INTO disposal_commit_guard VALUES('missing'); END")
				want = nil
			case "canceled", "late expiry":
				base, calls := fixture.auth.now(), 0
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
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
			before := disposalCorrectionState(t, s)
			if value, err := service.CorrectDisposal(ctx, fixture.token, "final-failed-disposal-correction", fixture.unit.ID, record.Revision, disposalCorrectionInput(asset.Revision, 0)); err == nil || want != nil && !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("late authorization/cancellation/COMMIT failure exposed partial finance", failure, err)
			}
			requireDisposalCorrectionState(t, s, before)
			if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM disposal_commit_guard") != 0 {
				t.Fatal("failed financial COMMIT leaked deferred private rows")
			}
		})
	}
}
