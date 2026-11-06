package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAssetFinancialSafeTotalsRejectWholeRangeOverflowWithoutRowOrderDependence(t *testing.T) {
	for _, amount := range []int64{-maxSafeInteger, -1, 0, 1, maxSafeInteger} {
		if value, err := assetSafeTotal(big.NewInt(amount)); err != nil || value != amount {
			t.Fatal("exact signed safe boundary was rejected", amount, err)
		}
	}
	for _, text := range []string{"9007199254740992", "-9007199254740992", "9223372036854775808", "-9223372036854775809", "1000000000000000000000000000000"} {
		amount, ok := new(big.Int).SetString(text, 10)
		if !ok {
			t.Fatal("invalid test integer")
		}
		if value, err := assetSafeTotal(amount); !errors.Is(err, ErrAssetOverflow) || value != 0 {
			t.Fatal("unsafe aggregate was truncated, wrapped or exposed", text, err)
		}
	}
}

func TestAssetSummaryCompletesLargeSourceCollectionsAndCancellationBeyondInt64(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 2050 {
		direction := "expense"
		if i >= 1025 {
			direction = "credit"
		}
		id := fmt.Sprintf("complete-cost-%04d", i)
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,created_at)
 VALUES(?,?,'2026-11-07T00:00:00Z','repair',?,?,'JPY','legacy')`, id, fixture.unit.ID, maxSafeInteger, direction); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('cost',?,1)", id); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 205 {
		id := fmt.Sprintf("complete-use-%04d", i)
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,created_at)
 VALUES(?,?,'2026-11-07T00:00:00Z',1,'times','use','legacy')`, id, fixture.unit.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('usage',?,1)", id); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Positive/negative cost groups each exceed int64. The exact signed result is
	// zero, regardless of SQL SUM overflow, stable-ID order or live-page defaults.
	before := domainState(t, s)
	value, err := assetReader(t, fixture).CostSummary(t.Context(), fixture.token, fixture.unit.ID, "")
	if err != nil || value.ExtraMinor != 0 || value.TotalUsage != 205 || value.PerUseMinor == nil || *value.PerUseMinor != 0 {
		t.Fatal("financial source was paginated, summed through SQL/float overflow or incompletely validated", value, err)
	}
	requireDomainState(t, s, before)
}

func TestAssetSummaryEqualInstantsGroupButDistinctNanosecondsPreserveUsagePrefix(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	insertAssetUsage(t, s, "later-use", fixture.unit.ID, "2026-11-07T00:00:00.000000002Z", 1, nil)
	insertAssetUsage(t, s, "earlier-correction", fixture.unit.ID, "2026-11-07T00:00:00.000000001Z", -1, testPointer("later-use"))
	before := domainState(t, s)
	if value, err := assetReader(t, fixture).CostSummary(t.Context(), fixture.token, fixture.unit.ID, ""); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("a nanosecond-negative prefix was hidden by second/millisecond truncation or zero lifetime total", value, err)
	}
	requireDomainState(t, s, before)
}

func TestAssetSummaryValidatesActualCorrectionGraphsBeforeCalculating(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		for _, graph := range []string{"valid", "cycle", "wrong interpretation"} {
			t.Run(kind+graph, func(t *testing.T) {
				fixture, _ := manualAssetFixture(t, 0)
				s := fixture.store
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				var firstRef *string
				if graph == "cycle" {
					firstRef = testPointer("second")
				}
				for _, row := range []struct {
					id  string
					ref *string
				}{{"first", firstRef}, {"second", testPointer("first")}} {
					meaning := "times"
					if kind == "cost" {
						meaning = "JPY"
					}
					if graph == "wrong interpretation" && row.id == "first" {
						if kind == "usage" {
							meaning = "minutes"
						} else {
							meaning = "USD"
						}
					}
					if kind == "usage" {
						_, err = tx.ExecContext(t.Context(), "INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,correction_of,created_at) VALUES(?,?,'2026-11-07T00:00:00Z',1,?,'correction',?,'legacy')", row.id, fixture.unit.ID, meaning, row.ref)
					} else {
						_, err = tx.ExecContext(t.Context(), "INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,correction_of,created_at) VALUES(?,?,'2026-11-07T00:00:00Z','adjustment',1,'expense',?,?,'legacy')", row.id, fixture.unit.ID, meaning, row.ref)
					}
					if err != nil {
						t.Fatal(err)
					}
					if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES(?,?,1)", kind, row.id); err != nil {
						t.Fatal(err)
					}
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				before := domainState(t, s)
				value, err := assetReader(t, fixture).CostSummary(t.Context(), fixture.token, fixture.unit.ID, "")
				if graph == "valid" {
					if err != nil || kind == "usage" && value.TotalUsage != 2 || kind == "cost" && value.ExtraMinor != 2 {
						t.Fatal("valid retained unknown-provenance correction graph was rejected", value, err)
					}
				} else if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("invalid correction graph was summed without actual validation", graph, value, err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
}

func TestAssetSummaryScanAndLateIteratorFailuresWithholdEveryCompleteTotal(t *testing.T) {
	for _, source := range []struct{ table, amount string }{{"purchase_lines", "paid_minor"}, {"asset_cost_events", "amount_minor"}, {"asset_usage_events", "delta"}} {
		for _, defect := range []string{"scan", "late iterator"} {
			t.Run(source.table+defect, func(t *testing.T) {
				fixture := assetsFixture(t, true)
				s := fixture.store
				before := domainState(t, s)
				value, err := readDomain(t.Context(), fixture.auth, fixture.token, func(tx *sql.Tx) (AssetCostSummary, error) {
					if _, err := tx.ExecContext(t.Context(), "ALTER TABLE "+source.table+" RENAME TO saved_asset_finance_source"); err != nil {
						t.Fatal(err)
					}
					rows, err := tx.QueryContext(t.Context(), "SELECT * FROM saved_asset_finance_source LIMIT 0")
					if err != nil {
						t.Fatal(err)
					}
					columns, err := rows.Columns()
					if err = errors.Join(err, rows.Close()); err != nil {
						t.Fatal(err)
					}
					for i, column := range columns {
						if defect == "scan" && column == source.amount {
							columns[i] = "'not a safe integer' AS " + column
						} else if defect == "late iterator" && column == "note" {
							columns[i] = "json_extract('invalid JSON','$') AS note"
						}
					}
					selection := "SELECT " + strings.Join(columns, ",") + " FROM saved_asset_finance_source"
					if defect == "late iterator" {
						selection = "SELECT * FROM saved_asset_finance_source UNION ALL " + selection
					}
					if _, err = tx.ExecContext(t.Context(), "CREATE VIEW "+source.table+" AS "+selection); err != nil {
						t.Fatal(err)
					}
					return loadAssetCostSummary(t.Context(), tx, fixture.unit.ID, fixture.auth.now())
				})
				if err == nil || !reflect.ValueOf(value).IsZero() {
					t.Fatal("partial financial source scan/iteration exposed aggregate data", value, err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
}

func TestAssetSummaryExplicitEmptyPurchaseBasisAndZeroInstantAreNotMissingIntent(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	execSQL(t, s.db, "UPDATE asset_profiles SET cost_basis='purchase',manual_acquisition_minor=NULL,in_service_at='0001-01-01T00:00:00Z' WHERE item_unit_id=?", fixture.unit.ID)
	before := domainState(t, s)
	value, err := assetReader(t, fixture).CostSummary(t.Context(), fixture.token, fixture.unit.ID, "0001-01-01T00:00:00Z")
	if err != nil || value.CostBasis != "purchase" || value.AcquisitionMinor != 0 || value.AsOf != "0001-01-01T00:00:00.000000000Z" || value.ElapsedDays == nil || *value.ElapsedDays != 1 || value.PerDayMinor == nil || *value.PerDayMinor != 0 {
		t.Fatal("completed empty purchase history or explicit zero time was replaced by missing/default intent", value, err)
	}
	requireDomainState(t, s, before)
	if value, err := assetReader(t, fixture).GetDisposal(t.Context(), fixture.token, fixture.unit.ID); !errors.Is(err, ErrNotFound) || value != (Disposal{}) {
		t.Fatal("absence became an implicit zero-proceeds disposal", value, err)
	}
}

func TestAssetSummaryActualTimezoneAndZeroCatalogRevisionStayAuthoritative(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 100)
	s := fixture.store
	execSQL(t, s.db, `INSERT INTO app_settings VALUES('preferences.timezone','"Asia/Taipei"','2026')`)
	execSQL(t, s.db, "UPDATE asset_profiles SET in_service_at='2026-11-07T15:59:59Z' WHERE item_unit_id=?", fixture.unit.ID)
	execSQL(t, s.db, "UPDATE data_revision SET revision=0 WHERE scope='catalog'")
	value, err := assetReader(t, fixture).CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T16:00:00.000000001Z")
	if err != nil || value.Timezone != "Asia/Taipei" || value.ElapsedDays == nil || *value.ElapsedDays != 2 || value.SourceRevision != 0 || value.PerDayMinor == nil || *value.PerDayMinor != 50 {
		t.Fatal("summary borrowed host timezone, synthesized revision one or counted 24-hour durations", value, err)
	}
	// A home-calendar reversal at Alaska's historical dateline change must not
	// become zero/negative elapsed days or a silently clamped divisor.
	zone, err := time.LoadLocation("America/Juneau")
	if err != nil {
		t.Fatal(err)
	}
	asset := Asset{AssetData{ItemUnitID: "unit", CostBasis: "purchase", Currency: "JPY", UsageUnit: "times", InServiceAt: testPointer("1867-10-18T12:00:00Z")}, 1}
	end, err := time.Parse(time.RFC3339Nano, "1867-10-19T01:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if days, err := assetElapsedDays(asset, end, zone); !errors.Is(err, ErrDomainState) || days != nil {
		t.Fatal("a reversed civil date range produced a nonpositive or invented divisor", days, err)
	}
	execSQL(t, s.db, `UPDATE app_settings SET value_json='"America/Juneau"' WHERE key='preferences.timezone'`)
	execSQL(t, s.db, "UPDATE asset_profiles SET in_service_at=? WHERE item_unit_id=?", asset.InServiceAt, fixture.unit.ID)
	before := domainState(t, s)
	if value, err := assetReader(t, fixture).CostSummary(t.Context(), fixture.token, fixture.unit.ID, stamp(end)); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("invalid home date allocation escaped as a partial service summary", value, err)
	}
	requireDomainState(t, s, before)
}
