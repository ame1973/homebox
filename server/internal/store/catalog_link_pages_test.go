package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type ownedWirePage struct {
	Data       []map[string]json.RawMessage `json:"data"`
	NextCursor *string                      `json:"next_cursor"`
	raw        string
}

func readOwnedPage(t *testing.T, ctx context.Context, catalog *Catalog, kind, token, owner, cursor string, limit int) (ownedWirePage, error) {
	t.Helper()
	var value any
	var err error
	switch kind {
	case "barcode":
		value, err = catalog.PageBarcodes(ctx, token, owner, cursor, limit)
	case "alias":
		value, err = catalog.PageAliases(ctx, token, owner, cursor, limit)
	case "attribute":
		value, err = catalog.PageAttributes(ctx, token, owner, cursor, limit)
	case "tag":
		value, err = catalog.PageTags(ctx, token, owner, cursor, limit)
	default:
		t.Fatal("unrecognized test collection")
	}
	if err != nil {
		if !reflect.ValueOf(value).IsZero() {
			t.Fatal("failed owned read exposed a successful prefix/continuation")
		}
		return ownedWirePage{}, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result ownedWirePage
	if json.Unmarshal(encoded, &result) != nil {
		t.Fatal("owned page did not serialize as its declared resource collection")
	}
	result.raw = string(encoded)
	return result, nil
}

func wireID(t *testing.T, value map[string]json.RawMessage) string {
	t.Helper()
	var id string
	if json.Unmarshal(value["id"], &id) != nil || !validDomainID(id) {
		t.Fatal("collection row has no independent bounded identity")
	}
	return id
}

func seedOwnedPages(t *testing.T, catalog *Catalog, auth *Auth, token string, owners ...Item) *Taxonomy {
	t.Helper()
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	var tagIDs []string
	for index, name := range []string{"  Tag é  ", strings.Repeat("😀", 200), "OtherTag"} {
		tag, err := taxonomy.CreateTag(t.Context(), token, fmt.Sprintf("owned-page-tag-%d", index), name)
		if err != nil {
			t.Fatal(err)
		}
		tagIDs = append(tagIDs, tag.ID)
	}
	for _, owner := range owners {
		for index, input := range []BarcodeWrite{{"CODE_128", "\x00GS1\x1d00123"}, {"QR_CODE", strings.Repeat("😀", 512)}, {"OTHER", " https://example.invalid/opaque?code=0001 "}} {
			current := getCatalogItem(t, catalog, token, owner.ID)
			if _, err := catalog.CreateBarcode(t.Context(), token, fmt.Sprintf("page-code-%s-%d", owner.ID, index), owner.ID, current.Revision, input); err != nil {
				t.Fatal(err)
			}
		}
		for index, value := range []string{"  Alias é  ", strings.Repeat("😀", 300), "\x00OtherAlias"} {
			current := getCatalogItem(t, catalog, token, owner.ID)
			if _, err := catalog.CreateAlias(t.Context(), token, fmt.Sprintf("page-alias-%s-%d", owner.ID, index), owner.ID, current.Revision, value); err != nil {
				t.Fatal(err)
			}
		}
		for index, value := range []string{"", "  Exact é  ", strings.Repeat("😀", 10000)} {
			current := getCatalogItem(t, catalog, token, owner.ID)
			if _, err := catalog.CreateAttribute(t.Context(), token, fmt.Sprintf("page-attribute-%s-%d", owner.ID, index), owner.ID, current.Revision, AttributeWrite{strings.Repeat("😀", 100), value}); err != nil {
				t.Fatal(err)
			}
		}
		current := getCatalogItem(t, catalog, token, owner.ID)
		if _, err := catalog.ReplaceTags(t.Context(), token, "page-tags-"+owner.ID, owner.ID, current.Revision, tagIDs); err != nil {
			t.Fatal(err)
		}
	}
	return taxonomy
}

func TestCatalogOwnedPagesHaveExactScopedResourcesAndRequiredTerminalCursor(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	other := createCatalogItem(t, catalog, token, ItemCreate{Name: "Other"})
	for _, kind := range []string{"barcode", "alias", "attribute", "tag"} {
		page, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", 100)
		if err != nil || page.Data == nil || len(page.Data) != 0 || page.NextCursor != nil || !strings.Contains(page.raw, `"next_cursor":null`) {
			t.Fatal("empty owned collection was not an explicit empty array/null continuation", err)
		}
	}
	seedOwnedPages(t, catalog, auth, token, item, other)
	before := domainState(t, s)
	for _, kind := range []string{"barcode", "alias", "attribute", "tag"} {
		all, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", 100)
		if err != nil || len(all.Data) != 3 || all.NextCursor != nil {
			t.Fatal("complete owner-scoped read failed", kind, err)
		}
		cursor, previous := "", ""
		for index, expected := range all.Data {
			page, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, cursor, 1)
			if err != nil || len(page.Data) != 1 || !reflect.DeepEqual(page.Data[0], expected) {
				t.Fatal("paging lost exact source text, revision or owner isolation", kind, err)
			}
			id := wireID(t, page.Data[0])
			if id <= previous {
				t.Fatal("stable independent-ID order was not retained")
			}
			previous = id
			fields := map[string]int{"barcode": 6, "alias": 4, "attribute": 5, "tag": 3}[kind]
			if len(page.Data[0]) != fields || string(page.Data[0]["revision"]) == "0" {
				t.Fatal("owned resource omitted exact required fields or leaked private columns")
			}
			if kind != "tag" && string(page.Data[0]["item_id"]) != `"`+item.ID+`"` {
				t.Fatal("collection mixed another owner's source row")
			}
			for _, private := range []string{token, testAdminPassword, "NextID", `"created_at"`, `"raw_ocr"`} {
				if strings.Contains(page.raw, private) {
					t.Fatal("owned list exposed credentials, unsigned continuation or private fields")
				}
			}
			if index == 2 {
				if page.NextCursor != nil || !strings.Contains(page.raw, `"next_cursor":null`) {
					t.Fatal("terminal page did not retain its required null cursor")
				}
			} else {
				if page.NextCursor == nil || *page.NextCursor == "" {
					t.Fatal("nonterminal page omitted its signed continuation")
				}
				cursor = *page.NextCursor
				body, err := base64.RawURLEncoding.DecodeString(strings.Split(cursor, ".")[0])
				if err != nil || strings.Contains(string(body), token) || strings.Contains(string(body), item.ID) || strings.Contains(string(body), "Alias") || strings.Contains(string(body), "opaque?") {
					t.Fatal("cursor exposed credentials, owner scope or product values", err)
				}
			}
		}
	}
	requireDomainState(t, s, before)
}

func TestCatalogOwnedCursorsRejectOtherOwnersCollectionsAndExistingMetadataPurposes(t *testing.T) {
	_, catalog, auth, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	other := createCatalogItem(t, catalog, token, ItemCreate{Name: "Other"})
	taxonomy := seedOwnedPages(t, catalog, auth, token, item, other)
	for _, kind := range []string{"barcode", "alias", "attribute", "tag"} {
		first, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", 1)
		if err != nil || first.NextCursor == nil {
			t.Fatal(err)
		}
		cursor := *first.NextCursor
		if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, other.ID, cursor, 1); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("continuation silently changed its owner", err)
		}
		if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, cursor, 2); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("continuation silently changed its requested page size", err)
		}
		for _, otherKind := range []string{"barcode", "alias", "attribute", "tag"} {
			if otherKind != kind {
				if _, err := readOwnedPage(t, t.Context(), catalog, otherKind, token, item.ID, cursor, 1); !errors.Is(err, ErrInvalidCursor) {
					t.Fatal("owned continuation crossed collection purposes", err)
				}
			}
		}
		if _, err := taxonomy.PageTags(t.Context(), token, cursor, 1); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("owned continuation became a global Tag continuation", err)
		}
		if _, err := catalog.Page(t.Context(), token, cursor, 1, ItemFilters{}); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("owned continuation became a filtered Item continuation", err)
		}
		parts := strings.Split(cursor, ".")
		for _, invalid := range []string{"unsigned-id", cursor + ".extra", parts[0] + "=." + parts[1], parts[0] + "." + strings.Repeat("A", 43)} {
			if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, invalid, 1); !errors.Is(err, ErrInvalidCursor) {
				t.Fatal("noncanonical/tampered owned continuation was accepted", err)
			}
		}
	}
	global, err := taxonomy.PageTags(t.Context(), token, "", 1)
	if err != nil || global.NextCursor == nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"barcode", "alias", "attribute", "tag"} {
		if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, *global.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("global Tag continuation became an owner-specific continuation", err)
		}
	}
}

func TestCatalogOwnedPageAdmissionMissingOwnersAndCorruptLookaheadNeverReturnPrefixes(t *testing.T) {
	for _, kind := range []string{"barcode", "alias", "attribute", "tag"} {
		t.Run(kind, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			seedOwnedPages(t, catalog, auth, token, item)
			before := domainState(t, s)
			for _, owner := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
				if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, owner, "", 1); !errors.Is(err, ErrDomainInput) {
					t.Fatal("invalid owner entered a list transaction", err)
				}
			}
			for _, limit := range []int{0, 101} {
				if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", limit); !errors.Is(err, ErrDomainInput) {
					t.Fatal("unbounded owned page was accepted", err)
				}
			}
			if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, strings.Repeat("x", 2049), 1); !errors.Is(err, ErrDomainInput) {
				t.Fatal("oversized cursor was accepted", err)
			}
			if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, "absent-owner", "", 1); !errors.Is(err, ErrNotFound) {
				t.Fatal("absent owner became an invented empty collection", err)
			}
			requireDomainState(t, s, before)
			all, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			id := wireID(t, all.Data[1])
			switch kind {
			case "barcode":
				execSQL(t, s.db, "UPDATE item_barcodes SET raw_value=CAST(x'ff' AS TEXT) WHERE id=?", id)
			case "alias":
				execSQL(t, s.db, "UPDATE item_aliases SET alias='' WHERE id=?", id)
			case "attribute":
				execSQL(t, s.db, "UPDATE item_attributes SET attribute_key='' WHERE id=?", id)
			case "tag":
				execSQL(t, s.db, "UPDATE tags SET name='' WHERE id=?", id)
			}
			before = domainState(t, s)
			if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", 1); !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid lookahead exposed the preceding valid row", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogOwnedPagesRequireActiveSessionAndBoundedEpochButAllowFreshLogin(t *testing.T) {
	for _, state := range []string{"fresh login", "cursor expired", "mandatory", "revoked", "session expired", "epoch", "invalid"} {
		t.Run(state, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			seedOwnedPages(t, catalog, auth, token, item)
			cursors := make(map[string]string)
			for _, kind := range []string{"barcode", "alias", "attribute", "tag"} {
				first, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", 1)
				if err != nil || first.NextCursor == nil {
					t.Fatal(err)
				}
				cursors[kind] = *first.NextCursor
			}
			want := ErrAuthentication
			switch state {
			case "fresh login":
				if _, err := auth.Logout(t.Context(), token, "logout-owned-page"); err != nil {
					t.Fatal(err)
				}
				_, token = login(t, auth)
			case "cursor expired":
				now := auth.now().Add(metadataCursorLifetime)
				auth.now = func() time.Time { return now }
				want = ErrInvalidCursor
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(auth.now()))
			case "session expired":
				execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(auth.now()))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-owned-page'")
				_, token = login(t, auth)
				want = ErrInvalidCursor
			case "invalid":
				token = "invalid"
			}
			before := domainState(t, s)
			for kind, cursor := range cursors {
				page, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, cursor, 1)
				if state == "fresh login" {
					if err != nil || len(page.Data) != 1 {
						t.Fatal("fresh same-principal session could not continue its read", err)
					}
				} else if !errors.Is(err, want) {
					t.Fatal("unauthorized/expired/restored read exposed a page", err)
				}
				if _, err := readOwnedPage(t, t.Context(), catalog, kind, cursor, item.ID, "", 1); !errors.Is(err, ErrAuthentication) {
					t.Fatal("a read cursor became a session credential", err)
				}
			}
			requireDomainState(t, s, before)
		})
	}
}
