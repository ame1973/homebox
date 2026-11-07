package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAssetReadConstructorsAndAdmissionRequireActualStoreAndExactInputs(t *testing.T) {
	fixture := assetsFixture(t, true)
	reader := assetReader(t, fixture.holdingFixture)
	for _, construct := range []func() (*Assets, error){
		func() (*Assets, error) { return NewAssets(nil) },
		func() (*Assets, error) { return NewAssets(&Auth{}) },
		func() (*Assets, error) { return fixture.store.NewAssets(nil) },
		func() (*Assets, error) { return (*Store)(nil).NewAssets(fixture.auth) },
		func() (*Assets, error) { return (&Store{}).NewAssets(fixture.auth) },
	} {
		if value, err := construct(); !errors.Is(err, ErrDomainInput) || value != nil {
			t.Fatal("financial reader accepted absent/foreign authority", err)
		}
	}
	if value, err := NewAssets(fixture.auth); err != nil || value.auth != fixture.auth {
		t.Fatal("normal Assets constructor did not retain actual authority", err)
	}
	before := domainState(t, fixture.store)
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if value, err := reader.Get(t.Context(), fixture.token, id); !errors.Is(err, ErrDomainInput) || value != (Asset{}) {
			t.Fatal("invalid Asset identity exposed source", err)
		}
		if value, err := reader.GetDisposal(t.Context(), fixture.token, id); !errors.Is(err, ErrDomainInput) || value != (Disposal{}) {
			t.Fatal("invalid Disposal identity exposed source", err)
		}
		if value, err := reader.CostSummary(t.Context(), fixture.token, id, ""); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid summary identity exposed source", err)
		}
	}
	for _, instant := range []string{"2026", "2026-11-07t00:00:00z", "2026-02-30T00:00:00Z", "2026-11-07T00:00:00.1234567890Z", "0001-01-01T00:00:00+00:01", "9999-12-31T23:59:59-00:01", "2026-11-07T00:00:00Z\n", string([]byte{0xff})} {
		if value, err := reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, instant); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid as_of was repaired or exposed a partial result", instant, err)
		}
	}
	for _, id := range []string{"absent", " unit", "new", "tree", "\x00"} {
		if value, err := reader.Get(t.Context(), fixture.token, id); !errors.Is(err, ErrNotFound) || value != (Asset{}) {
			t.Fatal("valid absent identity was reserved, trimmed or invented", err)
		}
		if value, err := reader.GetDisposal(t.Context(), fixture.token, id); !errors.Is(err, ErrNotFound) || value != (Disposal{}) {
			t.Fatal("absent disposal was reconstructed from status", err)
		}
		if value, err := reader.CostSummary(t.Context(), fixture.token, id, ""); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("missing actual Asset became implicit zero acquisition", err)
		}
	}
	requireDomainState(t, fixture.store, before)
}

func TestAssetReadAuthorizationFinalSessionWriteAndCommitFailuresWithholdAllResources(t *testing.T) {
	for _, failure := range []string{"mandatory", "revoked", "epoch", "invalid", "expired", "canceled", "session write", "deferred commit", "late expiry"} {
		t.Run(failure, func(t *testing.T) {
			fixture := assetsFixture(t, true)
			s := fixture.store
			reader := assetReader(t, fixture.holdingFixture)
			ctx, token := t.Context(), fixture.token
			want := ErrAuthentication
			base := fixture.auth.now().Add(time.Minute)
			fixture.auth.now = func() time.Time { return base }
			switch failure {
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(base))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='other-asset-read-epoch'")
			case "invalid":
				token = "invalid"
			case "expired":
				execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(base))
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "session write":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'Asset read failure'); END")
				want = nil
			case "deferred commit":
				execSQL(t, s.db, "CREATE TABLE asset_read_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_read AFTER UPDATE ON sessions BEGIN INSERT INTO asset_read_commit_guard VALUES('missing'); END")
				want = nil
			}
			before := domainState(t, s)
			for _, read := range []func() (any, error){
				func() (any, error) { return reader.Get(ctx, token, fixture.unit.ID) },
				func() (any, error) { return reader.GetDisposal(ctx, token, fixture.unit.ID) },
				func() (any, error) { return reader.CostSummary(ctx, token, fixture.unit.ID, "2026-11-07T00:00:00Z") },
				func() (any, error) { return reader.Page(ctx, token, "", 50, AssetFilters{}) },
				func() (any, error) { return reader.PageUsage(ctx, token, fixture.unit.ID, "", 50) },
				func() (any, error) { return reader.PageCosts(ctx, token, fixture.unit.ID, "", 50) },
			} {
				if failure == "late expiry" {
					calls := 0
					fixture.auth.now = func() time.Time {
						calls++
						if calls > 1 {
							return base.Add(31 * 24 * time.Hour)
						}
						return base
					}
				}
				if value, err := read(); err == nil || want != nil && !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("failed authorization/session/COMMIT released financial data", failure, value, err)
				}
				requireDomainState(t, s, before)
			}
			if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_read_commit_guard") != 0 {
				t.Fatal("failed read COMMIT leaked deferred state")
			}
		})
	}
}

func TestAssetSummaryRequiresEveryRealSourceCounterWithoutRepairingMissingAuthority(t *testing.T) {
	for _, kind := range []string{"asset", "unit", "item", "purchase", "purchase_line", "usage", "cost", "disposal", "catalog revision"} {
		t.Run(kind, func(t *testing.T) {
			fixture := assetsFixture(t, true)
			s := fixture.store
			reader := assetReader(t, fixture.holdingFixture)
			if kind == "catalog revision" {
				execSQL(t, s.db, "DELETE FROM data_revision WHERE scope='catalog'")
			} else {
				id := fixture.unit.ID
				switch kind {
				case "item":
					id = fixture.individual.ID
				case "purchase":
					id = fixture.purchase.ID
				case "purchase_line":
					id = fixture.unitLine.ID
				case "usage":
					id = fixture.usage.ID
				case "cost":
					id = fixture.cost.ID
				}
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", kind, id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, id)
			}
			before := domainState(t, s)
			if value, err := reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, ""); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("missing financial authority was seeded or treated as empty", kind, value, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestAssetSummarySQLTimezoneRevisionAndClockFailuresNeverExposePartialTotals(t *testing.T) {
	for _, failure := range []string{"purchase_lines", "purchases", "asset_cost_events", "asset_usage_events", "asset_disposals", "app_settings", "data_revision", "invalid timezone", "null timezone", "unsafe revision", "negative revision", "invalid default clock"} {
		t.Run(failure, func(t *testing.T) {
			fixture := assetsFixture(t, true)
			s := fixture.store
			reader := assetReader(t, fixture.holdingFixture)
			var inject string
			switch failure {
			case "invalid timezone":
				execSQL(t, s.db, `INSERT INTO app_settings VALUES('preferences.timezone','"Local"','2026')`)
			case "null timezone":
				execSQL(t, s.db, `INSERT INTO app_settings VALUES('preferences.timezone','null','2026')`)
			case "unsafe revision":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger+1)
			case "negative revision":
				// A private view injects invalid read representation without disabling
				// or changing the production data_revision CHECK constraint.
				inject = "ALTER TABLE data_revision RENAME TO saved_asset_read_revision; CREATE VIEW data_revision AS SELECT 'catalog' AS scope,-1 AS revision"
			case "invalid default clock":
				base := fixture.auth.now()
				calls := 0
				fixture.auth.now = func() time.Time {
					calls++
					if calls > 1 {
						return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
					}
					return base
				}
			default:
				inject = "ALTER TABLE " + failure + " RENAME TO unavailable_asset_read_source"
			}
			before := domainState(t, s)
			var value AssetCostSummary
			var err error
			if inject == "" {
				value, err = reader.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "")
			} else {
				value, err = readDomain(t.Context(), fixture.auth, fixture.token, func(tx *sql.Tx) (AssetCostSummary, error) {
					if _, err := tx.ExecContext(t.Context(), inject); err != nil {
						t.Fatal(err)
					}
					return loadAssetCostSummary(t.Context(), tx, fixture.unit.ID, fixture.auth.now())
				})
			}
			if err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("failed financial source/timezone/revision/clock returned a partial result", failure, value, err)
			}
			requireDomainState(t, s, before)
		})
	}
}
