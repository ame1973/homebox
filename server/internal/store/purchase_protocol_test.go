package store

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPurchaseCommandsRejectInvalidInputBeforeAnyDomainSideEffect(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	valid := PurchaseCreate{MerchantName: "Merchant", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"}
	for _, input := range []PurchaseCreate{
		{}, {MerchantName: "Merchant", PurchasedAt: "2026-11-06", Currency: "JPY"},
		{MerchantName: "Merchant", PurchasedAt: "2026-11-06T00:00:00.1234567890Z", Currency: "JPY"},
		{MerchantName: strings.Repeat("😀", 301), PurchasedAt: valid.PurchasedAt, Currency: "JPY"},
		{MerchantName: string([]byte{0xff}), PurchasedAt: valid.PurchasedAt, Currency: "JPY"},
		{MerchantName: "Merchant", PurchasedAt: valid.PurchasedAt, Currency: "jpy"},
		{MerchantName: "Merchant", PurchasedAt: valid.PurchasedAt, Currency: "JPY", ReceiptNote: strings.Repeat("😀", 10001)},
	} {
		before := purchaseCommandState(t, s)
		if value, err := p.Create(t.Context(), fixture.token, "invalid-create", input); !errors.Is(err, ErrDomainInput) || value != (Purchase{}) {
			t.Fatal("invalid create accepted", err)
		}
		requirePurchaseCommandState(t, s, before)
	}
	for _, input := range []PurchasePatch{{}, {MerchantName: testPointer("")}, {MerchantName: testPointer(strings.Repeat("x", 301))}, {PurchasedAt: testPointer("2026")}, {Currency: testPointer("JP\n")}, {ReceiptNote: testPointer(strings.Repeat("x", 10001))}, {ReceiptNote: testPointer(string([]byte{0xff}))}} {
		before := purchaseCommandState(t, s)
		if value, err := p.Patch(t.Context(), fixture.token, "invalid-patch", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Purchase{}) {
			t.Fatal("invalid patch accepted", err)
		}
		requirePurchaseCommandState(t, s, before)
	}
	base := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: testPointer(fixture.unit.ID), QuantityBase: 1, PaidMinor: 0}
	for _, field := range []string{"item", "item proof", "nullable Unit", "quantity", "amount", "note"} {
		input := base
		switch field {
		case "item":
			input.ItemID = ""
		case "item proof":
			input.ItemRevision = 0
		case "nullable Unit":
			input.ItemUnitID = testPointer("")
		case "quantity":
			input.QuantityBase = 2
		case "amount":
			input.PaidMinor = -1
		case "note":
			input.Note = strings.Repeat("😀", 10001)
		}
		before := purchaseCommandState(t, s)
		if value, err := p.CreateLine(t.Context(), fixture.token, "invalid-line", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrDomainInput) || value != (PurchaseLine{}) {
			t.Fatal("invalid allocation accepted", field, err)
		}
		requirePurchaseCommandState(t, s, before)
	}
	for _, number := range []int64{-1, maxSafeInteger + 1} {
		for _, field := range []string{"item proof", "quantity", "amount"} {
			input := base
			input.ItemUnitID = nil
			switch field {
			case "item proof":
				input.ItemRevision = number
			case "quantity":
				input.QuantityBase = number
			case "amount":
				input.PaidMinor = number
			}
			before := purchaseCommandState(t, s)
			if value, err := p.CreateLine(t.Context(), fixture.token, "unsafe-allocation", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrDomainInput) || value != (PurchaseLine{}) {
				t.Fatal("unsafe allocation field accepted", field, err)
			}
			requirePurchaseCommandState(t, s, before)
		}
	}
	for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
		want := ErrDomainInput
		if revision == 0 {
			want = ErrPreconditionRequired
		}
		before := purchaseCommandState(t, s)
		if value, err := p.Patch(t.Context(), fixture.token, "missing-purchase-proof", fixture.purchase.ID, revision, PurchasePatch{ReceiptNote: testPointer("")}); !errors.Is(err, want) || value != (Purchase{}) {
			t.Fatal("bad header proof accepted", err)
		}
		if value, err := p.CreateLine(t.Context(), fixture.token, "missing-line-parent-proof", fixture.purchase.ID, revision, base); !errors.Is(err, want) || value != (PurchaseLine{}) {
			t.Fatal("bad allocation parent proof accepted", err)
		}
		requirePurchaseCommandState(t, s, before)
	}
	for _, key := range []string{"short", strings.Repeat("x", 129)} {
		before := purchaseCommandState(t, s)
		if value, err := p.Create(t.Context(), fixture.token, key, valid); !errors.Is(err, ErrDomainInput) || value != (Purchase{}) {
			t.Fatal("unbounded/short domain key accepted", err)
		}
		requirePurchaseCommandState(t, s, before)
	}
}

func TestPurchaseAllocationTrackingOwnershipAndAssetDependenciesAreExplicitGuards(t *testing.T) {
	for _, mode := range []string{"quantity Unit", "foreign Unit", "missing Unit", "manual asset", "foreign asset currency", "compatible asset", "unsafe manual source", "invalid asset currency"} {
		t.Run(mode, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			s := fixture.store
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: testPointer(fixture.unit.ID), QuantityBase: 1, PaidMinor: 0}
			want := ErrDomainInput
			switch mode {
			case "quantity Unit":
				input.ItemID = fixture.quantity.ID
				input.ItemRevision = fixture.quantity.Revision
				want = ErrUnitTracking
			case "foreign Unit":
				other := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Other owner"})
				input.ItemID = other.ID
				input.ItemRevision = other.Revision
			case "missing Unit":
				input.ItemUnitID = testPointer("absent")
				want = ErrNotFound
			default:
				basis, code := "purchase", "JPY"
				var manual any
				want = ErrPurchaseAssetLifecycle
				if mode == "manual asset" {
					basis = "manual"
					manual = int64(0)
					want = ErrPurchaseAssetBasis
				}
				if mode == "foreign asset currency" {
					code = "USD"
					want = ErrPurchaseCurrency
				}
				if mode == "unsafe manual source" {
					basis = "manual"
					manual = maxSafeInteger + 1
					want = ErrDomainState
				}
				if mode == "invalid asset currency" {
					code = "日本円"
					want = ErrDomainState
				}
				execSQL(t, s.db, "INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,manual_acquisition_minor,created_at,updated_at) VALUES(?,?,?,?, 'legacy','legacy')", fixture.unit.ID, basis, code, manual)
			}
			before := purchaseCommandState(t, s)
			if value, err := p.CreateLine(t.Context(), fixture.token, "guarded-allocation", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, want) || value != (PurchaseLine{}) {
				t.Fatal("allocation bypassed actual source/financial producer guard", err, want)
			}
			requirePurchaseCommandState(t, s, before)
		})
	}
}

func TestPurchaseOriginalReceiptsSurviveLaterSourceFreshLoginAndNewAssetGuardWithoutReplay(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	input := PurchaseCreate{MerchantName: "Original merchant", PurchasedAt: "2026-11-06T12:00:00+08:00", Currency: "JPY", ReceiptNote: "Exact original"}
	parent, err := p.Create(t.Context(), fixture.token, "original-purchase-create", input)
	if err != nil {
		t.Fatal(err)
	}
	command := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: testPointer(fixture.unit.ID), QuantityBase: 1, PaidMinor: 120000, Note: "Exact original allocation"}
	line, err := p.CreateLine(t.Context(), fixture.token, "original-purchase-line", parent.ID, parent.Revision, command)
	if err != nil {
		t.Fatal(err)
	}
	current, err := p.Get(t.Context(), fixture.token, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	patch := PurchasePatch{MerchantName: testPointer("Later actual merchant")}
	corrected, err := p.Patch(t.Context(), fixture.token, "original-purchase-correction", parent.ID, current.Revision, patch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Patch(t.Context(), fixture.token, "still-later-purchase", parent.ID, corrected.Revision, PurchasePatch{ReceiptNote: testPointer("Latest receipt")}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,created_at,updated_at) VALUES(?,'purchase','JPY','legacy','legacy')", fixture.unit.ID)
	_, fresh := login(t, fixture.auth)
	before := purchaseCommandState(t, s)
	if frozen, err := p.Create(t.Context(), fresh, "original-purchase-create", input); err != nil || frozen != parent {
		t.Fatal("creation did not retain its exact original reply", err)
	}
	if frozen, err := p.CreateLine(t.Context(), fresh, "original-purchase-line", parent.ID, parent.Revision, command); err != nil || !reflect.DeepEqual(frozen, line) {
		t.Fatal("current profile/counter changes blocked original-key financial reconciliation", err)
	}
	if frozen, err := p.Patch(t.Context(), fresh, "original-purchase-correction", parent.ID, current.Revision, patch); err != nil || frozen != corrected {
		t.Fatal("correction did not retain its original after-state", err)
	}
	requirePurchaseCommandState(t, s, before)
	changed := command
	changed.ItemRevision++
	if value, err := p.CreateLine(t.Context(), fresh, "original-purchase-line", parent.ID, parent.Revision, changed); !errors.Is(err, ErrIdempotencyConflict) || value != (PurchaseLine{}) {
		t.Fatal("changed interpretation proof reused a financial key", err)
	}
	if value, err := p.CreateLine(t.Context(), fresh, "original-purchase-line", parent.ID, parent.Revision+1, command); !errors.Is(err, ErrIdempotencyConflict) || value != (PurchaseLine{}) {
		t.Fatal("changed Purchase proof reused a financial key", err)
	}
	if value, err := p.Patch(t.Context(), fresh, "original-purchase-line", parent.ID, current.Revision, patch); !errors.Is(err, ErrIdempotencyConflict) || value != (Purchase{}) {
		t.Fatal("cross-operation financial key reused", err)
	}
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseExpiredDomainClaimsCannotBecomeNewSpendAfterFreshAuthentication(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	command := PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1, PaidMinor: 0}
	if _, err := p.CreateLine(t.Context(), fixture.token, "expiring-purchase-line", fixture.purchase.ID, fixture.purchase.Revision, command); err != nil {
		t.Fatal(err)
	}
	now := fixture.auth.now().Add(domainReplyLifetime + time.Second)
	fixture.auth.now = func() time.Time { return now }
	_, fresh := login(t, fixture.auth)
	before := purchaseCommandState(t, s)
	if value, err := p.CreateLine(t.Context(), fresh, "expiring-purchase-line", fixture.purchase.ID, fixture.purchase.Revision, command); !errors.Is(err, ErrIdempotencyExpired) || value != (PurchaseLine{}) {
		t.Fatal("expired original claim was deleted/reused to duplicate allocation", err)
	}
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseAllReadAndWriteServicesRequireCompletedActiveAuthorizationEvenForReceipts(t *testing.T) {
	for _, mode := range []string{"revoked", "mandatory", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			s := fixture.store
			input := PurchaseCreate{MerchantName: "Protected header", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"}
			if _, err := p.Create(t.Context(), fixture.token, "protected-original-header", input); err != nil {
				t.Fatal(err)
			}
			want := ErrAuthentication
			switch mode {
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at='2026-11-06T00:00:00Z'")
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='different-epoch'")
			}
			calls := []func() (domainResult, error){
				func() (domainResult, error) { return p.Get(t.Context(), fixture.token, fixture.purchase.ID) },
				func() (domainResult, error) {
					return p.Create(t.Context(), fixture.token, "protected-original-header", input)
				},
				func() (domainResult, error) {
					return p.Patch(t.Context(), fixture.token, "protected-patch", fixture.purchase.ID, fixture.purchase.Revision, PurchasePatch{ReceiptNote: testPointer("")})
				},
				func() (domainResult, error) {
					return p.CreateLine(t.Context(), fixture.token, "protected-line", fixture.purchase.ID, fixture.purchase.Revision, PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1, PaidMinor: 0})
				},
			}
			for _, call := range calls {
				before := purchaseCommandState(t, s)
				if value, err := call(); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("noncompleted session reached private source/frozen reply", mode, err)
				}
				requirePurchaseCommandState(t, s, before)
			}
		})
	}
}

func TestPurchaseActualCommandsNeverInventMissingOrStalePurchaseItemOrUnitAuthority(t *testing.T) {
	for _, scope := range []string{"missing Purchase", "missing Item", "stale Purchase", "stale Item", "Purchase counter", "Item counter", "Unit counter"} {
		t.Run(scope, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			s := fixture.store
			owner, revision := fixture.purchase.ID, fixture.purchase.Revision
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: testPointer(fixture.unit.ID), QuantityBase: 1, PaidMinor: 0}
			want := ErrDomainState
			switch scope {
			case "missing Purchase":
				owner = "absent"
				want = ErrNotFound
			case "missing Item":
				input.ItemID = "absent"
				want = ErrNotFound
			case "stale Purchase":
				revision++
				want = ErrRevisionConflict
			case "stale Item":
				input.ItemRevision++
				want = ErrRevisionConflict
			default:
				key := projectionKey{"purchase", fixture.purchase.ID}
				if scope == "Item counter" {
					key = projectionKey{"item", fixture.individual.ID}
				}
				if scope == "Unit counter" {
					key = projectionKey{"unit", fixture.unit.ID}
				}
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
			}
			before := purchaseCommandState(t, s)
			if value, err := p.CreateLine(t.Context(), fixture.token, "missing-actual-allocation-authority", owner, revision, input); !errors.Is(err, want) || value != (PurchaseLine{}) {
				t.Fatal("allocation invented/rebased missing or stale authority", scope, err)
			}
			if scope == "missing Purchase" || scope == "stale Purchase" || scope == "Purchase counter" {
				if value, err := p.Patch(t.Context(), fixture.token, "missing-actual-correction-authority", owner, revision, PurchasePatch{ReceiptNote: testPointer("")}); !errors.Is(err, want) || value != (Purchase{}) {
					t.Fatal("correction invented/rebased missing or stale authority", err)
				}
			}
			requirePurchaseCommandState(t, s, before)
		})
	}
	fixture, p := purchaseCommands(t)
	parent := createPurchaseCommand(t, p, fixture.token, "unsupported-unallocated-currency-parent", "JPY")
	before := purchaseCommandState(t, fixture.store)
	if value, err := p.Patch(t.Context(), fixture.token, "unsupported-unallocated-currency", parent.ID, parent.Revision, PurchasePatch{Currency: testPointer("XAU")}); !errors.Is(err, ErrPurchaseCurrency) || value != (Purchase{}) {
		t.Fatal("unsupported exponent became new unallocated money", err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
}

func TestPurchaseActualCommandsPreserveMaximumExactUnicodeAndIndependentMinorBaseIntegers(t *testing.T) {
	fixture, p := purchaseCommands(t)
	note := "\x00" + strings.Repeat("😀", 9999)
	parent, err := p.Create(t.Context(), fixture.token, "maximum-real-purchase", PurchaseCreate{MerchantName: strings.Repeat("界", 300), PurchasedAt: "0001-01-01T00:00:00Z", Currency: "CLF", ReceiptNote: note})
	if err != nil || parent.ReceiptNote != note || parent.PurchasedAt != "0001-01-01T00:00:00.000000000Z" {
		t.Fatal("exact maximum/source boundary was repaired", err)
	}
	input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, QuantityBase: maxSafeInteger, PaidMinor: maxSafeInteger, Note: note}
	line, err := p.CreateLine(t.Context(), fixture.token, "maximum-real-allocation", parent.ID, parent.Revision, input)
	if err != nil || line.Revision != 1 || line.ItemUnitID != nil || line.QuantityBase != maxSafeInteger || line.PaidMinor != maxSafeInteger || line.Note != note {
		t.Fatal("allocation scaled, multiplied, rounded or assigned an unallocated bulk count", err)
	}
}

func TestPurchaseConcurrentOriginalProofsCreateOneAllocationAndSameKeysReturnOneIdentity(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "competing proofs", true: "identical original key"}[sameKey], func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			s := fixture.store
			command := PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1, PaidMinor: 100}
			type result struct {
				line PurchaseLine
				err  error
			}
			results := make(chan result, 2)
			var wg sync.WaitGroup
			for index := 0; index < 2; index++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					key := "concurrent-purchase-a"
					if index == 1 && !sameKey {
						key = "concurrent-purchase-b"
					}
					line, err := p.CreateLine(t.Context(), fixture.token, key, fixture.purchase.ID, fixture.purchase.Revision, command)
					results <- result{line, err}
				}(index)
			}
			wg.Wait()
			close(results)
			var successful []PurchaseLine
			conflicts := 0
			for reply := range results {
				if reply.err == nil {
					successful = append(successful, reply.line)
				} else if errors.Is(reply.err, ErrRevisionConflict) {
					conflicts++
				} else {
					t.Fatal(reply.err)
				}
			}
			if sameKey {
				if len(successful) != 2 || !reflect.DeepEqual(successful[0], successful[1]) || conflicts != 0 {
					t.Fatal("identical requests did not share one original result")
				}
			} else if len(successful) != 1 || conflicts != 1 {
				t.Fatal("stale competing allocation did not fail atomically")
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_lines") != 3 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_purchases_id_lines'") != 1 {
				t.Fatal("concurrent calls duplicated financial spend or audit")
			}
		})
	}
}
