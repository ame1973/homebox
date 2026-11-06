package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type unitCallTest struct {
	kind, subject string
	proof         int64
	create        UnitCreate
	patch         UnitPatch
	target        string
	status        UnitStatus
}

func unitCallsTest(f holdingFixture) []unitCallTest {
	return []unitCallTest{
		{kind: "create", subject: f.individual.ID, proof: f.individual.Revision, create: UnitCreate{LocationID: testPointer("source"), Notes: "Original"}},
		{kind: "patch", subject: f.unit.ID, proof: f.unit.Revision, patch: UnitPatch{Notes: testPointer("Changed")}},
		{kind: "move", subject: f.unit.ID, proof: f.unit.Revision, target: "target"},
		{kind: "status", subject: f.unit.ID, proof: f.unit.Revision, status: UnitStatus{Status: "in_use", Note: "Original note"}},
		{kind: "delete", subject: f.unit.ID, proof: f.unit.Revision},
	}
}

func callUnitTest(ctx context.Context, inventory *Inventory, token, key string, command unitCallTest) (domainResult, error) {
	switch command.kind {
	case "create":
		return inventory.CreateUnit(ctx, token, key, command.subject, command.proof, command.create)
	case "patch":
		return inventory.PatchUnit(ctx, token, key, command.subject, command.proof, command.patch)
	case "move":
		return inventory.MoveUnit(ctx, token, key, command.subject, command.proof, command.target)
	case "status":
		return inventory.SetUnitStatus(ctx, token, key, command.subject, command.proof, command.status)
	default:
		return inventory.DeleteUnit(ctx, token, key, command.subject, command.proof)
	}
}

func TestUnitOriginalCommandsBindSubjectsProofsEveryFieldAndOperationAfterLaterEditsOrDeletion(t *testing.T) {
	for _, kind := range []string{"create", "patch", "move", "status", "delete", "disposal"} {
		t.Run(kind, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			var original unitCallTest
			for _, candidate := range unitCallsTest(fixture) {
				if candidate.kind == kind {
					original = candidate
				}
			}
			if kind == "disposal" {
				original = unitCallTest{kind: "status", subject: fixture.unit.ID, proof: fixture.unit.Revision, status: UnitStatus{Status: "disposed", DisposalKind: testPointer("discarded"), Note: "Original"}}
			}
			first, err := callUnitTest(t.Context(), inventory, fixture.token, "unit-original-command", original)
			if err != nil {
				t.Fatal(err)
			}
			if unit, ok := first.(Unit); ok {
				if _, err = inventory.PatchUnit(t.Context(), fixture.token, "unit-later-command", unit.ID, unit.Revision, UnitPatch{Notes: testPointer("Later metadata")}); err != nil {
					t.Fatal(err)
				}
			}
			_, fresh := login(t, fixture.auth)
			before := domainState(t, fixture.store)
			if frozen, err := callUnitTest(t.Context(), inventory, fresh, "unit-original-command", original); err != nil || !reflect.DeepEqual(frozen, first) {
				t.Fatal("same-principal replay fetched/rebased current source", err)
			}
			requireDomainState(t, fixture.store, before)
			fields := []string{"subject", "proof", "operation"}
			switch kind {
			case "create":
				fields = append(fields, "location", "serial", "notes", "initial status")
			case "patch":
				fields = append(fields, "notes", "serial presence")
			case "move":
				fields = append(fields, "target")
			case "status":
				fields = append(fields, "status", "memo")
			case "disposal":
				fields = append(fields, "kind", "memo")
			}
			for _, field := range fields {
				changed := original
				switch field {
				case "subject":
					changed.subject += "-other"
				case "proof":
					changed.proof++
				case "operation":
					changed.kind = "move"
					changed.target = "root"
					if original.kind == "move" {
						changed.kind = "patch"
						changed.patch = UnitPatch{Notes: testPointer("Different operation")}
					}
				case "location":
					changed.create.LocationID = nil
				case "serial":
					changed.create.SerialNumber = testPointer("")
				case "notes":
					if kind == "create" {
						changed.create.Notes += "different"
					} else {
						changed.patch.Notes = testPointer("Different notes")
					}
				case "initial status":
					changed.create.Status = testPointer("stored")
				case "serial presence":
					changed.patch.SerialNumber = NullableString{Set: true}
				case "target":
					changed.target = "root"
				case "status":
					changed.status.Status = "lost"
				case "kind":
					changed.status.DisposalKind = testPointer("sold")
				case "memo":
					changed.status.Note += "different"
				}
				if value, err := callUnitTest(t.Context(), inventory, fresh, "unit-original-command", changed); !errors.Is(err, ErrIdempotencyConflict) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("changed original reused an immutable Unit key", field, err)
				}
				requireDomainState(t, fixture.store, before)
			}
		})
	}
}

func TestUnitCommandsRequireOriginalItemCreationButIndependentRowEditProofs(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	setCatalogProjectionRevision(t, fixture.store, "item", fixture.individual.ID, 7)
	unit, err := inventory.CreateUnit(t.Context(), fixture.token, "unit-independent-create", fixture.individual.ID, 7, UnitCreate{})
	if err != nil || unit.Revision != 1 || getCatalogItem(t, fixture.catalog, fixture.token, unit.ItemID).Revision != 8 {
		t.Fatal("Unit response was confused with Item-proof-plus-one", err)
	}
	if _, err = fixture.catalog.Patch(t.Context(), fixture.token, "unit-owner-later-title", unit.ItemID, 8, ItemPatch{Name: testPointer("Later owner")}); err != nil {
		t.Fatal(err)
	}
	unit, err = inventory.PatchUnit(t.Context(), fixture.token, "unit-own-proof-after-owner", unit.ID, unit.Revision, UnitPatch{Notes: testPointer("Independent row edit")})
	if err != nil || unit.Revision != 2 || !strings.Contains(projectedSearchText(t, fixture.store, "unit", unit.ID), "Later owner") {
		t.Fatal("independent row edit acquired an extra stale Item precondition or copied old owner text", err)
	}
	before := domainState(t, fixture.store)
	if _, err = inventory.PatchUnit(t.Context(), fixture.token, "wrong-unit-proof-domain", unit.ID, 10, UnitPatch{Notes: testPointer("Wrong proof")}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("Item aggregate proof authorized a Unit edit", err)
	}
	if _, err = inventory.CreateUnit(t.Context(), fixture.token, "wrong-unit-tracking-mode", fixture.quantity.ID, fixture.quantity.Revision, UnitCreate{}); !errors.Is(err, ErrUnitTracking) {
		t.Fatal("quantity Item created an individual Unit", err)
	}
	requireDomainState(t, fixture.store, before)
}

func TestUnitTypedAdmissionRejectsInvalidIdentityProofsAndPerCommandBounds(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for _, original := range unitCallsTest(fixture) {
		for _, invalid := range []string{"id empty", "id wide", "id UTF", "proof absent", "proof negative", "proof unsafe", "key short", "key wide", "key UTF", "notes wide", "notes UTF", "serial wide", "serial UTF", "presence", "empty patch", "location wide", "location UTF", "location empty", "status unknown", "terminal initial", "memo wide", "memo UTF", "missing kind", "unknown kind", "unexpected kind"} {
			if strings.HasPrefix(invalid, "notes") && original.kind != "create" && original.kind != "patch" || strings.HasPrefix(invalid, "serial") && original.kind != "create" && original.kind != "patch" ||
				(invalid == "presence" || invalid == "empty patch") && original.kind != "patch" || strings.HasPrefix(invalid, "location") && original.kind != "create" && original.kind != "move" ||
				invalid == "status unknown" && original.kind != "create" && original.kind != "status" || invalid == "terminal initial" && original.kind != "create" ||
				(strings.HasPrefix(invalid, "memo") || strings.HasSuffix(invalid, "kind")) && original.kind != "status" {
				continue
			}
			t.Run(original.kind+"/"+invalid, func(t *testing.T) {
				command, key, want := original, "invalid-unit-command", ErrDomainInput
				bad := strings.Repeat("界", 10001)
				switch invalid {
				case "id empty":
					command.subject = ""
				case "id wide":
					command.subject = strings.Repeat("界", 129)
				case "id UTF":
					command.subject = string([]byte{0xff})
				case "proof absent":
					command.proof = 0
					want = ErrPreconditionRequired
				case "proof negative":
					command.proof = -1
				case "proof unsafe":
					command.proof = maxSafeInteger + 1
				case "key short":
					key = "short"
				case "key wide":
					key = strings.Repeat("界", 129)
				case "key UTF":
					key = "invalid-" + string([]byte{0xff})
				case "notes wide", "notes UTF":
					if invalid == "notes UTF" {
						bad = string([]byte{0xff})
					}
					if command.kind == "create" {
						command.create.Notes = bad
					} else {
						command.patch.Notes = &bad
					}
				case "serial wide", "serial UTF":
					if invalid == "serial UTF" {
						bad = string([]byte{0xff})
					}
					if command.kind == "create" {
						command.create.SerialNumber = &bad
					} else {
						command.patch.SerialNumber = NullableString{Set: true, Value: &bad}
					}
				case "presence":
					command.patch.SerialNumber = NullableString{Value: testPointer("unexpected")}
				case "empty patch":
					command.patch = UnitPatch{}
				case "location wide", "location UTF", "location empty":
					bad = strings.Repeat("界", 129)
					if invalid == "location UTF" {
						bad = string([]byte{0xff})
					} else if invalid == "location empty" {
						bad = ""
					}
					if command.kind == "create" {
						command.create.LocationID = &bad
					} else {
						command.target = bad
					}
				case "status unknown":
					if command.kind == "create" {
						command.create.Status = testPointer("unknown")
					} else {
						command.status.Status = "unknown"
					}
				case "terminal initial":
					command.create.Status = testPointer("retired")
				case "memo wide":
					command.status.Note = bad
				case "memo UTF":
					command.status.Note = string([]byte{0xff})
				case "missing kind":
					command.status = UnitStatus{Status: "disposed"}
				case "unknown kind":
					command.status = UnitStatus{Status: "disposed", DisposalKind: testPointer("other")}
				case "unexpected kind":
					command.status.DisposalKind = testPointer("discarded")
				}
				before := domainState(t, fixture.store)
				if value, err := callUnitTest(t.Context(), inventory, fixture.token, key, command); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("invalid Unit input released a partial result", err)
				}
				requireDomainState(t, fixture.store, before)
			})
		}
	}
}

func TestUnitAllCommandsFrozenRepliesAndCancellationRequireCurrentCompletedSessions(t *testing.T) {
	for _, state := range []string{"mandatory", "revoked", "expired", "epoch", "invalid", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			commands := unitCallsTest(fixture)
			for index, command := range commands {
				if command.kind == "create" {
					command.proof = getCatalogItem(t, fixture.catalog, fixture.token, command.subject).Revision
				} else {
					current, err := inventory.GetUnit(t.Context(), fixture.token, command.subject)
					if err != nil {
						t.Fatal(err)
					}
					command.proof = current.Revision
				}
				if _, err := callUnitTest(t.Context(), inventory, fixture.token, "authorized-unit-"+command.kind, command); err != nil {
					t.Fatal(err)
				}
				commands[index] = command
			}
			want := ErrAuthentication
			ctx := t.Context()
			switch state {
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, fixture.store.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "expired":
				execSQL(t, fixture.store.db, "UPDATE sessions SET expires_at=?", stamp(fixture.auth.now()))
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='other-unit-epoch'")
			case "invalid":
				fixture.token = "invalid"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			before := domainState(t, fixture.store)
			for _, command := range commands {
				for _, prefix := range []string{"authorized-unit-", "unauthorized-unit-"} {
					if value, err := callUnitTest(ctx, inventory, fixture.token, prefix+command.kind, command); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
						t.Fatal("Unit write/replay escaped SQL authorization/cancellation", command.kind, err)
					}
					requireDomainState(t, fixture.store, before)
				}
			}
		})
	}
}

func TestUnitExpiredOriginalClaimsCannotRecreateDeletedSourceOrRepeatPhysicalEffects(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	unit := createUnitTest(t, fixture, inventory, "unit-expiry-create", nil)
	if _, err := inventory.DeleteUnit(t.Context(), fixture.token, "unit-expiry-delete", unit.ID, unit.Revision); err != nil {
		t.Fatal(err)
	}
	now := fixture.auth.now().Add(domainReplyLifetime)
	fixture.auth.now = func() time.Time { return now }
	_, fixture.token = login(t, fixture.auth)
	before := domainState(t, fixture.store)
	if value, err := inventory.CreateUnit(t.Context(), fixture.token, "unit-expiry-create", fixture.individual.ID, fixture.individual.Revision, UnitCreate{}); !errors.Is(err, ErrIdempotencyExpired) || value != (Unit{}) {
		t.Fatal("expired creation recreated a removed Unit", err)
	}
	if ack, err := inventory.DeleteUnit(t.Context(), fixture.token, "unit-expiry-delete", unit.ID, unit.Revision); !errors.Is(err, ErrIdempotencyExpired) || ack.OK {
		t.Fatal("expired deletion became another operation", err)
	}
	requireDomainState(t, fixture.store, before)
}

func TestUnitConcurrentOriginalKeysAndCompetingReviewedProofsHaveOnePhysicalCommit(t *testing.T) {
	for _, kind := range []string{"create", "patch", "move", "status", "delete"} {
		for _, duplicate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/duplicate=%t", kind, duplicate), func(t *testing.T) {
				fixture, inventory := inventoryFixture(t)
				var command unitCallTest
				for _, candidate := range unitCallsTest(fixture) {
					if candidate.kind == kind {
						command = candidate
					}
				}
				type outcome struct {
					value domainResult
					err   error
				}
				start, results := make(chan struct{}), make(chan outcome, 8)
				for index := range 8 {
					go func(index int) {
						<-start
						key := "concurrent-unit-original"
						if !duplicate {
							key = fmt.Sprintf("competing-unit-%d", index)
						}
						value, err := callUnitTest(t.Context(), inventory, fixture.token, key, command)
						results <- outcome{value, err}
					}(index)
				}
				close(start)
				successes, conflicts := 0, 0
				var first domainResult
				for range 8 {
					result := <-results
					if result.err == nil {
						if successes > 0 && !reflect.DeepEqual(first, result.value) {
							t.Fatal("same original returned different frozen Unit state")
						}
						first = result.value
						successes++
					} else if !duplicate && (errors.Is(result.err, ErrRevisionConflict) || kind == "delete" && errors.Is(result.err, ErrNotFound)) && reflect.ValueOf(result.value).IsZero() {
						conflicts++
					} else {
						t.Fatal("unexpected concurrent Unit outcome", result.err)
					}
				}
				want := 1
				if duplicate {
					want = 8
				}
				if successes != want || conflicts != 8-want || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events") != 1 {
					t.Fatal("concurrent reviewed Unit commands duplicated physical effects")
				}
			})
		}
	}
}
