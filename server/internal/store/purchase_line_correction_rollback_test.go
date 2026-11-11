package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPurchaseLineCorrectionRollsBackEveryActualFinancialSearchPublicationAndReceiptStatement(t *testing.T) {
	for _, action := range []string{"void", "replace"} {
		for _, failure := range []struct{ table, verb string }{
			{"purchase_line_corrections", "INSERT"}, {"asset_profiles", "UPDATE"}, {"items", "UPDATE"},
			{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
			{"current_projections", "INSERT"}, {"current_projections", "UPDATE"}, {"current_projections", "DELETE"},
			{"sync_changes", "INSERT"}, {"search_documents", "INSERT"}, {"search_documents", "UPDATE"},
			{"unit_search_documents", "INSERT"}, {"unit_search_documents", "UPDATE"},
			{"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
			{"purchase_lines", "INSERT"},
		} {
			if action == "void" && failure.table == "purchase_lines" {
				continue
			}
			for _, mode := range []string{"ABORT,'failed allocation correction'", "IGNORE"} {
				t.Run(action+"/"+failure.table+failure.verb+"/"+mode, func(t *testing.T) {
					t.Parallel()
					fixture, purchases, _, _ := purchaseAssetFixture(t)
					s := fixture.store
					taxonomy, err := NewTaxonomy(fixture.auth)
					if err != nil {
						t.Fatal(err)
					}
					publishCatalogSearch(t, taxonomy, fixture.token, "baseline-correction-owned-search", fixture.individual.ID)
					publishCatalogSearch(t, taxonomy, fixture.token, "baseline-correction-target-search", fixture.quantity.ID)
					input := reviewedLineVoid(t, fixture, fixture.unitLine)
					if action == "replace" {
						input.Action = action
						input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1500, PaidMinor: 13, Note: "UncommittedCorrectionEvidence"}
					}
					now := fixture.auth.now().Add(time.Minute)
					fixture.auth.now = func() time.Time { return now }
					execSQL(t, s.db, "CREATE TRIGGER fail_line_correction BEFORE "+failure.verb+" ON "+failure.table+" BEGIN SELECT RAISE("+mode+"); END")
					before := assetProfileState(t, s)
					value, err := purchases.CorrectLine(t.Context(), fixture.token, "failed-original-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input)
					if err == nil || !reflect.ValueOf(value).IsZero() {
						t.Fatal("failed/ignored mandatory statement froze a successful financial correction", err)
					}
					requireAssetProfileState(t, s, before)
					if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'UncommittedCorrectionEvidence'") != 0 ||
						scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'OriginalUnitLine'") != 1 {
						t.Fatal("failed correction leaked new FTS or removed original approved evidence")
					}
					execSQL(t, s.db, "DROP TRIGGER fail_line_correction")
					if _, err = purchases.CorrectLine(t.Context(), fixture.token, "failed-original-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
						t.Fatal("rolled-back original key/proofs/connection could not be explicitly retried", err)
					}
				})
			}
		}
	}
}

func TestPurchaseLineCorrectionWithholdsLateAuthorizationCancellationAndDeferredCommit(t *testing.T) {
	for _, action := range []string{"void", "replace"} {
		for _, failure := range []string{"mandatory", "revoked", "epoch", "canceled", "late expiry", "deferred commit"} {
			t.Run(action+"/"+failure, func(t *testing.T) {
				fixture, purchases, _, _ := purchaseAssetFixture(t)
				s, ctx, want := fixture.store, t.Context(), ErrAuthentication
				input := reviewedLineVoid(t, fixture, fixture.unitLine)
				if action == "replace" {
					input.Action = action
					input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1500, PaidMinor: 13}
				}
				switch failure {
				case "mandatory":
					execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_final AFTER INSERT ON mutation_events BEGIN UPDATE admin_accounts SET must_change_password=1; END")
					want = ErrPasswordChangeRequired
				case "revoked":
					execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_final AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
				case "epoch":
					execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_final AFTER INSERT ON mutation_events BEGIN UPDATE sync_state SET dataset_epoch='changed-during-correction'; END")
				case "deferred commit":
					execSQL(t, s.db, "CREATE TABLE line_correction_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
					execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_final AFTER UPDATE ON sessions BEGIN INSERT INTO line_correction_commit_guard VALUES('missing'); END")
					want = nil
				case "canceled", "late expiry":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					base, calls := fixture.auth.now(), 0
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
				if value, err := purchases.CorrectLine(ctx, fixture.token, "late-failed-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input); err == nil || want != nil && !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("late authorization/COMMIT failure released partial correction", err)
				}
				requireAssetProfileState(t, s, before)
				if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM line_correction_commit_guard") != 0 {
					t.Fatal("failed COMMIT retained a deferred guard row")
				}
			})
		}
	}
}

func TestPurchaseLineCorrectionRejectsRewrittenSourceContextAndReusedIndependentIdentity(t *testing.T) {
	for _, failure := range []string{"rewritten context", "rewritten reason", "rewritten replacement", "reseeded Asset", "rewritten Item", "missing final Unit", "occupied replacement identity"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			s := fixture.store
			setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, 13)
			switch failure {
			case "rewritten context", "rewritten reason":
				execSQL(t, s.db, "DROP TRIGGER purchase_line_corrections_no_update")
				column, expression := "context_json", "json_set(NEW.context_json,'$.input.reason','rewritten')"
				if failure == "rewritten reason" {
					column, expression = "reason", "'rewritten'"
				}
				execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_source AFTER INSERT ON purchase_line_corrections BEGIN UPDATE purchase_line_corrections SET "+column+"="+expression+" WHERE mutation_event_id=NEW.mutation_event_id; END")
			case "rewritten replacement":
				execSQL(t, s.db, "DROP TRIGGER purchase_lines_no_update")
				execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_source AFTER INSERT ON purchase_lines BEGIN UPDATE purchase_lines SET paid_minor=999 WHERE id=NEW.id; END")
			case "reseeded Asset":
				execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_source AFTER INSERT ON purchase_line_corrections BEGIN DELETE FROM current_projections WHERE entity_type='asset'; DELETE FROM projection_revisions WHERE entity_type='asset'; INSERT INTO projection_revisions VALUES('asset','"+fixture.unit.ID+"',1); END")
			case "rewritten Item":
				execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_source AFTER INSERT ON purchase_line_corrections BEGIN UPDATE items SET notes='rewritten' WHERE id='"+fixture.individual.ID+"'; END")
			case "missing final Unit":
				execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_source AFTER UPDATE ON asset_profiles BEGIN DELETE FROM current_projections WHERE entity_type='unit'; DELETE FROM projection_revisions WHERE entity_type='unit'; END")
			case "occupied replacement identity":
				execSQL(t, s.db, "CREATE TRIGGER fail_line_correction_source BEFORE INSERT ON purchase_lines BEGIN INSERT INTO projection_revisions VALUES('purchase_line',NEW.id,7); END")
			}
			input := reviewedLineVoid(t, fixture, fixture.unitLine)
			input.Action = "replace"
			input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1500, PaidMinor: 13}
			before := assetProfileState(t, s)
			if value, err := purchases.CorrectLine(t.Context(), fixture.token, "rewritten-original-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("rewritten/missing/reseeded financial source froze a successful correction", err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestPurchaseLineCorrectionWithoutAssetStillRequiresItsActualItemTimestampWrite(t *testing.T) {
	for _, advance := range []time.Duration{0, time.Minute} {
		t.Run(advance.String(), func(t *testing.T) {
			fixture, purchases := purchaseCommands(t)
			s := fixture.store
			input := reviewedLineVoid(t, fixture, fixture.stockLine)
			now := fixture.auth.now().Add(advance)
			fixture.auth.now = func() time.Time { return now }
			execSQL(t, s.db, "CREATE TRIGGER ignore_correction_item BEFORE UPDATE ON items BEGIN SELECT RAISE(IGNORE); END")
			before := purchaseCommandState(t, s)
			if value, err := purchases.CorrectLine(t.Context(), fixture.token, "unbacked-required-item-write", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("ignored unbacked aggregate timestamp froze financial success", err)
			}
			requirePurchaseCommandState(t, s, before)
			execSQL(t, s.db, "DROP TRIGGER ignore_correction_item")
			if _, err := purchases.CorrectLine(t.Context(), fixture.token, "unbacked-required-item-write", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil || scalar[string](t, s.db, "SELECT updated_at FROM items WHERE id=?", fixture.quantity.ID) != stamp(now) {
				t.Fatal("actual equal/new timestamp or explicit original retry failed", err)
			}
		})
	}
}

func TestPurchaseLineCorrectionDoesNotRequireAnExistingDisposableCurrentRowToPublishTombstone(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.stockLine)
	execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='purchase_line' AND entity_id=?", fixture.stockLine.ID)
	changed := requireProjectionReplay(t, fixture.store, func() {
		if _, err := purchases.CorrectLine(t.Context(), fixture.token, "void-without-disposable-current", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
			t.Fatal("legitimately absent cache tombstone deletion required one deleted row", err)
		}
	})
	if !changed[projectionKey{"purchase_line", fixture.stockLine.ID}] || scalar[int64](t, fixture.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='purchase_line' AND entity_id=?", fixture.stockLine.ID) != fixture.stockLine.Revision+1 {
		t.Fatal("missing cache erased authoritative identity/tombstone history")
	}
}
