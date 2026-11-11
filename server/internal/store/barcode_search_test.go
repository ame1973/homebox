package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func seedBarcodeSearch(t *testing.T, s *Store, ids []string, value string) []BarcodeMatch {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	matches := make([]BarcodeMatch, 0, len(ids))
	for index, id := range ids {
		owner := fmt.Sprintf("barcode-owner-%03d", index)
		if index == 0 {
			owner = "legacy/owner\x00😀"
		}
		symbology := "QR_CODE"
		if index%2 == 0 {
			symbology = "EAN_13"
		}
		status := "active"
		if index == 0 {
			status = "archived"
		}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{"INSERT INTO items(id,name,tracking_mode,status,created_at,updated_at) VALUES(?,?,'individual',?,'2026','2026')", []any{owner, "Actual owner", status}},
			{"INSERT INTO projection_revisions VALUES('item',?,1)", []any{owner}},
			{"INSERT INTO item_barcodes VALUES(?,?,?,'retained raw differs',?,'2026')", []any{id, owner, symbology, value}},
			{"INSERT INTO projection_revisions VALUES('barcode',?,7)", []any{id}},
		} {
			if _, err := tx.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		matches = append(matches, BarcodeMatch{ItemID: owner, BarcodeID: id, Symbology: symbology, NormalizedValue: value})
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].BarcodeID < matches[j].BarcodeID })
	return matches
}

func TestBarcodeSearchCompletesSharedActualSourcesWithoutChangingExactValues(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "First product"})
	other := createCatalogItem(t, catalog, token, ItemCreate{Name: "Second product"})
	first, err := catalog.CreateBarcode(t.Context(), token, "barcode-search-first", item.ID, item.Revision, BarcodeWrite{"UPC_A", "036000291452"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.CreateBarcode(t.Context(), token, "barcode-search-second", other.ID, other.Revision, BarcodeWrite{"UPC_A", "036000291452"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := catalog.CreateBarcode(t.Context(), token, "barcode-search-other-namespace", item.ID, item.Revision+1, BarcodeWrite{"OTHER", "036000291452"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Archive(t.Context(), token, "barcode-search-archive", other.ID, other.Revision+1, "retain in search"); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	page, err := catalog.SearchBarcodes(t.Context(), token, "036000291452", "", "", 50)
	if err != nil || page.Matches == nil || len(page.Matches) != 3 || page.NextCursor != nil {
		t.Fatal("shared product value did not return all independent rows", page, err)
	}
	seen := map[string]string{}
	for _, match := range page.Matches {
		seen[match.BarcodeID] = match.ItemID
		if match.NormalizedValue != "036000291452" {
			t.Fatal("leading zero was changed", match)
		}
	}
	if !reflect.DeepEqual(seen, map[string]string{first.ID: item.ID, second.ID: other.ID, third.ID: item.ID}) {
		t.Fatal("shared or archived product identities were collapsed", seen)
	}
	filtered, err := catalog.SearchBarcodes(t.Context(), token, "036000291452", "UPC_A", "", 50)
	if err != nil || len(filtered.Matches) != 2 {
		t.Fatal("namespace restriction lost a shared product", filtered, err)
	}
	for _, value := range []string{"36000291452", " 036000291452", "036000291452\n"} {
		empty, err := catalog.SearchBarcodes(t.Context(), token, value, "", "", 50)
		if err != nil || empty.Matches == nil || len(empty.Matches) != 0 || empty.NextCursor != nil {
			t.Fatal("exact lookup repaired or converted a different string", value, empty, err)
		}
	}
	raw, err := json.Marshal(page)
	if err != nil || strings.Contains(string(raw), `"raw_value"`) || strings.Contains(string(raw), `"revision"`) || strings.Contains(string(raw), "First product") || strings.Contains(string(raw), token) {
		t.Fatal("lookup returned noncontracted source or private data", err)
	}
	requireDomainState(t, s, before)
}

func TestBarcodeSearchPagesPreserveRetainedControlsAndLegacyNamespaces(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	ids := []string{strings.Repeat("\x00", 128), "\x00barcode\r\n", ".", "..", "a/b\\", "☕😀", strings.Repeat("😀", 128), "\U0010ffff"}
	for index := range 205 {
		ids = append(ids, fmt.Sprintf("barcode-%03d", index))
	}
	value := "\x00legacy + é\r\n00123"
	want := seedBarcodeSearch(t, s, ids, value)
	// Search source and counters, not a poisoned disposable projection cache.
	execSQL(t, s.db, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('barcode',?,7,'{"poison":true}','2026')`, ids[0])
	before := domainState(t, s)
	var got []BarcodeMatch
	cursor := ""
	for {
		page, err := catalog.SearchBarcodes(t.Context(), token, value, "", cursor, 50)
		if err != nil || len(page.Matches) == 0 || len(page.Matches) > 50 {
			t.Fatal("shared exact page failed", page, err)
		}
		got = append(got, page.Matches...)
		if page.NextCursor == nil {
			break
		}
		if len(*page.NextCursor) > 2048 {
			t.Fatal("continuation exceeded its UTF-8 byte budget")
		}
		cursor = *page.NextCursor
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("live pages lost independent owners, order, retained values or archived source")
	}
	first, err := catalog.SearchBarcodes(t.Context(), token, value, "", "", 1)
	if err != nil || len(first.Matches) != 1 || first.Matches[0].BarcodeID != strings.Repeat("\x00", 128) || first.NextCursor == nil {
		t.Fatal("maximum control identity was not a valid cursor position", first, err)
	}
	second, err := catalog.SearchBarcodes(t.Context(), token, value, "", *first.NextCursor, 1)
	if err != nil || len(second.Matches) != 1 || second.Matches[0].BarcodeID != "\x00barcode\r\n" {
		t.Fatal("control continuation did not preserve the next exact identity", second, err)
	}
	legacy, err := catalog.SearchBarcodes(t.Context(), token, value, "EAN_13", "", 100)
	if err != nil || len(legacy.Matches) != 100 || legacy.NextCursor == nil {
		t.Fatal("retained values were incorrectly subjected to current GTIN policy", legacy, err)
	}
	for _, row := range legacy.Matches {
		if row.Symbology != "EAN_13" || row.NormalizedValue != value {
			t.Fatal("retained namespace or exact value changed", row)
		}
	}
	for _, different := range []string{strings.TrimPrefix(value, "\x00"), value + "x", "\x00legacy + é\r\n00123"} {
		empty, err := catalog.SearchBarcodes(t.Context(), token, different, "", "", 50)
		if err != nil || len(empty.Matches) != 0 || empty.NextCursor != nil {
			t.Fatal("lookup normalized, trimmed or case-folded its value", empty, err)
		}
	}
	requireDomainState(t, s, before)
}

func TestBarcodeSearchCursorBindsValueNamespaceSizePrincipalEpochAndLifetime(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	value := "private-filter\x00😀"
	seedBarcodeSearch(t, s, []string{"a", "b", "c", "d"}, value)
	first, err := catalog.SearchBarcodes(t.Context(), token, value, "", "", 1)
	if err != nil || first.NextCursor == nil {
		t.Fatal("missing signed continuation", first, err)
	}
	cursor := *first.NextCursor
	parts := strings.Split(cursor, ".")
	if len(parts) != 2 {
		t.Fatal("invalid continuation envelope")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || strings.Contains(string(body), value) || strings.Contains(string(body), token) || strings.Contains(string(body), "private-filter") {
		t.Fatal("cursor included a filter or credential field", err)
	}
	var encoded metadataCursor
	if json.Unmarshal(body, &encoded) != nil {
		t.Fatal("invalid cursor body")
	}
	encoded.AdminID = "another-principal"
	purpose := "product-barcode-search-cursor-v1\x00\x00" + value
	otherPrincipal, err := encodeMetadataCursor(auth, purpose, encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		value, symbology, cursor string
		limit                    int
	}{
		{value + "x", "", cursor, 1}, {value, "EAN_13", cursor, 1}, {value, "", cursor, 2},
		{value, "", cursor + "x", 1}, {value, "", "unsigned", 1}, {value, "", otherPrincipal, 1},
	} {
		page, err := catalog.SearchBarcodes(t.Context(), token, test.value, test.symbology, test.cursor, test.limit)
		if !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(page).IsZero() {
			t.Fatal("cursor crossed its scope or returned a prefix", page, err)
		}
	}
	owned, err := catalog.PageBarcodes(t.Context(), token, "barcode-owner-001", "", 1)
	if err != nil || len(owned.Data) != 1 {
		t.Fatal("owner-scoped control read failed", err)
	}
	if _, err = catalog.PageBarcodes(t.Context(), token, "barcode-owner-001", cursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("search continuation became an owner-scoped continuation", err)
	}
	if _, err = catalog.SearchBarcodes(t.Context(), cursor, value, "", "", 1); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a cursor authenticated as a session", err)
	}
	base := auth.now()
	auth.now = func() time.Time { return base.Add(metadataCursorLifetime) }
	if page, err := catalog.SearchBarcodes(t.Context(), token, value, "", cursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(page).IsZero() {
		t.Fatal("expired cursor returned a page", page, err)
	}
	auth.now = func() time.Time { return base }
	if _, err = auth.Logout(t.Context(), token, "barcode-search-logout"); err != nil {
		t.Fatal(err)
	}
	_, token = login(t, auth)
	if page, err := catalog.SearchBarcodes(t.Context(), token, value, "", cursor, 1); err != nil || len(page.Matches) != 1 || page.Matches[0].BarcodeID != "b" {
		t.Fatal("fresh same-principal session could not continue", page, err)
	}
	execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='barcode-restored-epoch'")
	_, token = login(t, auth)
	if page, err := catalog.SearchBarcodes(t.Context(), token, value, "", cursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(page).IsZero() {
		t.Fatal("cursor crossed restored dataset epoch", page, err)
	}
}

func TestBarcodeSearchWithholdsCorruptLookaheadAndInvalidInputs(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	seedBarcodeSearch(t, s, []string{"a", "b"}, "shared")
	for _, failure := range []struct{ name, corrupt, restore string }{
		{"barcode counter", "DELETE FROM projection_revisions WHERE entity_type='barcode' AND entity_id='b'", "INSERT INTO projection_revisions VALUES('barcode','b',7)"},
		{"barcode raw UTF-8", "UPDATE item_barcodes SET raw_value=CAST(x'ff' AS TEXT) WHERE id='b'", "UPDATE item_barcodes SET raw_value='retained raw differs' WHERE id='b'"},
		{"barcode namespace", "UPDATE item_barcodes SET symbology='UNKNOWN' WHERE id='b'", "UPDATE item_barcodes SET symbology='QR_CODE' WHERE id='b'"},
		{"owner counter", "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id='barcode-owner-001'", "INSERT INTO projection_revisions VALUES('item','barcode-owner-001',1)"},
		{"owner UTF-8", "UPDATE items SET name=CAST(x'ff' AS TEXT) WHERE id='barcode-owner-001'", "UPDATE items SET name='Actual owner' WHERE id='barcode-owner-001'"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			execSQL(t, s.db, failure.corrupt)
			defer execSQL(t, s.db, failure.restore)
			before := domainState(t, s)
			for _, limit := range []int{1, 2} {
				page, err := catalog.SearchBarcodes(t.Context(), token, "shared", "", "", limit)
				if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(page).IsZero() {
					t.Fatal("corrupt source or lookahead exposed a prefix", page, err)
				}
			}
			requireDomainState(t, s, before)
		})
	}
	execSQL(t, s.db, "PRAGMA foreign_keys=OFF")
	execSQL(t, s.db, "DELETE FROM items WHERE id='barcode-owner-001'")
	execSQL(t, s.db, "PRAGMA foreign_keys=ON")
	page, err := catalog.SearchBarcodes(t.Context(), token, "shared", "", "", 1)
	if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(page).IsZero() {
		t.Fatal("orphan lookahead became an empty page or 404", page, err)
	}
	for _, query := range []struct {
		value, symbology, cursor string
		limit                    int
	}{
		{"", "", "", 50}, {strings.Repeat("😀", 513), "", "", 50}, {string([]byte{0xff}), "", "", 50},
		{"shared", "JAN", "", 50}, {"shared", "", "", 0}, {"shared", "", "", 101}, {"shared", "", strings.Repeat("x", 2049), 50},
	} {
		page, err := catalog.SearchBarcodes(t.Context(), token, query.value, query.symbology, query.cursor, query.limit)
		if !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(page).IsZero() {
			t.Fatal("invalid input returned a page", page, err)
		}
	}
}

func TestBarcodeSearchWithholdsLateAuthorizationSigningAndCommitFailures(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	seedBarcodeSearch(t, s, []string{"a", "b"}, "shared")
	for _, failure := range []struct{ name, setup, cleanup string }{
		{"activity failure", `CREATE TRIGGER fail_barcode_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'activity failure'); END`, `DROP TRIGGER fail_barcode_read`},
		{"ignored activity", `CREATE TRIGGER fail_barcode_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(IGNORE); END`, `DROP TRIGGER fail_barcode_read`},
		{"late password gate", `CREATE TRIGGER fail_barcode_read AFTER UPDATE ON sessions BEGIN UPDATE admin_accounts SET must_change_password=1; END`, `DROP TRIGGER fail_barcode_read`},
		{"late epoch", `CREATE TRIGGER fail_barcode_read AFTER UPDATE ON sessions BEGIN UPDATE sync_state SET dataset_epoch='late-change'; END`, `DROP TRIGGER fail_barcode_read`},
		{"deferred commit", `CREATE TABLE barcode_read_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_barcode_read AFTER UPDATE ON sessions BEGIN INSERT INTO barcode_read_guard VALUES('missing'); END`, `DROP TRIGGER fail_barcode_read; DROP TABLE barcode_read_guard`},
	} {
		t.Run(failure.name, func(t *testing.T) {
			execSQL(t, s.db, failure.setup)
			defer execSQL(t, s.db, failure.cleanup)
			before := domainState(t, s)
			page, err := catalog.SearchBarcodes(t.Context(), token, "shared", "", "", 1)
			if err == nil || !reflect.ValueOf(page).IsZero() {
				t.Fatal("late failure returned a barcode prefix or cursor", page, err)
			}
			requireDomainState(t, s, before)
		})
	}
	secret := auth.serverSecret
	auth.serverSecret = nil
	page, err := catalog.SearchBarcodes(t.Context(), token, "shared", "", "", 1)
	auth.serverSecret = secret
	if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(page).IsZero() {
		t.Fatal("failed signing returned a page", page, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if page, err := catalog.SearchBarcodes(ctx, token, "shared", "", "", 1); !errors.Is(err, context.Canceled) || !reflect.ValueOf(page).IsZero() {
		t.Fatal("canceled read returned a page", page, err)
	}
	base, calls := auth.now(), 0
	auth.now = func() time.Time {
		calls++
		if calls > 2 {
			return base.Add(31 * 24 * time.Hour)
		}
		return base
	}
	page, err = catalog.SearchBarcodes(t.Context(), token, "shared", "", "", 1)
	auth.now = func() time.Time { return base }
	if err == nil || !reflect.ValueOf(page).IsZero() {
		t.Fatal("late-expired session returned a page", page, err)
	}
	if page, err := catalog.SearchBarcodes(t.Context(), token, "shared", "", "", 1); err != nil || len(page.Matches) != 1 {
		t.Fatal("read failure left the authorized connection unusable", page, err)
	}
}
