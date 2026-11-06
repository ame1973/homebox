package store

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func assetReader(t *testing.T, fixture holdingFixture) *Assets {
	t.Helper()
	reader, err := fixture.store.NewAssets(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func manualAssetFixture(t *testing.T, amount int64) (holdingFixture, Asset) {
	t.Helper()
	fixture := holdingsFixture(t)
	asset := Asset{AssetData{ItemUnitID: fixture.unit.ID, CostBasis: "manual", Currency: "JPY", ManualAcquisitionMinor: &amount, UsageUnit: "times"}, 1}
	execSQL(t, fixture.store.db, `INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,manual_acquisition_minor,usage_unit,created_at,updated_at)
 VALUES(?,'manual','JPY',?,'times','legacy','legacy')`, fixture.unit.ID, amount)
	seedCatalogProjection(t, fixture.store, "asset", fixture.unit.ID, asset.AssetData)
	return fixture, asset
}

func insertAssetUsage(t *testing.T, s *Store, id, unitID, instant string, delta int64, reference *string) {
	t.Helper()
	eventType := "use"
	if reference != nil {
		eventType = "correction"
	}
	execSQL(t, s.db, `INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,correction_of,created_at)
 VALUES(?,?,?,?,'times',?,?,'legacy')`, id, unitID, instant, delta, eventType, reference)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('usage',?,1)", id)
}

func insertAssetCost(t *testing.T, s *Store, id, unitID string, amount int64, direction string) {
	t.Helper()
	execSQL(t, s.db, `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,created_at)
 VALUES(?,?,'2026-11-07T00:00:00Z','repair',?,?,'JPY','legacy')`, id, unitID, amount, direction)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('cost',?,1)", id)
}

func TestAssetSummaryUsesExactUnitAllocationsOnceAndCurrentPostedSource(t *testing.T) {
	fixture := assetsFixture(t, true)
	s := fixture.store
	reader := assetReader(t, fixture.holdingFixture)
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"NeverFinancialAuthority\":true}' WHERE entity_type IN('asset','usage','cost','disposal','purchase','purchase_line')")
	// An unallocated Item Line is outside this exact Unit, even when its source
	// counter is invalid. It is neither extra acquisition nor relevant authority.
	execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor) VALUES('unallocated',?,?,100,999999)", fixture.purchase.ID, fixture.individual.ID)
	before := domainState(t, s)
	value, err := reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-06T12:00:00.123456789+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if value.UnitID != fixture.unit.ID || value.Currency != "JPY" || value.CostBasis != "purchase" || value.AcquisitionMinor != 120000 || value.ExtraMinor != 5000 || value.RealizedProceedsMinor != 20000 || value.NetCostMinor != 105000 || value.EstimatedResidualMinor == nil || *value.EstimatedResidualMinor != 20000 || value.ProjectedNetCostMinor == nil || *value.ProjectedNetCostMinor != 105000 {
		t.Fatal("summary multiplied retained quantity, used a header/cache, borrowed an unallocated Line, or mixed residual and proceeds", value)
	}
	if value.ElapsedDays == nil || *value.ElapsedDays != 730 || value.TotalUsage != 250 || value.UsageUnit != "times" || value.PerUseMinor == nil || *value.PerUseMinor != 420 || value.PerDayMinor == nil || math.Abs(*value.PerDayMinor-105000.0/730) > 1e-10 {
		t.Fatal("current posted usage/proceeds were filtered by as_of or divisors were incorrect", value)
	}
	if value.AsOf != "2026-11-06T04:00:00.123456789Z" || value.Timezone != "UTC" || value.DayPolicy != "inclusive_civil_days" || value.SourceRevision != scalar[int64](t, s.db, "SELECT revision FROM data_revision WHERE scope='catalog'") {
		t.Fatal("summary lost nanoseconds, effective timezone, date policy or real source revision", value)
	}
	requireDomainState(t, s, before)
	asset, err := reader.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || !reflect.DeepEqual(asset, fixture.asset) {
		t.Fatal("Asset read substituted cache or a related counter", err)
	}
	disposal, err := reader.GetDisposal(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || !reflect.DeepEqual(disposal, fixture.disposal) {
		t.Fatal("Disposal read reconstructed volatile state instead of actual source", err)
	}
	requireDomainState(t, s, before)
}

func TestAssetSummaryPreservesExplicitZeroNullDenominatorsNegativeCostsAndSeparateProjection(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	reader := assetReader(t, fixture)
	value, err := reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "")
	if err != nil || value.AcquisitionMinor != 0 || value.ExtraMinor != 0 || value.NetCostMinor != 0 || value.TotalUsage != 0 || value.ElapsedDays != nil || value.PerDayMinor != nil || value.PerUseMinor != nil || value.EstimatedResidualMinor != nil || value.ProjectedNetCostMinor != nil || value.ProjectedPerDayMinor != nil || value.ProjectedPerUseMinor != nil || value.AsOf != stamp(fixture.auth.now()) {
		t.Fatal("empty actual source lost explicit zero/null or invented service/usage/residual", value, err)
	}
	insertAssetCost(t, s, "credit", fixture.unit.ID, 100, "credit")
	execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=20,in_service_at='2026-11-04T00:00:00Z' WHERE item_unit_id=?", fixture.unit.ID)
	value, err = reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-05T00:00:00Z")
	if err != nil || value.ExtraMinor != -100 || value.NetCostMinor != -100 || value.ProjectedNetCostMinor == nil || *value.ProjectedNetCostMinor != -120 || value.PerDayMinor == nil || *value.PerDayMinor != -50 || value.ProjectedPerDayMinor == nil || *value.ProjectedPerDayMinor != -60 || value.PerUseMinor != nil || value.ProjectedPerUseMinor != nil {
		t.Fatal("credits were clamped, residual was treated as actual proceeds, or zero usage divided", value, err)
	}
	execSQL(t, s.db, "INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,created_at) VALUES(?,'2026-11-07T00:00:00Z','sold',10,'JPY','legacy')", fixture.unit.ID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,1)", fixture.unit.ID)
	value, err = reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-05T00:00:00Z")
	if err != nil || value.NetCostMinor != -110 || value.ProjectedNetCostMinor == nil || *value.ProjectedNetCostMinor != -120 {
		t.Fatal("sale and residual were subtracted together or future posted disposal was hidden", value, err)
	}
}

func TestAssetSummaryUsesCompleteHistoryAndExactSignedArithmeticIndependentOfIDOrder(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	reader := assetReader(t, fixture)
	for _, id := range []string{"a-expense", "b-expense"} {
		insertAssetCost(t, s, id, fixture.unit.ID, maxSafeInteger, "expense")
	}
	for _, id := range []string{"c-credit", "d-credit"} {
		insertAssetCost(t, s, id, fixture.unit.ID, maxSafeInteger, "credit")
	}
	insertAssetCost(t, s, "e-small", fixture.unit.ID, 1, "expense")
	for _, step := range []struct {
		id    string
		delta int64
	}{{"a-use", maxSafeInteger}, {"b-use", maxSafeInteger}, {"c-correction", -maxSafeInteger}} {
		var reference *string
		if step.delta < 0 {
			reference = testPointer("a-use")
		}
		instant := "2026-11-07T00:00:00.123456789Z"
		if step.delta < 0 {
			instant = "2026-11-07T08:00:00.123456789+08:00"
		}
		insertAssetUsage(t, s, step.id, fixture.unit.ID, instant, step.delta, reference)
	}
	value, err := reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "")
	if err != nil || value.ExtraMinor != 1 || value.TotalUsage != maxSafeInteger {
		t.Fatal("transient ID-order sums or equivalent-offset spelling changed the exact complete result", value, err)
	}
}

func TestAssetSummaryCivilDayPolicyUsesCalendarLabelsExactInstantsAndRetirement(t *testing.T) {
	for _, test := range []struct {
		name, zone, start, end, retired string
		days                            int64
	}{
		{"unknown service", "UTC", "", "2026-11-07T00:00:00Z", "", 0},
		{"before same-day start", "UTC", "2026-11-07T00:00:00.000000002Z", "2026-11-07T00:00:00.000000001Z", "", 0},
		{"exact start", "UTC", "2026-11-07T00:00:00.000000002Z", "2026-11-07T00:00:00.000000002Z", "", 1},
		{"spring DST", "America/New_York", "2024-03-10T05:00:00Z", "2024-03-11T04:00:00Z", "", 2},
		{"autumn DST", "America/New_York", "2024-11-03T04:00:00Z", "2024-11-04T05:00:00Z", "", 2},
		{"repeated hour", "America/New_York", "2024-11-03T01:30:00-04:00", "2024-11-03T01:15:00-05:00", "", 1},
		{"skipped date labels", "Pacific/Apia", "2011-12-29T12:00:00-10:00", "2011-12-31T12:00:00+14:00", "", 3},
		{"repeated date label", "Pacific/Kwajalein", "1969-09-30T01:00:00+11:00", "1969-09-30T23:00:00-12:00", "", 1},
		{"home date", "Asia/Taipei", "2026-11-07T15:59:59Z", "2026-11-07T16:00:00Z", "", 2},
		{"retired frozen", "UTC", "2024-11-07T04:00:00Z", "2026-11-07T04:00:00Z", "2026-11-06T12:00:00+08:00", 730},
		{"future retirement", "UTC", "2026-11-07T00:00:00Z", "2026-11-08T00:00:00Z", "2026-11-09T00:00:00Z", 2},
		{"full supported years", "UTC", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999999Z", "", 3652059},
		{"home year crossing", "Pacific/Kiritimati", "9999-12-30T23:00:00Z", "9999-12-31T23:00:00Z", "", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			zone, err := time.LoadLocation(test.zone)
			if err != nil {
				t.Fatal(err)
			}
			asset := Asset{AssetData{ItemUnitID: "unit", CostBasis: "purchase", Currency: "JPY", UsageUnit: "times"}, 1}
			if test.start != "" {
				asset.InServiceAt = &test.start
			}
			if test.retired != "" {
				asset.RetiredAt = &test.retired
			}
			end, err := time.Parse(time.RFC3339Nano, test.end)
			if err != nil {
				t.Fatal(err)
			}
			days, err := assetElapsedDays(asset, end, zone)
			if err != nil || test.days == 0 && days != nil || test.days > 0 && (days == nil || *days != test.days) {
				t.Fatal("civil-day policy used duration, lexical offsets, a future start or saturated long interval", days, err)
			}
		})
	}
}

func TestAssetSummaryRejectsIncompatibleCurrencyBasisUsageAndUnsafeFinalTotals(t *testing.T) {
	for _, defect := range []string{"manual with allocation", "allocation currency", "cost currency", "usage interpretation", "disposal currency", "negative usage prefix", "usage prefix overflow", "extra overflow", "net overflow", "projected overflow", "acquisition overflow"} {
		t.Run(defect, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s := fixture.store
			reader := assetReader(t, fixture.holdingFixture)
			switch defect {
			case "manual with allocation":
				execSQL(t, s.db, "UPDATE asset_profiles SET cost_basis='manual',manual_acquisition_minor=0 WHERE item_unit_id=?", fixture.unit.ID)
			case "allocation currency":
				execSQL(t, s.db, "UPDATE purchases SET currency='USD' WHERE id=?", fixture.purchase.ID)
			case "cost currency":
				execSQL(t, s.db, "INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,created_at) VALUES('foreign-cost',?,'2026-11-07T00:00:00Z','repair',1,'expense','USD','legacy')", fixture.unit.ID)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('cost','foreign-cost',1)")
			case "usage interpretation":
				execSQL(t, s.db, "UPDATE asset_profiles SET usage_unit='minutes' WHERE item_unit_id=?", fixture.unit.ID)
			case "disposal currency":
				execSQL(t, s.db, "INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,created_at) VALUES(?,'2026-11-07T00:00:00Z','sold',0,'USD','legacy')", fixture.unit.ID)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,1)", fixture.unit.ID)
			case "negative usage prefix":
				insertAssetUsage(t, s, "backdated", fixture.unit.ID, "2026-11-06T00:00:00Z", -1, &fixture.usage.ID)
			case "usage prefix overflow":
				insertAssetUsage(t, s, "too-many", fixture.unit.ID, "2026-11-06T00:00:00Z", maxSafeInteger, nil)
				insertAssetUsage(t, s, "later-correction", fixture.unit.ID, "2026-11-08T00:00:00Z", -250, &fixture.usage.ID)
			case "extra overflow":
				insertAssetCost(t, s, "too-much", fixture.unit.ID, maxSafeInteger, "expense")
			case "net overflow":
				execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=NULL WHERE item_unit_id=?", fixture.unit.ID)
				insertAssetCost(t, s, "net-too-large", fixture.unit.ID, maxSafeInteger-5000, "expense")
			case "projected overflow":
				execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=? WHERE item_unit_id=?", maxSafeInteger, fixture.unit.ID)
				insertAssetCost(t, s, "credit-too-large", fixture.unit.ID, maxSafeInteger, "credit")
			case "acquisition overflow":
				execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor) VALUES('extra-line',?,?,?,1,?)", fixture.purchase.ID, fixture.individual.ID, fixture.unit.ID, maxSafeInteger)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('purchase_line','extra-line',1)")
			}
			before := domainState(t, s)
			value, err := reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "")
			want := ErrDomainState
			if strings.Contains(defect, "overflow") {
				want = ErrAssetOverflow
			}
			if !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("incompatible/unsafe source returned a partial financial result or unrelated failure", defect, value, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestAssetSummaryManualBasisRejectsEvenZeroPaidExactUnitHistory(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	reader := assetReader(t, fixture)
	execSQL(t, s.db, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,created_at) VALUES('zero-purchase','Zero','2026-11-07T00:00:00Z','JPY','legacy')")
	execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor) VALUES('zero-line','zero-purchase',?,?,1,0)", fixture.individual.ID, fixture.unit.ID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('purchase','zero-purchase',1),('purchase_line','zero-line',1)")
	before := domainState(t, s)
	if value, err := reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, ""); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("zero-paid history was mistaken for no manual-basis reference", value, err)
	}
	requireDomainState(t, s, before)
}

func TestAssetSummaryRetainsUnknownCurrencyRepresentationAndCompleteJSONNulls(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	execSQL(t, fixture.store.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
	value, err := assetReader(t, fixture).CostSummary(t.Context(), fixture.token, fixture.unit.ID, "0001-01-01T00:00:00.000000001Z")
	if err != nil || value.Currency != "ZZZ" || value.AsOf != "0001-01-01T00:00:00.000000001Z" {
		t.Fatal("retained source was repriced or subjected to new-money registry policy", value, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(encoded, &object); err != nil || len(object) != 20 {
		t.Fatal("summary omitted or added contracted fields", string(encoded), err)
	}
	for _, field := range []string{"estimated_residual_minor", "projected_net_cost_minor", "elapsed_days", "per_day_minor", "per_use_minor", "projected_per_day_minor", "projected_per_use_minor"} {
		if value, present := object[field]; !present || value != nil {
			t.Fatal("required nullable summary field was absent or fabricated", field)
		}
	}
}
