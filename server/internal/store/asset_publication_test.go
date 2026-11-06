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

// Private final-state publication probe, not a public financial/lifecycle service.
// Real services still need original proofs, basis/currency/usage eligibility,
// profile before-images and explicit reviewed lifecycle commands before exposure.
func mutateAssetProjectionFixture(ctx context.Context, fixture assetFixture, key, final string) (Asset, error) {
	request := struct{ UnitID, Final string }{fixture.unit.ID, final}
	return runDomainMutation(ctx, fixture.auth, fixture.token, key, "test_asset_projection", request, func(m *mutationTx) (Asset, error) {
		if _, err := loadAsset(ctx, m.tx, fixture.unit.ID); err != nil {
			return Asset{}, err
		}
		if _, err := loadUsage(ctx, m.tx, fixture.usage.ID); err != nil {
			return Asset{}, err
		}
		if _, err := loadCost(ctx, m.tx, fixture.cost.ID); err != nil {
			return Asset{}, err
		}
		_, err := loadDisposal(ctx, m.tx, fixture.unit.ID)
		newDisposal := errors.Is(err, ErrNotFound)
		if err != nil && !newDisposal {
			return Asset{}, err
		}
		usageID, err := newDomainID()
		if err != nil {
			return Asset{}, err
		}
		costID, err := newDomainID()
		if err != nil {
			return Asset{}, err
		}
		_, err = m.tx.ExecContext(ctx, `INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,note,correction_of,created_at,mutation_event_id)
 VALUES(?,?,1,'times','correction',?,?,?,?,?)`, usageID, fixture.unit.ID, stamp(m.now), final+"Usage", fixture.usage.ID, stamp(m.now), m.eventID)
		if err != nil {
			return Asset{}, err
		}
		_, err = m.tx.ExecContext(ctx, `INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,direction,currency,kind,occurred_at,note,correction_of,created_at,mutation_event_id)
 VALUES(?,?,1,'credit','JPY','adjustment',?,?,?,?,?)`, costID, fixture.unit.ID, stamp(m.now), final+"Cost", fixture.cost.ID, stamp(m.now), m.eventID)
		if err != nil {
			return Asset{}, err
		}
		if newDisposal {
			if _, err = m.tx.ExecContext(ctx, `INSERT INTO asset_disposals(item_unit_id,kind,disposed_at,realized_proceeds_minor,currency,note,created_at,mutation_event_id)
 VALUES(?,'sold',?,0,'JPY',?,?,?)`, fixture.unit.ID, stamp(m.now), final+"Disposal", stamp(m.now), m.eventID); err != nil {
				return Asset{}, err
			}
			if _, err = m.tx.ExecContext(ctx, "UPDATE item_units SET status='disposed',updated_at=? WHERE id=?", stamp(m.now), fixture.unit.ID); err != nil {
				return Asset{}, err
			}
		}
		for _, stage := range []struct {
			residual int64
			deleted  bool
		}{{1, true}, {int64(20000 + len(final)), false}} {
			if _, err = m.tx.ExecContext(ctx, "UPDATE asset_profiles SET estimated_residual_minor=?,updated_at=? WHERE item_unit_id=?", stage.residual, stamp(m.now), fixture.unit.ID); err != nil {
				return Asset{}, err
			}
			for _, row := range []projectionKey{{"asset", fixture.unit.ID}, {"usage", usageID}, {"cost", costID}} {
				revision, err := m.touch(row.kind, row.id, stage.deleted)
				if err != nil {
					return Asset{}, err
				}
				if row.kind != "asset" && revision != 1 {
					return Asset{}, ErrDomainState
				}
			}
			if newDisposal {
				if revision, err := m.touch("disposal", fixture.unit.ID, stage.deleted); err != nil || revision != 1 {
					if err == nil {
						err = ErrDomainState
					}
					return Asset{}, err
				}
				if _, err = m.touch("unit", fixture.unit.ID, false); err != nil {
					return Asset{}, err
				}
				if err = m.touchLocationPath(*fixture.unit.LocationID); err != nil {
					return Asset{}, err
				}
			}
			if _, err = m.touch("item", fixture.individual.ID, false); err != nil {
				return Asset{}, err
			}
			if _, err = m.tx.ExecContext(ctx, "UPDATE items SET updated_at=? WHERE id=?", stamp(m.now), fixture.individual.ID); err != nil {
				return Asset{}, err
			}
		}
		return loadAsset(ctx, m.tx, fixture.unit.ID)
	})
}

func TestAssetPublicationCoalescesFinalSourceIndependentHistoryAndFrozenOriginalReplies(t *testing.T) {
	fixture := assetsFixture(t, false)
	s := fixture.store
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"NeverCopyAssetCurrent\":true}' WHERE entity_type IN('asset','usage','cost')")
	execSQL(t, s.db, "INSERT INTO unit_search_documents(unit_id,title,reviewed_ai,revision,updated_at) VALUES(?,'UnverifiedAssetCache','NeverPublishAssetOCR',999,'2026')", fixture.unit.ID)
	before := domainState(t, s)
	var reply Asset
	changed := requireProjectionReplay(t, s, func() {
		var err error
		reply, err = mutateAssetProjectionFixture(t.Context(), fixture, "asset-final-source", "Final")
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.Revision != 8 || reply.EstimatedResidualMinor == nil || *reply.EstimatedResidualMinor != 20005 || reply.InServiceAt == nil || *reply.InServiceAt != *fixture.asset.InServiceAt || reply.RetiredAt != nil {
		t.Fatal("profile used intermediate/cache data or guessed retirement/service dates")
	}
	for _, key := range []projectionKey{{"asset", fixture.unit.ID}, {"disposal", fixture.unit.ID}, {"unit", fixture.unit.ID}, {"item", fixture.individual.ID}, {"location", "source"}, {"location", "root"}} {
		if !changed[key] {
			t.Fatal("explicit financial/physical dependency was not published", key)
		}
	}
	if len(changed) != 8 {
		t.Fatal("publisher repeated an identity or invented a dependency", len(changed))
	}
	for _, row := range []struct{ kind, table string }{{"usage", "asset_usage_events"}, {"cost", "asset_cost_events"}} {
		id := scalar[string](t, s.db, "SELECT id FROM "+row.table+" WHERE mutation_event_id IS NOT NULL")
		if !changed[projectionKey{row.kind, id}] || scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type=? AND entity_id=?", row.kind, id) != 1 {
			t.Fatal("independent new event proof became Asset-plus-one", row.kind)
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+row.table+" e JOIN mutation_events m ON m.id=e.mutation_event_id WHERE e.id=? AND e.created_at=m.occurred_at AND e.idempotency_key IS NULL AND m.operation='test_asset_projection'", id) != 1 {
			t.Fatal("event did not commit actual private audit/time or stored a raw caller key")
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+row.table+" WHERE correction_of=?", map[string]string{"usage": fixture.usage.ID, "cost": fixture.cost.ID}[row.kind]) != 1 {
			t.Fatal("actual correction reference was lost", row.kind)
		}
		payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, id)
		for _, private := range []string{"mutation_event_id", "created_at", "idempotency_key", "revision"} {
			if strings.Contains(payload, private) {
				t.Fatal("private producer metadata leaked into frozen financial payload")
			}
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_disposals d JOIN mutation_events m ON m.id=d.mutation_event_id WHERE d.created_at=m.occurred_at AND m.operation='test_asset_projection'") != 1 || scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='disposal' AND entity_id=?", fixture.unit.ID) != 1 {
		t.Fatal("disposal proof/audit was conflated with the Unit or Asset")
	}
	encoded, _ := json.Marshal(reply.AssetData)
	if scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='asset' AND entity_id=?", reply.ItemUnitID) != string(encoded) {
		t.Fatal("Asset payload did not come from exact final source")
	}
	for _, text := range []string{"NeverCopyAssetCurrent", "NeverPublishAssetOCR", "UnverifiedAssetCache"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE ?", "%"+text+"%") != 0 {
			t.Fatal("unverified cache entered financial publication", text)
		}
	}
	after := domainState(t, s)
	for _, table := range []string{"purchases", "purchase_lines", "stock_balances", "inventory_events", "search_documents", "unit_search_documents", "media", "media_links", "ai_jobs", "ai_review_entries", "ocr_results"} {
		if after[table] != before[table] {
			t.Fatal("private financial publication invented unrelated source/search/AI work", table)
		}
	}
	original := reply
	if _, err := mutateAssetProjectionFixture(t.Context(), fixture, "asset-later-source", "Later"); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, fixture.auth)
	fixture.token = fresh
	before = domainState(t, s)
	if frozen, err := mutateAssetProjectionFixture(t.Context(), fixture, "asset-final-source", "Final"); err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("fresh login could not reconcile the original frozen Asset", err)
	}
	requireDomainState(t, s, before)
	if value, err := mutateAssetProjectionFixture(t.Context(), fixture, "asset-final-source", "Changed"); !errors.Is(err, ErrIdempotencyConflict) || value != (Asset{}) {
		t.Fatal("changed financial input reused an original key", err)
	}
	requireDomainState(t, s, before)
}

func TestAssetPublicationRollsBackEverySourceProjectionReceiptAndSessionStatement(t *testing.T) {
	fixture := assetsFixture(t, false)
	s := fixture.store
	now := fixture.auth.now().Add(time.Minute)
	fixture.auth.now = func() time.Time { return now }
	for _, failure := range []struct{ table, event string }{
		{"asset_profiles", "UPDATE"}, {"asset_usage_events", "INSERT"}, {"asset_cost_events", "INSERT"}, {"asset_disposals", "INSERT"},
		{"item_units", "UPDATE"}, {"items", "UPDATE"},
		{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
		{"current_projections", "INSERT"}, {"current_projections", "UPDATE"},
		{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_asset_statement BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'Asset publication failure'); END")
			defer execSQL(t, s.db, "DROP TRIGGER fail_asset_statement")
			if value, err := mutateAssetProjectionFixture(t.Context(), fixture, "asset-failed-statement", "Final"); err == nil || value != (Asset{}) {
				t.Fatal("failed financial statement exposed partial success", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestAssetPublicationLateAuthorizationAndDeferredAuditCommitFailuresWithholdAllResults(t *testing.T) {
	for _, failure := range []string{"late revocation", "deferred commit", "missing event"} {
		t.Run(failure, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s := fixture.store
			if failure == "late revocation" {
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_late AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
			} else if failure == "deferred commit" {
				execSQL(t, s.db, "CREATE TABLE asset_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_late AFTER INSERT ON mutation_events BEGIN INSERT INTO asset_commit_guard VALUES('missing'); END")
			} else {
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_late BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END")
			}
			before := domainState(t, s)
			if value, err := mutateAssetProjectionFixture(t.Context(), fixture, "asset-failed-late", "Final"); err == nil || value != (Asset{}) || failure == "late revocation" && !errors.Is(err, ErrAuthentication) {
				t.Fatal("late authorization/deferred audit/COMMIT failure released financial result", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestAssetPublicationNeverReseedsMissingOriginalOrRelatedAuthority(t *testing.T) {
	for _, scope := range []string{"asset", "usage", "cost", "unit", "item"} {
		t.Run(scope, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s := fixture.store
			id := fixture.unit.ID
			switch scope {
			case "usage":
				id = fixture.usage.ID
			case "cost":
				id = fixture.cost.ID
			case "item":
				id = fixture.individual.ID
			}
			execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", scope, id)
			execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", scope, id)
			before := domainState(t, s)
			if value, err := mutateAssetProjectionFixture(t.Context(), fixture, "asset-missing-authority", "Final"); !errors.Is(err, ErrDomainState) || value != (Asset{}) {
				t.Fatal("missing original authority became a fictional revision one", scope, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestAssetPublicationCounterAndSequenceExhaustionRollBackEveryEffect(t *testing.T) {
	for _, scope := range []string{"asset", "unit", "item", "location", "catalog revision", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s := fixture.store
			id := fixture.unit.ID
			switch scope {
			case "catalog revision":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			default:
				if scope == "item" {
					id = fixture.individual.ID
				} else if scope == "location" {
					id = "source"
				}
				setCatalogProjectionRevision(t, s, scope, id, maxSafeInteger)
			}
			before := domainState(t, s)
			if value, err := mutateAssetProjectionFixture(t.Context(), fixture, "asset-exhausted-authority", "Final"); !errors.Is(err, ErrRevisionOverflow) || value != (Asset{}) {
				t.Fatal("exhausted counter/sequence exposed financial success", scope, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestAssetFinalMaterializationWithholdsMissingCorruptOrExhaustedIndependentSource(t *testing.T) {
	for _, kind := range []string{"asset", "usage", "cost", "disposal"} {
		for _, failure := range []string{"missing", "corrupt", "overflow"} {
			t.Run(kind+failure, func(t *testing.T) {
				fixture := assetsFixture(t, false)
				s := fixture.store
				id := fixture.unit.ID
				if kind == "usage" {
					id = fixture.usage.ID
				} else if kind == "cost" {
					id = fixture.cost.ID
				}
				if failure == "overflow" {
					if kind == "disposal" {
						execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,?)", id, maxSafeInteger)
					} else {
						setCatalogProjectionRevision(t, s, kind, id, maxSafeInteger)
					}
				}
				before := domainState(t, s)
				ack, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "asset-bad-materialization", "test_asset_bad_materialization", struct{ Kind, Failure string }{kind, failure}, func(m *mutationTx) (ActionAck, error) {
					identity := id
					if failure == "missing" {
						identity = "missing"
					}
					if failure == "corrupt" {
						var err error
						switch kind {
						case "asset":
							_, err = m.tx.ExecContext(m.ctx, "UPDATE asset_profiles SET currency='jpy' WHERE item_unit_id=?", id)
						case "usage":
							identity = "bad-usage"
							_, err = m.tx.ExecContext(m.ctx, "INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,created_at) VALUES(?,?,?,'times','use','2026-11-07T00:00:00Z','legacy')", identity, fixture.unit.ID, maxSafeInteger+1)
						case "cost":
							identity = "bad-cost"
							_, err = m.tx.ExecContext(m.ctx, "INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,currency,direction,kind,occurred_at,created_at) VALUES(?,?,?,'JPY','expense','repair','2026-11-07T00:00:00Z','legacy')", identity, fixture.unit.ID, maxSafeInteger+1)
						case "disposal":
							_, err = m.tx.ExecContext(m.ctx, "INSERT INTO asset_disposals(item_unit_id,kind,disposed_at,realized_proceeds_minor,currency,created_at) VALUES(?,'sold','2026-11-07T00:00:00Z',?,'JPY','legacy')", identity, maxSafeInteger+1)
						}
						if err != nil {
							return ActionAck{}, err
						}
					}
					revision, err := m.touch(kind, identity, false)
					return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, err
				})
				want := ErrDomainState
				if failure == "missing" {
					want = ErrNotFound
				} else if failure == "overflow" {
					want = ErrRevisionOverflow
				}
				if !errors.Is(err, want) || ack != (ActionAck{}) {
					t.Fatal("invalid final financial source released a partial result", kind, failure, err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
}

func TestAssetCounterOnlyTombstonesRemainIndependentWithoutDeletingFinancialHistory(t *testing.T) {
	fixture := assetsFixture(t, true)
	s := fixture.store
	for _, kind := range []string{"asset", "usage", "cost", "disposal"} {
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES(?,?,7)", kind, "gone-"+kind)
	}
	before := domainState(t, s)
	changed := requireProjectionReplay(t, s, func() {
		_, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "asset-independent-tombstones", "test_asset_tombstones", struct{}{}, func(m *mutationTx) (ActionAck, error) {
			var revision int64
			for _, kind := range []string{"asset", "usage", "cost", "disposal"} {
				var err error
				revision, err = m.touch(kind, "gone-"+kind, true)
				if err != nil {
					return ActionAck{}, err
				}
			}
			return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 4 {
		t.Fatal("counter-only tombstone omitted another independent identity")
	}
	after := domainState(t, s)
	for _, kind := range []string{"asset", "usage", "cost", "disposal"} {
		if !changed[projectionKey{kind, "gone-" + kind}] || scalar[int](t, s.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", kind, "gone-"+kind) != 1 {
			t.Fatal("independent financial tombstone was missing", kind)
		}
	}
	for _, table := range []string{"asset_profiles", "asset_usage_events", "asset_cost_events", "asset_disposals", "purchase_lines", "item_units"} {
		if after[table] != before[table] {
			t.Fatal("counter-only removal erased posted source or another owner", table)
		}
	}
}
