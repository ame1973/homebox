package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func locationFixture(t *testing.T) (*Store, *Locations, *Auth, string) {
	t.Helper()
	s, a, _, _, token := completedAuthFixture(t)
	locations, err := s.NewLocations(a)
	if err != nil {
		t.Fatal(err)
	}
	return s, locations, a, token
}

func testPointer[T any](value T) *T { return &value }

func createLocation(t *testing.T, locations *Locations, token, code string, parent *string) Location {
	t.Helper()
	var revision int64
	if parent != nil {
		value, err := locations.Get(t.Context(), token, *parent)
		if err != nil {
			t.Fatal(err)
		}
		revision = value.Revision
	}
	value, err := locations.Create(t.Context(), token, "create-"+code, revision, LocationCreate{Code: code, Name: "收納 " + code, ParentID: parent})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func getLocation(t *testing.T, locations *Locations, token, id string) Location {
	t.Helper()
	value, err := locations.Get(t.Context(), token, id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestLocationCreatePublishesOnlyTheCanonicalFrozenProjection(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	input := LocationCreate{Name: "  書房箱 📦 é  ", Code: "BOX-1", Description: "<script>notes</script>\n原樣保留", SortOrder: -maxSafeInteger}
	created, err := locations.Create(t.Context(), token, "location-create-key", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(created.ID) ||
		created.Revision != 1 || created.Kind != "other" || created.Status != "active" || created.ParentID != nil || created.Name != input.Name || created.Description != input.Description {
		t.Fatalf("creation did not preserve typed metadata: %+v", created)
	}
	if got := getLocation(t, locations, token, created.ID); !reflect.DeepEqual(got, created) {
		t.Fatal("authoritative read differs from the committed result")
	}
	frozen := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes")
	current := scalar[string](t, s.db, "SELECT payload_json FROM current_projections")
	var payload map[string]any
	if json.Unmarshal([]byte(frozen), &payload) != nil || len(payload) != 8 || payload["id"] != created.ID || payload["revision"] != nil || frozen != current {
		t.Fatal("outbox was not a frozen, exact version-one payload")
	}
	if scalar[int](t, s.db, "SELECT revision FROM data_revision WHERE scope='catalog'") != 1 ||
		scalar[int](t, s.db, "SELECT SUM(revision) FROM data_revision WHERE scope<>'catalog'") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 1 {
		t.Fatal("creation did not atomically invalidate its catalog or append an event")
	}
	for _, table := range []string{"api_idempotency", "mutation_events"} {
		column := "response_json"
		if table == "mutation_events" {
			column = "result_json"
		}
		body := scalar[string](t, s.db, "SELECT "+column+" FROM "+table)
		if strings.Contains(body, token) || strings.Contains(body, testAdminPassword) {
			t.Fatal("domain persistence included an authentication credential")
		}
	}
	if scalar[string](t, s.db, "SELECT key FROM api_idempotency") == "location-create-key" {
		t.Fatal("domain idempotency did not namespace the caller's key")
	}
}

func TestLocationParentCreationChecksAndAdvancesAggregateRevisions(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	root := createLocation(t, locations, token, "ROOT", nil)
	child := createLocation(t, locations, token, "CHILD", &root.ID)
	if getLocation(t, locations, token, root.ID).Revision != 2 || child.Revision != 1 {
		t.Fatal("child creation did not advance its parent")
	}
	input := LocationCreate{Code: "SIBLING", Name: "另一個箱子", ParentID: &root.ID}
	if _, err := locations.Create(t.Context(), token, "stale-parent-key", root.Revision, input); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("a stale parent revision created a child", err)
	}
	grandchild := createLocation(t, locations, token, "GRANDCHILD", &child.ID)
	if getLocation(t, locations, token, root.ID).Revision != 3 || getLocation(t, locations, token, child.ID).Revision != 2 || grandchild.Revision != 1 {
		t.Fatal("deep membership changes did not invalidate all ancestors")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 6 || scalar[int](t, s.db, "SELECT revision FROM data_revision WHERE scope='catalog'") != 3 {
		t.Fatal("one logical transaction emitted duplicate revisions or catalog invalidations")
	}
	if _, err := locations.Archive(t.Context(), token, "stale-subtree-key", root.ID, 2, true, "reviewed before grandchild creation"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("a stale subtree confirmation archived a newly added descendant", err)
	}
}

func TestLocationIdempotencyIsFrozenAcrossSessionsButStillRequiresAuthorization(t *testing.T) {
	s, locations, a, token := locationFixture(t)
	input := LocationCreate{Code: "BOX-1", Name: "原始名稱"}
	first, err := locations.Create(t.Context(), token, "shared-create-key", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := locations.Patch(t.Context(), token, "rename-location-key", first.ID, first.Revision, LocationPatch{Name: testPointer("修改之後")})
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken := login(t, a)
	replay, err := locations.Create(t.Context(), otherToken, "shared-create-key", 0, input)
	if err != nil || !reflect.DeepEqual(replay, first) || updated.Name == replay.Name {
		t.Fatal("a fresh active session did not receive the original acknowledgement", err)
	}
	input.Name = "不同請求"
	if _, err = locations.Create(t.Context(), token, "shared-create-key", 0, input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("changed body reused a domain key", err)
	}
	if _, err = locations.Move(t.Context(), token, "shared-create-key", first.ID, updated.Revision, nil); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("another operation reused a domain key", err)
	}
	if _, err = a.Logout(t.Context(), token, "logout-domain-key"); err != nil {
		t.Fatal(err)
	}
	input.Name = first.Name
	if _, err = locations.Create(t.Context(), token, "shared-create-key", 0, input); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a revoked cookie read a frozen domain response", err)
	}
	if _, err = locations.Get(t.Context(), token, first.ID); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a revoked cookie read domain data", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM locations") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 2 {
		t.Fatal("domain retry duplicated business effects")
	}
}

func TestLocationWritesRejectMandatoryExpiredAndWrongEpochSessions(t *testing.T) {
	for _, state := range []string{"mandatory", "revoked", "expired", "epoch", "invalid"} {
		t.Run(state, func(t *testing.T) {
			s, locations, a, token := locationFixture(t)
			want := ErrAuthentication
			switch state {
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(a.now()))
			case "expired":
				execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(a.now()))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='another-dataset'")
			case "invalid":
				token = "invalid"
			}
			if result, err := locations.Create(t.Context(), token, "denied-location-key", 0, LocationCreate{Code: "BOX", Name: "Not published"}); !errors.Is(err, want) || result.ID != "" {
				t.Fatal("unauthorized domain creation was not rejected", err)
			}
			if _, err := locations.Get(t.Context(), token, "missing-location"); !errors.Is(err, want) {
				t.Fatal("domain reads bypassed the same authorization gate", err)
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM locations") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 {
				t.Fatal("a rejected session changed domain data")
			}
		})
	}
}

func TestLocationMoveUpdatesOnlyLinksAndFreezesMovementHistory(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	from := createLocation(t, locations, token, "FROM", nil)
	to := createLocation(t, locations, token, "TO", nil)
	container := createLocation(t, locations, token, "BOX", &from.ID)
	child := createLocation(t, locations, token, "INNER", &container.ID)
	container = getLocation(t, locations, token, container.ID)
	execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('stock-item','Cable','quantity','2026','2026'),('unit-item','Camera','individual','2026','2026')`)
	execSQL(t, s.db, "INSERT INTO stock_balances VALUES('stock-item',?,1500,'2026')", container.ID)
	execSQL(t, s.db, "INSERT INTO item_units(id,item_id,location_id,asset_code,created_at,updated_at) VALUES('unit-1','unit-item',?,'UNIT-1','2026','2026')", child.ID)
	beforeChanges := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes")
	moved, err := locations.Move(t.Context(), token, "move-container-key", container.ID, container.Revision, &to.ID)
	if err != nil || moved.ParentID == nil || *moved.ParentID != to.ID || moved.Revision != container.Revision+1 {
		t.Fatal("container move failed", err)
	}
	if getLocation(t, locations, token, child.ID).Revision != child.Revision ||
		scalar[string](t, s.db, "SELECT location_id FROM stock_balances") != container.ID ||
		scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances") != 1500 ||
		scalar[string](t, s.db, "SELECT location_id FROM item_units") != child.ID {
		t.Fatal("moving a container rewrote descendants or inventory locations")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != beforeChanges+4 ||
		getLocation(t, locations, token, from.ID).Revision != 4 || getLocation(t, locations, token, to.ID).Revision != 2 {
		t.Fatal("move did not publish exactly its source, destination, container, and movement")
	}
	frozen := scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE entity_type='location_event'")
	var movement locationMovement
	if json.Unmarshal([]byte(frozen), &movement) != nil || movement.LocationID != container.ID || movement.FromParentID == nil || *movement.FromParentID != from.ID || movement.ToParentID == nil || *movement.ToParentID != to.ID {
		t.Fatal("movement history was not frozen correctly")
	}
	if _, err = locations.Patch(t.Context(), token, "later-rename-key", container.ID, moved.Revision, LocationPatch{Name: testPointer("後來的名稱")}); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE entity_type='location_event'") != frozen {
		t.Fatal("a later mutation changed an old frozen event")
	}
	for _, table := range []string{"location_events", "mutation_events"} {
		for _, query := range []string{"DELETE FROM " + table, "UPDATE " + table + " SET id=id"} {
			if _, err = s.db.ExecContext(t.Context(), query); err == nil {
				t.Fatal("immutable history accepted an update/delete")
			}
		}
	}
}

func TestLocationCycleArchivedDestinationAndDuplicateCodeGuards(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	root := createLocation(t, locations, token, "ROOT", nil)
	child := createLocation(t, locations, token, "CHILD", &root.ID)
	archived := createLocation(t, locations, token, "ARCHIVED", nil)
	archived, err := locations.Archive(t.Context(), token, "archive-empty-key", archived.ID, archived.Revision, false, "")
	if err != nil {
		t.Fatal(err)
	}
	root = getLocation(t, locations, token, root.ID)
	for _, input := range []struct {
		name   string
		parent *string
		want   error
	}{{"self", &root.ID, ErrLocationCycle}, {"descendant", &child.ID, ErrLocationCycle}, {"archived", &archived.ID, ErrLocationArchived}, {"missing", testPointer("missing"), ErrNotFound}} {
		if _, err = locations.Move(t.Context(), token, "move-guard-"+input.name, root.ID, root.Revision, input.parent); !errors.Is(err, input.want) {
			t.Fatal("incorrect hierarchy guard", input.name, err)
		}
	}
	if _, err = locations.Create(t.Context(), token, "archived-parent-key", archived.Revision, LocationCreate{ParentID: &archived.ID, Code: "NEW", Name: "Child"}); !errors.Is(err, ErrLocationArchived) {
		t.Fatal("created a child in an archived destination", err)
	}
	if _, err = locations.Create(t.Context(), token, "duplicate-code-key", 0, LocationCreate{Code: root.Code, Name: "Duplicate"}); !errors.Is(err, ErrLocationCode) {
		t.Fatal("duplicate location code was accepted", err)
	}
	if _, err = locations.Patch(t.Context(), token, "duplicate-edit-key", child.ID, child.Revision, LocationPatch{Code: &root.Code}); !errors.Is(err, ErrLocationCode) {
		t.Fatal("patch reused another location code", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM locations") != 3 || scalar[int](t, s.db, "SELECT COUNT(*) FROM location_events") != 0 {
		t.Fatal("failed hierarchy checks changed source state")
	}
}

func TestLocationArchiveRequiresExplicitSubtreeAndNoLiveHoldings(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	root := createLocation(t, locations, token, "ROOT", nil)
	child := createLocation(t, locations, token, "CHILD", &root.ID)
	root = getLocation(t, locations, token, root.ID)
	if _, err := locations.Archive(t.Context(), token, "archive-no-children", root.ID, root.Revision, false, ""); !errors.Is(err, ErrLocationChildren) {
		t.Fatal("archived a subtree without explicit confirmation", err)
	}
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('stock-item','Cable','quantity','2026','2026')")
	execSQL(t, s.db, "INSERT INTO stock_balances VALUES('stock-item',?,5,'2026')", child.ID)
	if _, err := locations.Archive(t.Context(), token, "archive-with-stock", root.ID, root.Revision, true, ""); !errors.Is(err, ErrLocationInUse) {
		t.Fatal("archived nonempty inventory", err)
	}
	execSQL(t, s.db, "UPDATE stock_balances SET quantity_base=0")
	archived, err := locations.Archive(t.Context(), token, "archive-reviewed-key", root.ID, root.Revision, true, "清空後封存")
	if err != nil || archived.Status != "archived" || getLocation(t, locations, token, child.ID).Status != "archived" || archived.Revision != root.Revision+1 {
		t.Fatal("reviewed empty subtree was not archived atomically", err)
	}
	if scalar[string](t, s.db, "SELECT reason FROM mutation_events WHERE operation='post_locations_id_archive'") != "清空後封存" {
		t.Fatal("archive reason was not recorded in immutable audit")
	}
	child = getLocation(t, locations, token, child.ID)
	if _, err = locations.Patch(t.Context(), token, "unarchive-child-key", child.ID, child.Revision, LocationPatch{Status: testPointer("active")}); !errors.Is(err, ErrLocationArchived) {
		t.Fatal("restored a child beneath an archived ancestor", err)
	}
	root, err = locations.Patch(t.Context(), token, "unarchive-root-key", root.ID, archived.Revision, LocationPatch{Status: testPointer("active")})
	if err != nil || root.Status != "active" {
		t.Fatal("could not restore an archived root", err)
	}
	if _, err = locations.Delete(t.Context(), token, "delete-zero-balance", child.ID, child.Revision); !errors.Is(err, ErrLocationInUse) {
		t.Fatal("deletion left a published zero-balance reference dangling", err)
	}
}

func TestLocationArchiveBlocksLiveUnitsButPreservesDisposedHistory(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "UNIT-BOX", nil)
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('item','Camera','individual','2026','2026')")
	execSQL(t, s.db, "INSERT INTO item_units(id,item_id,location_id,asset_code,created_at,updated_at) VALUES('unit','item',?,'UNIT','2026','2026')", location.ID)
	if _, err := locations.Archive(t.Context(), token, "archive-live-unit", location.ID, location.Revision, false, ""); !errors.Is(err, ErrLocationInUse) {
		t.Fatal("archived a location containing a live unit", err)
	}
	execSQL(t, s.db, "UPDATE item_units SET status='disposed'")
	archived, err := locations.Archive(t.Context(), token, "archive-disposed-unit", location.ID, location.Revision, false, "")
	if err != nil {
		t.Fatal("disposed history prevented safe archival", err)
	}
	if _, err = locations.Delete(t.Context(), token, "delete-disposed-unit", location.ID, archived.Revision); !errors.Is(err, ErrLocationInUse) {
		t.Fatal("deletion discarded a disposed unit's location reference", err)
	}
}

func TestLocationDeleteEmitsIndependentLinkTombstonesAndRetainsSharedMedia(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	parent := createLocation(t, locations, token, "PARENT", nil)
	child := createLocation(t, locations, token, "CHILD", &parent.ID)
	other := createLocation(t, locations, token, "OTHER", nil)
	parent = getLocation(t, locations, token, parent.ID)
	if _, err := locations.Delete(t.Context(), token, "delete-parent-key", parent.ID, parent.Revision); !errors.Is(err, ErrLocationChildren) {
		t.Fatal("deleted a parent with a live child", err)
	}
	execSQL(t, s.db, "INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','private-photo','digest','image/jpeg',1,1,10,'2026')")
	execSQL(t, s.db, "INSERT INTO media_links(id,media_id,location_id) VALUES('child-photo','photo',?),('other-photo','photo',?)", child.ID, other.ID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('media_link','child-photo',4)")
	ack, err := locations.Delete(t.Context(), token, "delete-child-key", child.ID, child.Revision)
	if err != nil || !ack.OK || ack.OperationID == "" || ack.Revision != child.Revision+1 {
		t.Fatal("leaf deletion failed", err)
	}
	replayed, err := locations.Delete(t.Context(), token, "delete-child-key", child.ID, child.Revision)
	if err != nil || ack != replayed {
		t.Fatal("deleted resource did not replay its original acknowledgement", err)
	}
	if _, err = locations.Get(t.Context(), token, child.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted location remained publicly readable", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM locations WHERE id=? AND deleted_at IS NOT NULL", child.ID) != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='location' AND entity_id=?", child.ID) != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM media") != 1 || scalar[string](t, s.db, "SELECT id FROM media_links") != "other-photo" {
		t.Fatal("deletion lost historical identity/shared media or retained a live projection")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE operation='delete' AND projection_json IS NULL") != 2 ||
		scalar[int](t, s.db, "SELECT entity_revision FROM sync_changes WHERE entity_type='media_link'") != 5 ||
		getLocation(t, locations, token, parent.ID).Revision != parent.Revision+1 {
		t.Fatal("owner/link tombstones or aggregate revisions were incomplete")
	}
	if _, err = locations.Create(t.Context(), token, "reuse-deleted-code", 0, LocationCreate{Code: child.Code, Name: "Replacement"}); !errors.Is(err, ErrLocationCode) {
		t.Fatal("deleted identity's code was reassigned", err)
	}
}

func TestLocationNoOpPatchDoesNotInventAProjectionChange(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	before := createLocation(t, locations, token, "BOX", nil)
	after, err := locations.Patch(t.Context(), token, "no-op-patch-key", before.ID, before.Revision, LocationPatch{Name: &before.Name})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("no-op patch changed its resource", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 2 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 1 || scalar[int](t, s.db, "SELECT revision FROM data_revision WHERE scope='catalog'") != 1 {
		t.Fatal("no-op acknowledgement invented a change or lacked its audit")
	}
}

func TestLocationListUsesBoundedStablePaginationAndExcludesDeletedRows(t *testing.T) {
	_, locations, _, token := locationFixture(t)
	for i := range 3 {
		createLocation(t, locations, token, fmt.Sprintf("BOX-%d", i), nil)
	}
	page, err := locations.List(t.Context(), token, "", 2)
	if err != nil || len(page.Data) != 2 || page.NextID != page.Data[1].ID || page.Data[0].ID >= page.Data[1].ID {
		t.Fatal("invalid first page", err)
	}
	next, err := locations.List(t.Context(), token, page.NextID, 2)
	if err != nil || len(next.Data) != 1 || next.NextID != "" || next.Data[0].ID <= page.NextID {
		t.Fatal("invalid final page", err)
	}
	if _, err = locations.Delete(t.Context(), token, "delete-page-row", next.Data[0].ID, next.Data[0].Revision); err != nil {
		t.Fatal(err)
	}
	last, err := locations.List(t.Context(), token, page.NextID, 2)
	if err != nil || len(last.Data) != 0 || last.Data == nil {
		t.Fatal("deleted row remained in list or empty data became null", err)
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, err = locations.List(t.Context(), token, "", limit); !errors.Is(err, ErrDomainInput) {
			t.Fatal("unbounded page limit was admitted", err)
		}
	}
	if _, err = locations.List(t.Context(), token, strings.Repeat("x", 129), 2); !errors.Is(err, ErrDomainInput) {
		t.Fatal("invalid continuation ID was admitted", err)
	}
}

func TestLocationConcurrentDuplicateCreatesCommitExactlyOnce(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	const callers = 12
	results := make(chan Location, callers)
	failures := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			location, err := locations.Create(t.Context(), token, "concurrent-create-key", 0, LocationCreate{Code: "BOX", Name: "Only once"})
			results <- location
			failures <- err
		})
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first Location
	for result := range results {
		if first.ID == "" {
			first = result
		}
		if !reflect.DeepEqual(result, first) {
			t.Fatal("duplicate concurrent callers received different results")
		}
	}
	for _, table := range []string{"locations", "projection_revisions", "current_projections", "sync_changes", "mutation_events", "api_idempotency"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table) != 1 {
			t.Fatal("duplicate commit in", table)
		}
	}
}

func TestLocationConcurrentOppositeMovesCannotCreateACycle(t *testing.T) {
	_, locations, _, token := locationFixture(t)
	left := createLocation(t, locations, token, "LEFT", nil)
	right := createLocation(t, locations, token, "RIGHT", nil)
	outcomes := make(chan error, 2)
	var group sync.WaitGroup
	for _, pair := range [][2]Location{{left, right}, {right, left}} {
		group.Go(func() {
			_, err := locations.Move(t.Context(), token, "opposite-"+pair[0].Code, pair[0].ID, pair[0].Revision, &pair[1].ID)
			outcomes <- err
		})
	}
	group.Wait()
	close(outcomes)
	committed := 0
	for err := range outcomes {
		if err == nil {
			committed++
		} else if !errors.Is(err, ErrRevisionConflict) && !errors.Is(err, ErrLocationCycle) {
			t.Fatal(err)
		}
	}
	if committed != 1 {
		t.Fatal("opposite moves did not serialize to exactly one success")
	}
}

func TestLocationNullableParentAndAllMetadataPatchSemantics(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	root := createLocation(t, locations, token, "ROOT", nil)
	child := createLocation(t, locations, token, "CHILD", &root.ID)
	patched, err := locations.Patch(t.Context(), token, "patch-all-fields", child.ID, child.Revision, LocationPatch{
		Code: testPointer("NEW-CODE"), Name: testPointer("New name"), Kind: testPointer("shelf"), SortOrder: testPointer[int64](-5), Description: testPointer("Notes"),
	})
	if err != nil || patched.ParentID == nil || *patched.ParentID != root.ID || patched.Code != "NEW-CODE" || patched.Kind != "shelf" || patched.SortOrder != -5 || patched.Description != "Notes" {
		t.Fatal("omitted parent or supplied metadata was not preserved", err)
	}
	moved, err := locations.Patch(t.Context(), token, "patch-parent-null", child.ID, patched.Revision, LocationPatch{ParentID: NullableID{Set: true}})
	if err != nil || moved.ParentID != nil || scalar[int](t, s.db, "SELECT COUNT(*) FROM location_events WHERE to_parent_id IS NULL") != 1 {
		t.Fatal("explicit null did not move to the root with an immutable event", err)
	}
	before := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes")
	unchanged, err := locations.Move(t.Context(), token, "move-root-no-op", child.ID, moved.Revision, nil)
	if err != nil || !reflect.DeepEqual(moved, unchanged) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != before {
		t.Fatal("moving a root to the root invented a projection/event", err)
	}
}

func TestLocationHierarchyHasNoArbitraryRecursionDepthCap(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	root := createLocation(t, locations, token, "ROOT", nil)
	// Seed a deep, valid graph as an imported-data fixture without performing
	// quadratic repeated ancestor publication during fixture construction.
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(t.Context(), `WITH RECURSIVE chain(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM chain WHERE n<1100)
 INSERT INTO locations(id,parent_id,code,name,created_at,updated_at)
 SELECT 'deep-'||n,CASE WHEN n=1 THEN ? ELSE 'deep-'||(n-1) END,'DEEP-'||n,'Deep node','2026','2026' FROM chain`, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions SELECT 'location',id,1 FROM locations WHERE id LIKE 'deep-%'"); err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(t.Context(), `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'location',id,1,json_object('id',id,'parent_id',parent_id,'code',code,'name',name,'kind',kind,
 'status',status,'sort_order',sort_order,'description',description),updated_at FROM locations WHERE id LIKE 'deep-%'`)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = locations.Move(t.Context(), token, "deep-cycle-check", root.ID, root.Revision, testPointer("deep-1100")); !errors.Is(err, ErrLocationCycle) {
		t.Fatal("deep cycle guard truncated its recursive traversal", err)
	}
	leaf, err := locations.Create(t.Context(), token, "deep-child-create", 1, LocationCreate{ParentID: testPointer("deep-1100"), Name: "Leaf", Code: "LEAF"})
	if err != nil || leaf.ParentID == nil || *leaf.ParentID != "deep-1100" || getLocation(t, locations, token, root.ID).Revision != 2 {
		t.Fatal("deep parent traversal or aggregate publication was truncated", err)
	}
}

func TestLocationInputBoundsAndPreconditions(t *testing.T) {
	_, locations, _, token := locationFixture(t)
	for _, input := range []LocationCreate{
		{Code: "", Name: "Name"}, {Code: "Code", Name: ""}, {Code: strings.Repeat("c", 101), Name: "Name"},
		{Code: "Code", Name: strings.Repeat("📦", 201)}, {Code: "Code", Name: "\xff"},
		{Code: "Code", Name: "Name", Kind: testPointer("")}, {Code: "Code", Name: "Name", Kind: testPointer(strings.Repeat("k", 33))},
		{Code: "Code", Name: "Name", Status: testPointer("deleted")}, {Code: "Code", Name: "Name", SortOrder: maxSafeInteger + 1},
		{Code: "Code", Name: "Name", SortOrder: -maxSafeInteger - 1}, {Code: "Code", Name: "Name", Description: strings.Repeat("d", 10001)},
		{Code: "Code", Name: "Name", ParentID: testPointer("")},
	} {
		if _, err := locations.Create(t.Context(), token, "invalid-create-key", 0, input); !errors.Is(err, ErrDomainInput) {
			t.Fatalf("invalid location input accepted: %+v: %v", input, err)
		}
	}
	if _, err := locations.Create(t.Context(), token, "parent-proof-key", 0, LocationCreate{Code: "Code", Name: "Name", ParentID: testPointer("parent")}); !errors.Is(err, ErrPreconditionRequired) {
		t.Fatal("child creation omitted its parent precondition", err)
	}
	if _, err := locations.Create(t.Context(), token, "unused-proof-key", 1, LocationCreate{Code: "Code", Name: "Name"}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("root creation silently ignored an unrelated If-Match", err)
	}
	if _, err := locations.Create(t.Context(), token, "short", 0, LocationCreate{Code: "Code", Name: "Name"}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("invalid idempotency key was admitted", err)
	}
	valid, err := locations.Create(t.Context(), token, "boundary-create-key", 0, LocationCreate{Code: strings.Repeat("📦", 100), Name: strings.Repeat("📦", 200), Kind: testPointer(strings.Repeat("k", 32)), Description: strings.Repeat("📦", 10000), SortOrder: maxSafeInteger})
	if err != nil {
		t.Fatal("Unicode/safe-integer boundary was rejected", err)
	}
	for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
		want := ErrDomainInput
		if revision == 0 {
			want = ErrPreconditionRequired
		}
		if _, err = locations.Patch(t.Context(), token, "bad-patch-revision", valid.ID, revision, LocationPatch{Name: testPointer("Name")}); !errors.Is(err, want) {
			t.Fatal("invalid revision admitted", err)
		}
	}
	for _, patch := range []LocationPatch{{}, {ParentID: NullableID{Value: testPointer("unmarked")}}, {Name: testPointer("")}, {Code: testPointer("")}, {Kind: testPointer("")}, {Status: testPointer("unknown")}, {SortOrder: testPointer(maxSafeInteger + 1)}, {Description: testPointer("\xff")}} {
		if _, err = locations.Patch(t.Context(), token, "invalid-patch-key", valid.ID, valid.Revision, patch); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid patch admitted", err)
		}
	}
	if _, err = locations.Get(t.Context(), token, ""); !errors.Is(err, ErrDomainInput) {
		t.Fatal("empty resource ID admitted", err)
	}
	if _, err = locations.Get(t.Context(), token, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing resource was not distinguished", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = locations.Create(ctx, token, "canceled-create-key", 0, LocationCreate{Code: "CANCEL", Name: "Canceled"}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled context changed source data", err)
	}
}

func TestLocationNewServiceRejectsAnotherStoresAuthorization(t *testing.T) {
	s, _, a, _ := locationFixture(t)
	other := openTestStore(t)
	for _, input := range []*Auth{nil, {}} {
		if _, err := s.NewLocations(input); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid authentication dependency admitted", err)
		}
	}
	if _, err := other.NewLocations(a); !errors.Is(err, ErrDomainInput) {
		t.Fatal("authorization from another private database admitted", err)
	}
}

func TestLocationExpiredIdempotencyClaimCannotBecomeAnotherWrite(t *testing.T) {
	s, a, now, _, token := completedAuthFixture(t)
	locations, err := s.NewLocations(a)
	if err != nil {
		t.Fatal(err)
	}
	input := LocationCreate{Code: "BOX", Name: "Original"}
	if _, err = locations.Create(t.Context(), token, "expired-claim-key", 0, input); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(domainReplyLifetime)
	_, fresh := login(t, a)
	if _, err = locations.Create(t.Context(), fresh, "expired-claim-key", 0, input); !errors.Is(err, ErrIdempotencyExpired) {
		t.Fatal("expired key was reused or replayed outside its lifetime", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM locations") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 1 {
		t.Fatal("expired retry duplicated business effects")
	}
	// A restored/reseeded epoch is a different dataset. Old credentials cannot
	// authorize it; even a fresh login must not retrieve the old epoch's reply.
	execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-dataset'")
	if _, err = locations.Get(t.Context(), fresh, "missing"); !errors.Is(err, ErrAuthentication) {
		t.Fatal("epoch rotation resurrected a cookie", err)
	}
	_, fresh = login(t, a)
	if _, err = locations.Create(t.Context(), fresh, "expired-claim-key", 0, input); !errors.Is(err, ErrLocationCode) {
		t.Fatal("fresh epoch retrieved an old dataset's acknowledgement", err)
	}
}
