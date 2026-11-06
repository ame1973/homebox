package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type assetFixture struct {
	purchaseFixture
	asset    Asset
	usage    Usage
	cost     Cost
	disposal Disposal
}

// Retained source only, not a public Asset command or proof of financial policy.
// Actual catalog/auth and retained Purchase/Unit authority come from existing fixtures.
func assetsFixture(t *testing.T, withDisposal bool) assetFixture {
	t.Helper()
	fixture := purchasesFixture(t)
	s := fixture.store
	asset := Asset{AssetData{ItemUnitID: fixture.unit.ID, CostBasis: "purchase", Currency: "JPY",
		InServiceAt: testPointer("2024-11-07T12:00:00.123456789+08:00"), EstimatedResidualMinor: testPointer(int64(20000)), UsageUnit: "times"}, 7}
	usage := Usage{UsageData{ID: "retained-use", ItemUnitID: fixture.unit.ID, Delta: 250, UsageUnit: "times", EventType: "use", OccurredAt: "2026-11-07T12:00:00.123456789+08:00", Note: "\x00Exact usage é\n"}, 3}
	cost := Cost{CostData{ID: "retained-cost", ItemUnitID: fixture.unit.ID, AmountMinor: 5000, Direction: "expense", Currency: "JPY", Kind: "repair", OccurredAt: usage.OccurredAt, Note: "\x00Exact cost é\n"}, 5}
	disposal := Disposal{DisposalData{ItemUnitID: fixture.unit.ID, Kind: "sold", DisposedAt: usage.OccurredAt, RealizedProceedsMinor: 20000, Currency: "JPY", Note: "\x00Exact disposal é\n"}, 11}
	execSQL(t, s.db, `INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,in_service_at,estimated_residual_minor,usage_unit,created_at,updated_at) VALUES(?,?,?,?,?,?,'legacy-created','legacy-updated')`, asset.ItemUnitID, asset.CostBasis, asset.Currency, asset.InServiceAt, asset.EstimatedResidualMinor, asset.UsageUnit)
	execSQL(t, s.db, `INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,note,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,'retained-raw-use-key','legacy-created')`, usage.ID, usage.ItemUnitID, usage.Delta, usage.UsageUnit, usage.EventType, usage.OccurredAt, usage.Note)
	execSQL(t, s.db, `INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,direction,currency,kind,occurred_at,note,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,?,'retained-raw-cost-key','legacy-created')`, cost.ID, cost.ItemUnitID, cost.AmountMinor, cost.Direction, cost.Currency, cost.Kind, cost.OccurredAt, cost.Note)
	for _, row := range []struct {
		kind, id string
		data     any
		revision int64
	}{{"asset", asset.ItemUnitID, asset.AssetData, asset.Revision}, {"usage", usage.ID, usage.UsageData, usage.Revision}, {"cost", cost.ID, cost.CostData, cost.Revision}} {
		seedCatalogProjection(t, s, row.kind, row.id, row.data)
		setCatalogProjectionRevision(t, s, row.kind, row.id, row.revision)
	}
	if withDisposal {
		execSQL(t, s.db, `INSERT INTO asset_disposals(item_unit_id,kind,disposed_at,realized_proceeds_minor,currency,note,created_at) VALUES(?,?,?,?,?,?,'legacy-created')`, disposal.ItemUnitID, disposal.Kind, disposal.DisposedAt, disposal.RealizedProceedsMinor, disposal.Currency, disposal.Note)
		seedCatalogProjection(t, s, "disposal", disposal.ItemUnitID, disposal.DisposalData)
		setCatalogProjectionRevision(t, s, "disposal", disposal.ItemUnitID, disposal.Revision)
	}
	return assetFixture{fixture, asset, usage, cost, disposal}
}

func TestAssetRepresentationsPreserveSafeBoundsNullsExactInstantsAndRetainedHistory(t *testing.T) {
	id := strings.Repeat("😀", 128)
	instant := "2026-11-07T12:00:00.123456789+23:59"
	asset := Asset{AssetData{ItemUnitID: id, CostBasis: "manual", Currency: "ZZZ", ManualAcquisitionMinor: testPointer(maxSafeInteger), InServiceAt: &instant, RetiredAt: &instant, EstimatedResidualMinor: testPointer(maxSafeInteger), UsageUnit: "times"}, maxSafeInteger}
	usage := Usage{UsageData{ID: id, ItemUnitID: id, Delta: -maxSafeInteger, UsageUnit: "minutes", EventType: "correction", OccurredAt: instant, Note: strings.Repeat("😀", 10000)}, maxSafeInteger}
	cost := Cost{CostData{ID: id, ItemUnitID: id, AmountMinor: maxSafeInteger, Direction: "credit", Currency: "ZZZ", Kind: "adjustment", OccurredAt: instant, Note: strings.Repeat("😀", 10000)}, maxSafeInteger}
	disposal := Disposal{DisposalData{ItemUnitID: id, Kind: "donated", DisposedAt: instant, RealizedProceedsMinor: maxSafeInteger, Currency: "ZZZ", Note: strings.Repeat("😀", 10000)}, maxSafeInteger}
	for _, value := range []domainResult{asset, usage, cost, disposal} {
		if err := value.validateDomainResult(); err != nil {
			t.Fatal("retained representation was repaired to new-input eligibility", err)
		}
	}
	asset.ManualAcquisitionMinor, asset.InServiceAt, asset.RetiredAt, asset.EstimatedResidualMinor = testPointer(int64(0)), nil, nil, nil
	usage.Delta, usage.Note, cost.Note, disposal.Note = 1, "\x00Exact é\n", "\x00Exact é\n", "\x00Exact é\n"
	disposal.RealizedProceedsMinor = 0
	for _, value := range []domainResult{asset, usage, cost, disposal} {
		if value.validateDomainResult() != nil {
			t.Fatal("explicit zero/unknown date/exact native controls were lost")
		}
	}
	for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
		a, u, c, d := asset, usage, cost, disposal
		a.Revision, u.Revision, c.Revision, d.Revision = revision, revision, revision, revision
		for _, value := range []domainResult{a, u, c, d} {
			if !errors.Is(value.validateDomainResult(), ErrDomainState) {
				t.Fatal("unsafe independent counter accepted")
			}
		}
	}
	for _, field := range []string{"id", "currency", "basis", "usage", "manual", "residual", "service", "retired"} {
		value := asset
		switch field {
		case "id":
			value.ItemUnitID = strings.Repeat("\x00", 129)
		case "currency":
			value.Currency = "jpy"
		case "basis":
			value.CostBasis = "both"
		case "usage":
			value.UsageUnit = "hours"
		case "manual":
			value.ManualAcquisitionMinor = nil
		case "residual":
			value.EstimatedResidualMinor = testPointer(maxSafeInteger + 1)
		case "service":
			value.InServiceAt = testPointer("2026-11-07T00:00:00.1234567890Z")
		case "retired":
			value.RetiredAt = testPointer("0001-01-01T00:00:00+00:01")
		}
		if !errors.Is(value.validateDomainResult(), ErrDomainState) {
			t.Fatal("invalid profile representation accepted", field)
		}
	}
	for _, amount := range []int64{-1, maxSafeInteger + 1} {
		value := asset
		value.ManualAcquisitionMinor = &amount
		if value.validateDomainResult() == nil {
			t.Fatal("unsafe manual amount accepted")
		}
		value = asset
		value.EstimatedResidualMinor = &amount
		if value.validateDomainResult() == nil {
			t.Fatal("unsafe residual accepted")
		}
		c, d := cost, disposal
		c.AmountMinor, d.RealizedProceedsMinor = amount, amount
		if c.validateDomainResult() == nil || d.validateDomainResult() == nil {
			t.Fatal("unsafe cost/proceeds accepted")
		}
	}
	for _, delta := range []int64{0, -maxSafeInteger - 1, maxSafeInteger + 1} {
		value := usage
		value.Delta = delta
		if value.validateDomainResult() == nil {
			t.Fatal("zero/unsafe usage accepted")
		}
	}
	for _, field := range []string{"id", "owner", "unit", "type", "date", "note", "reference"} {
		value := usage
		switch field {
		case "id":
			value.ID = ""
		case "owner":
			value.ItemUnitID = string([]byte{0xff})
		case "unit":
			value.UsageUnit = "hours"
		case "type":
			value.EventType = "replace"
		case "date":
			value.OccurredAt = "2026"
		case "note":
			value.Note = "\x00" + strings.Repeat("界", 10000)
		case "reference":
			value.CorrectionOf = &value.ID
		}
		if value.validateDomainResult() == nil {
			t.Fatal("invalid usage representation accepted", field)
		}
	}
	for _, field := range []string{"id", "owner", "amount", "currency", "direction", "kind", "date", "note", "reference"} {
		value := cost
		switch field {
		case "id":
			value.ID = ""
		case "owner":
			value.ItemUnitID = strings.Repeat("\x00", 129)
		case "amount":
			value.AmountMinor = 0
		case "currency":
			value.Currency = "日本円"
		case "direction":
			value.Direction = "negative"
		case "kind":
			value.Kind = "unknown"
		case "date":
			value.OccurredAt = "2026"
		case "note":
			value.Note = string([]byte{0xff})
		case "reference":
			value.CorrectionOf = &value.ID
		}
		if value.validateDomainResult() == nil {
			t.Fatal("invalid cost representation accepted", field)
		}
	}
	for _, field := range []string{"id", "kind", "date", "currency", "note"} {
		value := disposal
		switch field {
		case "id":
			value.ItemUnitID = ""
		case "kind":
			value.Kind = "unknown"
		case "date":
			value.DisposedAt = "2026"
		case "currency":
			value.Currency = "jpy"
		case "note":
			value.Note = string([]byte{0xff})
		}
		if value.validateDomainResult() == nil {
			t.Fatal("invalid disposal representation accepted", field)
		}
	}
}

func TestAssetServiceDatesCompareRealInstantsInsteadOfRetainedOffsetText(t *testing.T) {
	value := Asset{AssetData{ItemUnitID: "unit", CostBasis: "purchase", Currency: "JPY", UsageUnit: "times",
		InServiceAt: testPointer("2026-11-07T12:00:00+08:00"), RetiredAt: testPointer("2026-11-07T05:00:00Z")}, 1}
	if value.validateDomainResult() != nil {
		t.Fatal("chronological interval was rejected because its text sorts backwards")
	}
	value.InServiceAt, value.RetiredAt = testPointer("2026-11-07T04:00:00Z"), testPointer("2026-11-07T12:00:00+08:00")
	if value.validateDomainResult() != nil {
		t.Fatal("equivalent instant spellings were rewritten or rejected")
	}
	value.RetiredAt = testPointer("2026-11-07T12:00:00+09:00")
	if !errors.Is(value.validateDomainResult(), ErrDomainState) {
		t.Fatal("lexically increasing but chronologically reversed interval accepted")
	}
}

func TestAssetLoadersReadExactSourceAndIndependentCountersWithoutSideEffects(t *testing.T) {
	fixture := assetsFixture(t, true)
	s := fixture.store
	checks := []struct {
		kind, id string
		want     domainResult
		load     func(context.Context, queryer, string) (domainResult, error)
		scan     func(rowScanner) (domainResult, error)
	}{
		{"asset", fixture.asset.ItemUnitID, fixture.asset, func(ctx context.Context, q queryer, id string) (domainResult, error) { return loadAsset(ctx, q, id) }, func(row rowScanner) (domainResult, error) { return scanAsset(row) }},
		{"usage", fixture.usage.ID, fixture.usage, func(ctx context.Context, q queryer, id string) (domainResult, error) { return loadUsage(ctx, q, id) }, func(row rowScanner) (domainResult, error) { return scanUsage(row) }},
		{"cost", fixture.cost.ID, fixture.cost, func(ctx context.Context, q queryer, id string) (domainResult, error) { return loadCost(ctx, q, id) }, func(row rowScanner) (domainResult, error) { return scanCost(row) }},
		{"disposal", fixture.disposal.ItemUnitID, fixture.disposal, func(ctx context.Context, q queryer, id string) (domainResult, error) { return loadDisposal(ctx, q, id) }, func(row rowScanner) (domainResult, error) { return scanDisposal(row) }},
	}
	for _, check := range checks {
		t.Run(check.kind, func(t *testing.T) {
			execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"NeverCopyAssetCache\":true}' WHERE entity_type=? AND entity_id=?", check.kind, check.id)
			before := domainState(t, s)
			if value, err := check.load(t.Context(), s.db, check.id); err != nil || !reflect.DeepEqual(value, check.want) {
				t.Fatal("cache or parent revision replaced actual financial source", err)
			}
			requireDomainState(t, s, before)
			if value, err := check.load(t.Context(), s.db, "missing"); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("missing source exposed a financial DTO", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if value, err := check.load(ctx, s.db, check.id); !errors.Is(err, context.Canceled) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("canceled source exposed a financial DTO", err)
			}
			if value, err := check.scan(s.db.QueryRowContext(t.Context(), "SELECT 1")); err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("failed scan exposed a partial financial DTO", err)
			}
			for _, revision := range []int64{0, maxSafeInteger + 1} {
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = tx.ExecContext(t.Context(), "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", check.kind, check.id)
				if err == nil {
					_, err = tx.ExecContext(t.Context(), "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", check.kind, check.id)
				}
				if err == nil && revision != 0 {
					_, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES(?,?,?)", check.kind, check.id, revision)
				}
				if err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				value, readErr := check.load(t.Context(), tx, check.id)
				_ = tx.Rollback()
				if !errors.Is(readErr, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("missing/unsafe financial authority was reseeded", readErr)
				}
				requireDomainState(t, s, before)
			}
		})
	}
	for _, value := range []any{fixture.asset.AssetData, fixture.usage.UsageData, fixture.cost.CostData, fixture.disposal.DisposalData} {
		encoded, err := json.Marshal(value)
		for _, private := range []string{"revision", "created_at", "mutation_event_id", "idempotency_key", "retained-raw"} {
			if err != nil || strings.Contains(string(encoded), private) {
				t.Fatal("private provenance/revisions leaked into the offline payload", err)
			}
		}
	}
}

func TestAssetRelatedAuthorityAndSQLFailuresNeverBecomePartialResources(t *testing.T) {
	fixture := assetsFixture(t, true)
	s := fixture.store
	for _, key := range []projectionKey{{"asset", fixture.unit.ID}, {"unit", fixture.unit.ID}, {"item", fixture.individual.ID}} {
		t.Run(key.kind+" counter", func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(t.Context(), "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(t.Context(), "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id); err != nil {
				t.Fatal(err)
			}
			if value, err := loadUsage(t.Context(), tx, fixture.usage.ID); !errors.Is(err, ErrDomainState) || value != (Usage{}) {
				t.Fatal("missing related authority exposed usage", err)
			}
			if value, err := loadCost(t.Context(), tx, fixture.cost.ID); !errors.Is(err, ErrDomainState) || value != (Cost{}) {
				t.Fatal("missing related authority exposed cost", err)
			}
			if value, err := loadDisposal(t.Context(), tx, fixture.unit.ID); !errors.Is(err, ErrDomainState) || value != (Disposal{}) {
				t.Fatal("missing related authority exposed disposal", err)
			}
		})
	}
	for _, table := range []string{"asset_profiles", "item_units", "items"} {
		t.Run(table+" query", func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(t.Context(), "ALTER TABLE "+table+" RENAME TO unavailable_asset_source"); err != nil {
				t.Fatal(err)
			}
			if value, err := loadUsage(t.Context(), tx, fixture.usage.ID); err == nil || value != (Usage{}) {
				t.Fatal("failed source query exposed financial data", err)
			}
		})
	}
	for _, state := range []string{"missing Unit", "invalid Item", "quantity owner"} {
		t.Run(state, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var statement string
			var args []any
			switch state {
			case "missing Unit":
				statement, args = "INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,created_at,updated_at) VALUES('absent','purchase','JPY','legacy','legacy')", nil
				if _, err = tx.ExecContext(t.Context(), "PRAGMA defer_foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('asset','absent',1)"); err != nil {
					t.Fatal(err)
				}
			case "invalid Item":
				statement, args = "UPDATE items SET name=CAST(x'ff' AS TEXT) WHERE id=?", []any{fixture.individual.ID}
			case "quantity owner":
				statement, args = "UPDATE item_units SET item_id=? WHERE id=?", []any{fixture.quantity.ID, fixture.unit.ID}
			}
			if _, err = tx.ExecContext(t.Context(), statement, args...); err != nil {
				t.Fatal(err)
			}
			id := fixture.unit.ID
			if state == "missing Unit" {
				id = "absent"
			}
			if value, err := loadAsset(t.Context(), tx, id); !errors.Is(err, ErrDomainState) || value != (Asset{}) {
				t.Fatal("invalid actual Asset owner was hidden by a retained counter", err)
			}
		})
	}
}

func TestAssetCorrectionChainsValidateRealOwnersInterpretationsCountersAndCycles(t *testing.T) {
	fixture := assetsFixture(t, false)
	s := fixture.store
	for _, kind := range []string{"usage", "cost"} {
		for _, failure := range []string{"valid", "missing", "foreign owner", "interpretation", "counter", "cycle"} {
			t.Run(kind+failure, func(t *testing.T) {
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err = tx.ExecContext(t.Context(), "PRAGMA defer_foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
				target, owner, interpretation := "target", fixture.unit.ID, "times"
				if kind == "cost" {
					interpretation = "JPY"
				}
				if failure == "missing" {
					target = "absent"
				}
				targetOwner, targetMeaning := owner, interpretation
				if failure == "foreign owner" {
					targetOwner = "different-unit"
				}
				if failure == "interpretation" {
					if kind == "usage" {
						targetMeaning = "minutes"
					} else {
						targetMeaning = "USD"
					}
				}
				var targetRef *string
				if failure == "cycle" {
					targetRef = testPointer("correction")
				}
				for _, row := range []struct {
					id, owner, meaning string
					ref                *string
				}{{"target", targetOwner, targetMeaning, targetRef}, {"correction", owner, interpretation, &target}} {
					if kind == "usage" {
						_, err = tx.ExecContext(t.Context(), `INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,correction_of,created_at) VALUES(?,?,1,?,'correction','2026-11-07T00:00:00Z',?,'legacy')`, row.id, row.owner, row.meaning, row.ref)
					} else {
						_, err = tx.ExecContext(t.Context(), `INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,currency,kind,direction,occurred_at,correction_of,created_at) VALUES(?,?,1,?,'adjustment','credit','2026-11-07T00:00:00Z',?,'legacy')`, row.id, row.owner, row.meaning, row.ref)
					}
					if err != nil {
						t.Fatal(err)
					}
					if failure != "counter" || row.id != "target" {
						if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES(?,?,1)", kind, row.id); err != nil {
							t.Fatal(err)
						}
					}
				}
				var value domainResult
				if kind == "usage" {
					value, err = loadUsage(t.Context(), tx, "correction")
				} else {
					value, err = loadCost(t.Context(), tx, "correction")
				}
				if failure == "valid" {
					if err != nil || reflect.ValueOf(value).IsZero() {
						t.Fatal("actual same-asset correction chain rejected", err)
					}
					if err = validateAssetsBackfill(t.Context(), tx); err != nil {
						t.Fatal("backfill could not validate actual correction tails", err)
					}
				} else if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("invalid correction chain exposed a partial event", failure, err)
				}
			})
		}
	}
}

func TestAssetOwnedEventsRejectMissingProfilesWithoutInventingZeroAcquisition(t *testing.T) {
	fixture := assetsFixture(t, false)
	s := fixture.store
	for _, kind := range []string{"usage", "cost", "disposal"} {
		t.Run(kind, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(t.Context(), "PRAGMA defer_foreign_keys=ON"); err != nil {
				t.Fatal(err)
			}
			statement := map[string]string{
				"usage":    "INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,created_at) VALUES('orphan','missing-profile',1,'times','use','2026-11-07T00:00:00Z','legacy')",
				"cost":     "INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,currency,direction,kind,occurred_at,created_at) VALUES('orphan','missing-profile',1,'JPY','expense','repair','2026-11-07T00:00:00Z','legacy')",
				"disposal": "INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,currency,created_at) VALUES('missing-profile','2026-11-07T00:00:00Z','sold','JPY','legacy')",
			}[kind]
			if _, err = tx.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			id := "orphan"
			if kind == "disposal" {
				id = "missing-profile"
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES(?,?,1)", kind, id); err != nil {
				t.Fatal(err)
			}
			var value domainResult
			switch kind {
			case "usage":
				value, err = loadUsage(t.Context(), tx, id)
			case "cost":
				value, err = loadCost(t.Context(), tx, id)
			case "disposal":
				value, err = loadDisposal(t.Context(), tx, id)
			}
			if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("orphan event exposed data or became an implicit zero profile", err)
			}
			if err = validateAssetsBackfill(t.Context(), tx); !errors.Is(err, ErrDomainState) {
				t.Fatal("backfill accepted an orphan financial event", err)
			}
		})
	}
}

func TestAssetBackfillScanAndLateSQLiteIteratorErrorsNeverCertifyAuthority(t *testing.T) {
	fixture := assetsFixture(t, false)
	s := fixture.store
	for _, failure := range []string{"counter scan", "counter iterator", "source iterator"} {
		t.Run(failure, func(t *testing.T) {
			before := domainState(t, s)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// Private corruption/failure injection only. Roll back every DDL change;
			// production integrity, FK and schema policies are never disabled.
			var statements []string
			if failure == "source iterator" {
				statements = []string{"ALTER TABLE asset_profiles RENAME TO saved_asset_profiles",
					`CREATE VIEW asset_profiles AS SELECT * FROM saved_asset_profiles UNION ALL
 SELECT item_unit_id,in_service_at,retired_at,cost_basis,manual_acquisition_minor,json_extract('invalid JSON','$'),
 estimated_residual_minor,usage_unit,created_at,updated_at FROM saved_asset_profiles`}
			} else {
				view := "CREATE VIEW projection_revisions AS SELECT 'asset' AS entity_type,'unit' AS entity_id,'not a counter' AS revision"
				if failure == "counter iterator" {
					view = "CREATE VIEW projection_revisions AS SELECT 'asset' AS entity_type,'unit' AS entity_id,1 AS revision UNION ALL SELECT 'asset','bad',json_extract('invalid JSON','$')"
				}
				statements = []string{"ALTER TABLE projection_revisions RENAME TO saved_asset_counters", view}
			}
			for _, statement := range statements {
				if _, err = tx.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			if err = validateAssetsBackfill(t.Context(), tx); err == nil {
				t.Fatal("partial counter/source scan was certified")
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestAssetCorrectionValidationIsIterativeAndMemoizesLongVerifiedTails(t *testing.T) {
	const count = 5000
	values := make(map[string]assetCorrectionRef, count)
	for i := range count {
		id := fmt.Sprint(i)
		var target *string
		if i+1 < count {
			target = testPointer(fmt.Sprint(i + 1))
		}
		values[id] = assetCorrectionRef{id, "unit", "JPY", target}
	}
	reads := 0
	load := func(id string) (assetCorrectionRef, error) { reads++; return values[id], nil }
	verified := make(map[string]bool)
	for i := range count {
		if err := validateAssetCorrection(values[fmt.Sprint(i)], load, verified); err != nil {
			t.Fatal("long valid chain failed", err)
		}
	}
	if reads != count-1 || len(verified) != count {
		t.Fatal("backfill repeatedly traversed the same verified history", reads)
	}
	if err := validateAssetCorrection(assetCorrectionRef{"bad", "unit", "JPY", testPointer("missing")}, func(string) (assetCorrectionRef, error) { return assetCorrectionRef{}, sql.ErrConnDone }, verified); !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("correction source query failure was hidden", err)
	}
}

func TestAssetScannersRetainUnknownLegacyCreationButValidateClaimedProducerTime(t *testing.T) {
	s := openTestStore(t)
	for _, check := range []struct {
		name, query string
		scan        func(rowScanner) (domainResult, error)
	}{
		{"usage", "SELECT 'new-use','unit',1,'times','use','2026-11-07T00:00:00Z','',NULL,1,?,?", func(row rowScanner) (domainResult, error) { return scanUsage(row) }},
		{"cost", "SELECT 'new-cost','unit',1,'expense','JPY','repair','2026-11-07T00:00:00Z','',NULL,1,?,?", func(row rowScanner) (domainResult, error) { return scanCost(row) }},
		{"disposal", "SELECT 'unit','sold','2026-11-07T00:00:00Z',0,'JPY','',1,?,?", func(row rowScanner) (domainResult, error) { return scanDisposal(row) }},
	} {
		t.Run(check.name, func(t *testing.T) {
			for _, metadata := range []struct {
				created string
				event   *string
				valid   bool
			}{{"unknown legacy spelling", nil, true}, {"2026-11-07T00:00:00.123456789Z", testPointer("event"), true},
				{"2026", testPointer("event"), false}, {"2026-11-07T00:00:00.1234567890Z", testPointer("event"), false},
				{"0001-01-01T00:00:00+00:01", testPointer("event"), false}, {"2026-11-07T00:00:00Z", testPointer(""), false}} {
				value, err := check.scan(s.db.QueryRowContext(t.Context(), check.query, metadata.created, metadata.event))
				if metadata.valid && err != nil || !metadata.valid && (!errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero()) {
					t.Fatal("producer provenance was guessed or malformed claimed time accepted", err)
				}
			}
		})
	}
}
