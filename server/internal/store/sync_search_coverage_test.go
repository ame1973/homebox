package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestSyncSearchCoveragePagesActualItemsAndUnitsWithoutPrivateText(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	inventory, err := NewInventory(a)
	if err != nil {
		t.Fatal(err)
	}
	items := []Item{
		createCatalogItem(t, catalog, token, ItemCreate{Name: "Private first name"}),
		createCatalogItem(t, catalog, token, ItemCreate{Name: "Private second name"}),
		createCatalogItem(t, catalog, token, ItemCreate{Name: "Private third name"}),
	}
	unit, err := inventory.CreateUnit(t.Context(), token, "coverage-create-unit", items[0].ID, items[0].Revision, UnitCreate{SerialNumber: testPointer("Private serial")})
	if err != nil {
		t.Fatal(err)
	}
	items[0] = getCatalogItem(t, catalog, token, items[0].ID)
	missingID := items[1].ID
	missingIdentity := projectionPair("item", missingID)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", missingIdentity)
	missingRevision := scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", missingIdentity)
	revision := scalar[int64](t, s.db, "SELECT revision FROM data_revision WHERE scope='catalog'")
	var records []syncSearchCoverageEntry
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > len(items) {
			t.Fatal("coverage cursor did not terminate")
		}
		page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", cursor, 1)
		if err != nil || page.Data == nil || page.SourceRevision != revision {
			t.Fatal("valid Item coverage page failed", page, err)
		}
		records = append(records, page.Data...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	sort.Slice(items, func(i, j int) bool { return bytes.Compare([]byte(items[i].ID), []byte(items[j].ID)) < 0 })
	if len(records) != len(items) {
		t.Fatal("coverage missed an Item", records)
	}
	for i, entry := range records {
		item := items[i]
		if entry.EntityType != "item" || entry.EntityID != item.ID || entry.ItemID != item.ID || entry.ItemRevision != item.Revision || entry.UnitRevision != nil || entry.SearchRevision == nil {
			t.Fatal("coverage changed Item identity or original authority", entry)
		}
		if entry.EntityID == missingID {
			if entry.Current || *entry.SearchRevision != missingRevision {
				t.Fatal("missing projection was not distinguished from a retained counter", entry)
			}
		} else if !entry.Current {
			t.Fatal("valid current search was labeled missing", entry)
		}
	}
	page, err := a.pageSyncSearchCoverage(t.Context(), token, "unit", "", 20)
	if err != nil || len(page.Data) != 1 || page.NextCursor != nil || page.Data[0].EntityID != unit.ID || page.Data[0].ItemID != unit.ItemID ||
		page.Data[0].UnitRevision == nil || *page.Data[0].UnitRevision != unit.Revision || !page.Data[0].Current {
		t.Fatal("coverage did not retain the actual Unit and owner proofs", page, err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || strings.Contains(string(encoded), "Private serial") || strings.Contains(string(encoded), "Private first name") {
		t.Fatal("coverage returned source search text", string(encoded), err)
	}
}

func TestSyncSearchCoverageCursorsBindKindSizeRevisionAndSession(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "First"})
	createCatalogItem(t, catalog, token, ItemCreate{Name: "Second"})
	first, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 1)
	if err != nil || first.NextCursor == nil || len(first.Data) != 1 {
		t.Fatal("first coverage page lacked a continuation", first, err)
	}
	cursor := *first.NextCursor
	_, fresh := login(t, a)
	if next, err := a.pageSyncSearchCoverage(t.Context(), fresh, "item", cursor, 1); err != nil || len(next.Data) != 1 {
		t.Fatal("fresh completed session could not continue the same scope", next, err)
	}
	for _, invalid := range []struct {
		kind, cursor string
		limit        int
	}{
		{"unit", cursor, 1}, {"item", cursor, 2}, {"item", cursor + "a", 1},
	} {
		if page, err := a.pageSyncSearchCoverage(t.Context(), fresh, invalid.kind, invalid.cursor, invalid.limit); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
			t.Fatal("coverage cursor crossed or altered its original scope", page, err)
		}
	}
	if _, err := catalog.Patch(t.Context(), fresh, "coverage-change-source", item.ID, item.Revision, ItemPatch{Name: testPointer("Changed")}); err != nil {
		t.Fatal(err)
	}
	if page, err := a.pageSyncSearchCoverage(t.Context(), fresh, "item", cursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("coverage cursor survived a changed catalog revision", page, err)
	}
	first, err = a.pageSyncSearchCoverage(t.Context(), fresh, "item", "", 1)
	if err != nil || first.NextCursor == nil {
		t.Fatal("new scope did not provide a continuation", first, err)
	}
	execSQL(t, s.db, "UPDATE sync_state SET server_instance_id='ffffffffffffffffffffffffffffffff'")
	if page, err := a.pageSyncSearchCoverage(t.Context(), fresh, "item", *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("coverage cursor survived a changed server instance", page, err)
	}
}

func TestSyncSearchCoverageWithholdsCorruptCurrentLookahead(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	first := createCatalogItem(t, catalog, token, ItemCreate{Name: "First"})
	second := createCatalogItem(t, catalog, token, ItemCreate{Name: "Second"})
	if bytes.Compare([]byte(first.ID), []byte(second.ID)) > 0 {
		first, second = second, first
	}
	identity := projectionPair("item", second.ID)
	execSQL(t, s.db, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.approved_text','Private OCR') WHERE entity_type='search_doc' AND entity_id=?", identity)
	before := domainState(t, s)
	page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 1)
	if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("invalid lookahead returned a coverage prefix", page, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncSearchCoverageWithholdsMissingCounterAndBadUnit(t *testing.T) {
	s, catalog, a, token := catalogFixture(t)
	inventory, err := NewInventory(a)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	unit, err := inventory.CreateUnit(t.Context(), token, "coverage-unit-proof", item.ID, item.Revision, UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	identity := projectionPair("item", item.ID)
	retainedRevision := scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", identity)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", identity)
	before := domainState(t, s)
	if page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("missing counter with retained history was presented as backfillable", page, err)
	}
	requireDomainState(t, s, before)
	// Restore the real retained search counter, then corrupt the independent Unit authority.
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('search_doc',?,?)", identity, retainedRevision)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", unit.ID)
	execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", unit.ID)
	before = domainState(t, s)
	if page, err := a.pageSyncSearchCoverage(t.Context(), token, "unit", "", 20); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("a Unit without its real counter was returned", page, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncSearchCoverageReadsAfterActivityAndRechecksSession(t *testing.T) {
	s, _, a, token := locationFixture(t)
	execSQL(t, s.db, `CREATE TRIGGER coverage_activity AFTER UPDATE ON sessions BEGIN
 INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('late-item','Private late source','individual','2026','2026');
 INSERT INTO projection_revisions VALUES('item','late-item',1);
 UPDATE data_revision SET revision=revision+1 WHERE scope='catalog'; END`)
	page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20)
	if err != nil || len(page.Data) != 1 || page.Data[0].EntityID != "late-item" || page.Data[0].Current || page.Data[0].SearchRevision != nil || page.SourceRevision < 1 {
		t.Fatal("coverage did not see source created by session activity", page, err)
	}
	execSQL(t, s.db, "DROP TRIGGER coverage_activity")
	execSQL(t, s.db, `CREATE TRIGGER coverage_revoked AFTER UPDATE ON sessions WHEN NEW.revoked_at IS NULL BEGIN
 UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`)
	before := domainState(t, s)
	if page, err = a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20); !errors.Is(err, ErrAuthentication) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("revoked session returned coverage", page, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncSearchCoverageDoesNotSkipInvalidSourceIdentities(t *testing.T) {
	for _, kind := range []string{"item", "unit"} {
		for _, invalid := range []string{"", strings.Repeat("x", 513), "\xff"} {
			t.Run(kind+"/"+strconv.QuoteToASCII(invalid), func(t *testing.T) {
				s, catalog, a, token := catalogFixture(t)
				owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Actual owner"})
				if kind == "item" {
					execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Invalid identity','individual','2026','2026')`, invalid)
				} else {
					execSQL(t, s.db, `INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,'Invalid identity','2026','2026')`, invalid, owner.ID)
				}
				before := domainState(t, s)
				page, err := a.pageSyncSearchCoverage(t.Context(), token, kind, "", 20)
				if !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
					t.Fatal("coverage skipped an invalid source identity", page, err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
}

func TestSyncSearchCoverageLegacyIdentitiesAndBackfillRestart(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	itemIDs := []string{"\x00", strings.Repeat("\x01", 128), "item/\x00家庭\n", "item-最後"}
	unitIDs := []string{"\x00unit", "unit\\\x00測試\r\n", strings.Repeat("😀", 128)}
	for _, id := range itemIDs {
		execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Private legacy owner','individual','2026','2026')`, id)
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", id)
	}
	for i, id := range unitIDs {
		execSQL(t, s.db, `INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,?,'2026','2026')`, id, itemIDs[2], "legacy-code-"+strconv.Itoa(i))
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('unit',?,1)", id)
	}
	for _, test := range []struct {
		kind string
		ids  []string
	}{{"item", itemIDs}, {"unit", unitIDs}} {
		sort.Strings(test.ids)
		var got []string
		cursor := ""
		for {
			page, err := a.pageSyncSearchCoverage(t.Context(), token, test.kind, cursor, 1)
			if err != nil || len(page.Data) != 1 || page.Data[0].Current || page.Data[0].SearchRevision != nil {
				t.Fatal("legacy source could not be read exactly", test.kind, page, err)
			}
			entry := page.Data[0]
			if test.kind == "unit" && (entry.ItemID != "item/\x00家庭\n" || entry.ItemRevision != 1 || entry.UnitRevision == nil || *entry.UnitRevision != 1) {
				t.Fatal("legacy Unit owner or proof changed", entry)
			}
			got = append(got, entry.EntityID)
			if page.NextCursor == nil {
				break
			}
			if len(got) > len(test.ids) {
				t.Fatal("legacy cursor did not terminate")
			}
			cursor = *page.NextCursor
		}
		if !reflect.DeepEqual(got, test.ids) {
			t.Fatal("legacy identities were changed, skipped or reordered", test.kind, got, test.ids)
		}
	}
	first, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 1)
	if err != nil || first.NextCursor == nil {
		t.Fatal("legacy backfill fixture lacked a continuation", first, err)
	}
	entry := first.Data[0]
	_, grant := issueGrant(t, a, token, "coverage-backfill-grant")
	request := syncSearchBackfillRequest{EntityType: "item", EntityID: entry.EntityID, ItemID: entry.ItemID, ItemRevision: entry.ItemRevision}
	ack, err := a.backfillSyncSearch(t.Context(), token, grant, "coverage-backfill-command", request)
	if err != nil || ack.Revision != 1 {
		t.Fatal("coverage proofs did not support explicit backfill", ack, err)
	}
	if page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("backfill did not invalidate the live continuation", page, err)
	}
	restarted, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 1)
	if err != nil || len(restarted.Data) != 1 || restarted.Data[0].EntityID != entry.EntityID || !restarted.Data[0].Current ||
		restarted.Data[0].SearchRevision == nil || *restarted.Data[0].SearchRevision != ack.Revision {
		t.Fatal("explicit restart did not observe the backfilled projection", restarted, err)
	}
}

func TestSyncSearchCoverageIncludesArchivedAndDisposedSources(t *testing.T) {
	_, catalog, a, token := catalogFixture(t)
	inventory, err := NewInventory(a)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Retained owner"})
	unit, err := inventory.CreateUnit(t.Context(), token, "coverage-retained-unit", item.ID, item.Revision, UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	unit, err = inventory.SetUnitStatus(t.Context(), token, "coverage-dispose-unit", unit.ID, unit.Revision, UnitStatus{Status: "disposed", DisposalKind: testPointer("discarded")})
	if err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, catalog, token, item.ID)
	item, err = catalog.Archive(t.Context(), token, "coverage-archive-item", item.ID, item.Revision, "Retained")
	if err != nil {
		t.Fatal(err)
	}
	items, itemErr := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20)
	units, unitErr := a.pageSyncSearchCoverage(t.Context(), token, "unit", "", 20)
	if itemErr != nil || unitErr != nil || len(items.Data) != 1 || len(units.Data) != 1 || !items.Data[0].Current || !units.Data[0].Current ||
		items.Data[0].ItemRevision != item.Revision || units.Data[0].ItemRevision != item.Revision || units.Data[0].UnitRevision == nil || *units.Data[0].UnitRevision != unit.Revision {
		t.Fatal("archived or disposed source was skipped", items, units, itemErr, unitErr)
	}
}

func TestSyncSearchCoverageRejectsChangedDatasetAndExpiredCursor(t *testing.T) {
	for _, change := range []string{"epoch", "floor", "watermark", "expiry"} {
		t.Run(change, func(t *testing.T) {
			s, a, now, _, token := completedAuthFixture(t)
			catalog, err := NewCatalog(a)
			if err != nil {
				t.Fatal(err)
			}
			createCatalogItem(t, catalog, token, ItemCreate{Name: "First"})
			createCatalogItem(t, catalog, token, ItemCreate{Name: "Second"})
			page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 1)
			if err != nil || page.NextCursor == nil {
				t.Fatal("fixture lacked a continuation", page, err)
			}
			switch change {
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='different-epoch'")
				_, token = login(t, a)
			case "floor":
				execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq<=1")
				execSQL(t, s.db, "UPDATE sync_state SET min_retained_seq=1")
			case "watermark":
				seedUnprovenSearchChange(t, s)
			case "expiry":
				*now = now.Add(metadataCursorLifetime)
				_, token = login(t, a)
			}
			if result, err := a.pageSyncSearchCoverage(t.Context(), token, "item", *page.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(result, syncSearchCoveragePage{}) {
				t.Fatal("changed dataset scope or expired cursor was accepted", change, result, err)
			}
		})
	}
}

func TestSyncSearchCoverageRejectsInvalidMissingSourceAndSignerAfterActivity(t *testing.T) {
	t.Run("missing source", func(t *testing.T) {
		s, catalog, a, token := catalogFixture(t)
		first := createCatalogItem(t, catalog, token, ItemCreate{Name: "First"})
		second := createCatalogItem(t, catalog, token, ItemCreate{Name: "Second"})
		if first.ID > second.ID {
			first, second = second, first
		}
		execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", projectionPair("item", second.ID))
		execSQL(t, s.db, "INSERT INTO item_aliases(id,item_id,alias,created_at) VALUES('invalid-alias',?,CAST(X'80' AS TEXT),'2026')", second.ID)
		before := domainState(t, s)
		if page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 1); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
			t.Fatal("an invalid missing-source lookahead returned a prefix", page, err)
		}
		requireDomainState(t, s, before)
	})
	t.Run("changed key after activity", func(t *testing.T) {
		s, catalog, a, token := catalogFixture(t)
		createCatalogItem(t, catalog, token, ItemCreate{Name: "Source"})
		original := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret"))
		execSQL(t, s.db, "CREATE TRIGGER coverage_secret AFTER UPDATE ON sessions BEGIN UPDATE auth_server_secret SET key_bytes=zeroblob(32); END")
		before := domainState(t, s)
		if page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20); !errors.Is(err, ErrAuthState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
			t.Fatal("a changed signing key returned coverage", page, err)
		}
		requireDomainState(t, s, before)
		if !bytes.Equal(original, scalar[[]byte](t, s.db, "SELECT key_bytes FROM auth_server_secret")) {
			t.Fatal("failed coverage did not roll back the activity trigger")
		}
	})
}

func TestSyncSearchCoverageRejectsBadSnapshotAndUnitLookahead(t *testing.T) {
	t.Run("snapshot revision", func(t *testing.T) {
		s, catalog, a, token := catalogFixture(t)
		first := createCatalogItem(t, catalog, token, ItemCreate{Name: "First"})
		second := createCatalogItem(t, catalog, token, ItemCreate{Name: "Second"})
		if first.ID > second.ID {
			first, second = second, first
		}
		identity := projectionPair("item", second.ID)
		revision := scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", identity)
		execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'coverage-snapshot',server_instance_id,dataset_epoch,1,0,'2026-11-04T12:00:00Z','2026-11-04T12:15:00Z','ready' FROM sync_state`)
		execSQL(t, s.db, `INSERT INTO sync_snapshot_rows(snapshot_id,ordinal,entity_type,entity_id,entity_revision,payload_json)
 VALUES('coverage-snapshot',0,'search_doc',?,?,?)`, identity, revision+1, `{"entity_type":"item","entity_id":"arbitrary","approved_text":"private"}`)
		before := domainState(t, s)
		if page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 1); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
			t.Fatal("a regressed counter in the lookahead returned a prefix", page, err)
		}
		requireDomainState(t, s, before)
	})
	t.Run("Unit counter", func(t *testing.T) {
		s, catalog, a, token := catalogFixture(t)
		inventory, err := NewInventory(a)
		if err != nil {
			t.Fatal(err)
		}
		item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
		first, err := inventory.CreateUnit(t.Context(), token, "coverage-lookahead-first", item.ID, item.Revision, UnitCreate{})
		if err != nil {
			t.Fatal(err)
		}
		item = getCatalogItem(t, catalog, token, item.ID)
		second, err := inventory.CreateUnit(t.Context(), token, "coverage-lookahead-second", item.ID, item.Revision, UnitCreate{})
		if err != nil {
			t.Fatal(err)
		}
		if first.ID > second.ID {
			first, second = second, first
		}
		execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", second.ID)
		execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", second.ID)
		before := domainState(t, s)
		if page, err := a.pageSyncSearchCoverage(t.Context(), token, "unit", "", 1); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
			t.Fatal("invalid Unit lookahead returned a prefix", page, err)
		}
		requireDomainState(t, s, before)
	})
}

func TestSyncSearchCoverageEmptyPageAndRequiredState(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	for _, kind := range []string{"item", "unit"} {
		page, err := a.pageSyncSearchCoverage(t.Context(), token, kind, "", 20)
		if err != nil || page.Data == nil || len(page.Data) != 0 || page.NextCursor != nil || page.SourceRevision != 0 {
			t.Fatal("empty live coverage was not an explicit empty page", kind, page, err)
		}
	}
	execSQL(t, s.db, "DELETE FROM data_revision WHERE scope='catalog'")
	if page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("coverage accepted a missing source revision", page, err)
	}
}

func TestSyncSearchCoveragePageLimitAndLookahead(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	for i := 0; i < 21; i++ {
		id := fmt.Sprintf("item-%02d", i)
		execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Legacy owner','individual','2026','2026')`, id)
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", id)
	}
	first, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20)
	if err != nil || len(first.Data) != 20 || first.NextCursor == nil || first.Data[0].EntityID != "item-00" || first.Data[19].EntityID != "item-19" {
		t.Fatal("coverage did not honor its maximum page size", first, err)
	}
	last, err := a.pageSyncSearchCoverage(t.Context(), token, "item", *first.NextCursor, 20)
	if err != nil || len(last.Data) != 1 || last.Data[0].EntityID != "item-20" || last.NextCursor != nil {
		t.Fatal("coverage did not continue after its maximum page", last, err)
	}
}

func TestSyncSearchCoverageRequiresCompletedSessionAndSuccessfulCommit(t *testing.T) {
	t.Run("mandatory password change", func(t *testing.T) {
		s, catalog, a, token := catalogFixture(t)
		createCatalogItem(t, catalog, token, ItemCreate{Name: "Source"})
		execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
		before := domainState(t, s)
		if page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20); !errors.Is(err, ErrPasswordChangeRequired) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
			t.Fatal("an incomplete session returned coverage", page, err)
		}
		requireDomainState(t, s, before)
	})
	t.Run("deferred commit failure", func(t *testing.T) {
		s, catalog, a, token := catalogFixture(t)
		createCatalogItem(t, catalog, token, ItemCreate{Name: "Source"})
		execSQL(t, s.db, "CREATE TABLE coverage_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
		execSQL(t, s.db, "CREATE TRIGGER coverage_commit AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO coverage_guard VALUES('missing'); END")
		before := domainState(t, s)
		if page, err := a.pageSyncSearchCoverage(t.Context(), token, "item", "", 20); err == nil || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
			t.Fatal("coverage escaped a failed COMMIT", page, err)
		}
		requireDomainState(t, s, before)
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM coverage_guard") != 0 {
			t.Fatal("failed COMMIT retained its deferred guard row")
		}
	})
}

func TestSyncSearchCoverageRejectsInvalidInputAndCancellation(t *testing.T) {
	_, _, a, token := catalogFixture(t)
	for _, test := range []struct {
		kind, cursor string
		limit        int
	}{
		{"other", "", 1}, {"item", "", 0}, {"unit", "", 21}, {"item", strings.Repeat("x", 2049), 1},
	} {
		if page, err := a.pageSyncSearchCoverage(t.Context(), token, test.kind, test.cursor, test.limit); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
			t.Fatal("invalid coverage input returned data", page, err)
		}
	}
	if page, err := a.pageSyncSearchCoverage(t.Context(), "bad-token", "item", "", 1); !errors.Is(err, ErrAuthentication) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("unauthenticated coverage returned data", page, err)
	}
	if page, err := (*Auth)(nil).pageSyncSearchCoverage(t.Context(), token, "item", "", 1); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("nil authority returned coverage", page, err)
	}
	if page, err := a.pageSyncSearchCoverage(nil, token, "item", "", 1); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("nil context returned coverage", page, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if page, err := a.pageSyncSearchCoverage(ctx, token, "item", "", 1); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(page, syncSearchCoveragePage{}) {
		t.Fatal("canceled coverage returned data", page, err)
	}
}
