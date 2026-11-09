package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDisposalCorrectionHistoryCompletesMoreThanOneHundredActualDecisionsWithIndependentSignedCursors(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	const count = 105
	entries := make(map[string]DisposalCorrection, count)
	for index := range count {
		input := disposalCorrectionInput(asset.Revision+int64(index), int64(index))
		if index%3 == 1 {
			input.Action, input.Replacement = "void", nil
		}
		entry, err := service.CorrectDisposal(t.Context(), fixture.token, fmt.Sprintf("actual-disposal-history-%03d", index), fixture.unit.ID, record.Revision, input)
		if err != nil {
			t.Fatal("complete actual correction chain failed", index, err)
		}
		entries[entry.ID], record = entry, entry.After
	}
	before := disposalCorrectionState(t, fixture.store)
	seen, cursor, previous := make(map[string]bool), "", ""
	pages := 0
	for {
		page, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, cursor, 50)
		if err != nil || page.Data == nil || len(page.Data) > 50 {
			t.Fatal("history did not return its complete actual bounded live page", err)
		}
		pages++
		for _, value := range page.Data {
			if value.ID <= previous || seen[value.ID] || !reflect.DeepEqual(value, entries[value.ID]) {
				t.Fatal("stable-ID paging omitted, reordered, repeated or rewrote an immutable decision")
			}
			seen[value.ID], previous = true, value.ID
		}
		if page.NextCursor == nil {
			break
		}
		if len(page.Data) != 50 || len(*page.NextCursor) > 2048 {
			t.Fatal("history truncated a nonterminal page or exceeded cursor bounds")
		}
		cursor = *page.NextCursor
	}
	if len(seen) != count || pages != 3 {
		t.Fatal("history silently stopped at the first hundred", len(seen), pages)
	}
	first, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, "", 1)
	if err != nil || first.NextCursor == nil {
		t.Fatal(err)
	}
	for _, token := range []string{"x" + *first.NextCursor, "bad.cursor", *first.NextCursor + ".extra"} {
		if value, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, token, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("modified/ambiguous cursor entered history", err)
		}
	}
	if value, err := service.PageDisposalCorrections(t.Context(), fixture.token, "another-owner", *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("continuation crossed its original owner", err)
	}
	if value, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, *first.NextCursor, 2); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("changed size reused a signed continuation", err)
	}
	if value, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("correction continuation crossed its collection purpose", err)
	}
	purpose := "disposal-corrections-list-cursor-v1\x00" + projectionPair("disposal-corrections", fixture.unit.ID)
	for _, scope := range []string{"principal", "epoch"} {
		session, err := fixture.auth.loadSession(t.Context(), fixture.store.db, fixture.token, fixture.auth.now())
		if err != nil {
			t.Fatal(err)
		}
		now := fixture.auth.now()
		value := metadataCursor{AdminID: session.Admin.ID, Epoch: scalar[string](t, fixture.store.db, "SELECT dataset_epoch FROM sync_state"), AfterID: first.Data[0].ID, Limit: 1, Issued: now.Unix(), Expires: now.Add(metadataCursorLifetime).Unix()}
		if scope == "principal" {
			value.AdminID = "another-administrator"
		} else {
			value.Epoch = "another-dataset-epoch"
		}
		foreign, err := encodeMetadataCursor(fixture.auth, purpose, value)
		if err != nil {
			t.Fatal(err)
		}
		if page, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, foreign, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(page).IsZero() {
			t.Fatal("continuation crossed principal/epoch authority", scope, err)
		}
	}
	requireDisposalCorrectionState(t, fixture.store, before)
	now := fixture.auth.now().Add(metadataCursorLifetime)
	fixture.auth.now = func() time.Time { return now }
	if page, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(page).IsZero() {
		t.Fatal("expired continuation extended history authority", err)
	}
}

func TestDisposalCorrectionReadsRejectMissingOrCorruptSourceWithoutResurrectingOriginal(t *testing.T) {
	for _, defect := range []string{"missing current", "missing replacement", "missing zero", "missing original", "missing Asset null", "missing previous", "unknown field", "duplicate key", "repaired Unicode", "native reason", "native subject", "native previous", "wrong before", "wrong original", "wrong currency", "missing first", "missing original source", "unadvanced Asset", "unadvanced Item", "missing Disposal counter", "missing Asset counter", "missing Unit counter", "missing Item counter"} {
		t.Run(defect, func(t *testing.T) {
			fixture, service, asset, record := disposalCommands(t)
			s := fixture.store
			input := disposalCorrectionInput(asset.Revision, record.Current.RealizedProceedsMinor)
			input.Replacement.Note = record.Current.Note
			first, err := service.CorrectDisposal(t.Context(), fixture.token, "first-equal-financial-decision", fixture.unit.ID, record.Revision, input)
			if err != nil {
				t.Fatal(err)
			}
			input.AssetRevision++
			second, err := service.CorrectDisposal(t.Context(), fixture.token, "second-equal-financial-decision", fixture.unit.ID, first.After.Revision, input)
			if err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, "DROP TRIGGER asset_disposal_corrections_no_update")
			switch defect {
			case "missing current", "missing replacement", "missing zero", "missing original", "missing Asset null", "missing previous":
				path := map[string]string{"missing current": "$.after.current", "missing replacement": "$.input.replacement", "missing zero": "$.input.replacement.realized_proceeds_minor", "missing original": "$.before.original", "missing Asset null": "$.asset.in_service_at", "missing previous": "$.previous_id"}[defect]
				execSQL(t, s.db, "UPDATE asset_disposal_corrections SET context_json=json_remove(context_json,?) WHERE mutation_event_id=?", path, first.ID)
			case "unknown field":
				execSQL(t, s.db, "UPDATE asset_disposal_corrections SET context_json=json_set(context_json,'$.unexpected',1) WHERE mutation_event_id=?", first.ID)
			case "duplicate key":
				execSQL(t, s.db, `UPDATE asset_disposal_corrections SET context_json=substr(context_json,1,length(context_json)-1)||',"previous_id":null}' WHERE mutation_event_id=?`, first.ID)
			case "repaired Unicode":
				execSQL(t, s.db, `UPDATE asset_disposal_corrections SET context_json=replace(context_json,'Exact reviewed financial reason','\ud800') WHERE mutation_event_id=?`, first.ID)
			case "native reason":
				execSQL(t, s.db, "UPDATE asset_disposal_corrections SET reason='Different exact reason' WHERE mutation_event_id=?", first.ID)
			case "native subject":
				execSQL(t, s.db, "UPDATE asset_disposal_corrections SET subject_revision=100 WHERE mutation_event_id=?", first.ID)
			case "native previous":
				execSQL(t, s.db, "DROP INDEX idx_disposal_corrections_root")
				execSQL(t, s.db, "UPDATE asset_disposal_corrections SET previous_correction_id=NULL WHERE mutation_event_id=?", second.ID)
			case "wrong before":
				execSQL(t, s.db, "UPDATE asset_disposal_corrections SET context_json=json_set(context_json,'$.before.current.note','Disconnected predecessor') WHERE mutation_event_id=?", second.ID)
			case "wrong original":
				execSQL(t, s.db, "UPDATE asset_disposal_corrections SET context_json=json_set(context_json,'$.before.original.note','Invented original','$.after.original.note','Invented original') WHERE mutation_event_id=?", first.ID)
			case "wrong currency":
				execSQL(t, s.db, "UPDATE asset_disposal_corrections SET context_json=json_set(context_json,'$.after.current.currency','USD','$.input.replacement.currency','USD') WHERE mutation_event_id=?", first.ID)
			case "missing first", "missing original source":
				table, predicate, id := "asset_disposal_corrections", "mutation_event_id", first.ID
				if defect == "missing original source" {
					table, predicate, id = "asset_disposals", "item_unit_id", fixture.unit.ID
				}
				execSQL(t, s.db, "DROP TRIGGER "+table+"_no_delete")
				execSQL(t, s.db, "PRAGMA foreign_keys=OFF")
				execSQL(t, s.db, "DELETE FROM "+table+" WHERE "+predicate+"=?", id)
				execSQL(t, s.db, "PRAGMA foreign_keys=ON")
			case "unadvanced Asset":
				setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, input.AssetRevision)
			case "unadvanced Item":
				setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, fixture.individual.Revision+1)
			default:
				kind, id := "disposal", fixture.unit.ID
				if defect == "missing Asset counter" {
					kind = "asset"
				} else if defect == "missing Unit counter" {
					kind = "unit"
				} else if defect == "missing Item counter" {
					kind, id = "item", fixture.individual.ID
				}
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", kind, id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, id)
			}
			before := disposalCorrectionState(t, s)
			for _, read := range []func() (any, error){
				func() (any, error) { return service.GetDisposalRecord(t.Context(), fixture.token, fixture.unit.ID) },
				func() (any, error) { return service.GetDisposal(t.Context(), fixture.token, fixture.unit.ID) },
				func() (any, error) {
					return service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, "", 1)
				},
				func() (any, error) { return service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "") },
				func() (any, error) {
					return readDomain(t.Context(), fixture.auth, fixture.token, func(tx *sql.Tx) ([]byte, error) {
						m := mutationTx{ctx: t.Context(), tx: tx}
						return m.projectionPayload(projectionKey{"disposal", fixture.unit.ID})
					})
				},
			} {
				if value, err := read(); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("missing/corrupt source exposed a prefix or resurrected original financial effect", defect, err)
				}
				requireDisposalCorrectionState(t, s, before)
			}
		})
	}
}

func TestDisposalCorrectionRetainedAndHistoryReadsAdmissionAbsenceAndEmptyPages(t *testing.T) {
	fixture, service, _, record := disposalCommands(t)
	before := disposalCorrectionState(t, fixture.store)
	page, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || page.Data == nil || len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("actual empty correction history was not a terminal complete page", err)
	}
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if value, err := service.GetDisposalRecord(t.Context(), fixture.token, id); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid retained subject identity entered source", err)
		}
		if value, err := service.PageDisposalCorrections(t.Context(), fixture.token, id, "", 50); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid owned history identity entered source", err)
		}
	}
	for _, id := range []string{"missing", "new", "tree", "\x00"} {
		if value, err := service.GetDisposalRecord(t.Context(), fixture.token, id); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("absent source was synthesized or valid imported spelling reserved", err)
		}
		if value, err := service.PageDisposalCorrections(t.Context(), fixture.token, id, "", 50); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("missing original disposal became a fabricated empty history", err)
		}
	}
	for _, limit := range []int{0, -1, 101} {
		if value, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, "", limit); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("history accepted an unbounded or repaired page size", err)
		}
	}
	if value, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, strings.Repeat("x", 2049), 50); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("history widened the existing single-ID cursor bound", err)
	}
	requireDisposalCorrectionState(t, fixture.store, before)
	execSQL(t, fixture.store.db, "UPDATE current_projections SET payload_json=' {\"NotFinancialAuthority\":true} ' WHERE entity_type IN('asset','disposal','unit','item')")
	if actual, err := service.GetDisposalRecord(t.Context(), fixture.token, fixture.unit.ID); err != nil || !reflect.DeepEqual(actual, record) {
		t.Fatal("retained record borrowed or repaired projection cache bytes", err)
	}
}

func TestDisposalCorrectionReadQuerySigningFinalAuthorizationAndCommitFailuresWithholdEveryResult(t *testing.T) {
	for _, failure := range []string{"query", "signing", "session write", "ignored session write", "deferred commit", "late expiry"} {
		t.Run(failure, func(t *testing.T) {
			fixture, service, asset, record := disposalCommands(t)
			s := fixture.store
			for index := range 2 {
				entry, err := service.CorrectDisposal(t.Context(), fixture.token, fmt.Sprintf("read-failure-history-%d", index), fixture.unit.ID, record.Revision, disposalCorrectionInput(asset.Revision+int64(index), 0))
				if err != nil {
					t.Fatal(err)
				}
				record = entry.After
			}
			before := disposalCorrectionState(t, s)
			switch failure {
			case "query":
				execSQL(t, s.db, "ALTER TABLE asset_disposal_corrections RENAME TO unavailable_disposal_corrections")
				defer func() {
					execSQL(t, s.db, "ALTER TABLE unavailable_disposal_corrections RENAME TO asset_disposal_corrections")
					requireDisposalCorrectionState(t, s, before)
				}()
			case "signing":
				fixture.auth.serverSecret = nil
			case "session write":
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'disposal read failure'); END")
			case "ignored session write":
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(IGNORE); END")
			case "deferred commit":
				execSQL(t, s.db, "CREATE TABLE disposal_read_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_disposal_read AFTER UPDATE ON sessions BEGIN INSERT INTO disposal_read_commit_guard VALUES('missing'); END")
			}
			base, calls := fixture.auth.now(), 0
			if failure == "late expiry" {
				fixture.auth.now = func() time.Time {
					calls++
					if calls > 2 {
						return base.Add(31 * 24 * time.Hour)
					}
					return base
				}
			}
			if value, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, "", 1); err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("query/signing/final activity/COMMIT failure exposed an owned page prefix", err)
			}
			if failure != "query" {
				requireDisposalCorrectionState(t, s, before)
			}
			if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM disposal_read_commit_guard") != 0 {
				t.Fatal("failed read COMMIT retained private rows")
			}
			if failure != "signing" && failure != "late expiry" {
				if value, err := service.GetDisposalRecord(context.Background(), fixture.token, fixture.unit.ID); err == nil || !reflect.ValueOf(value).IsZero() {
					t.Fatal("failed retained read exposed an independently usable proof", err)
				}
			}
		})
	}
}
