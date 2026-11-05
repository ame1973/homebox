package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Decode actual wire data so both public list types exercise the same assertions.
type taxonomyWirePage struct {
	Data []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Revision int64  `json:"revision"`
	} `json:"data"`
	NextCursor *string `json:"next_cursor"`
	raw        string
}

func readTaxonomyPage(t *testing.T, taxonomy *Taxonomy, kind, token, cursor string, limit int) (taxonomyWirePage, error) {
	t.Helper()
	var value any
	var err error
	if kind == "category" {
		value, err = taxonomy.PageCategories(t.Context(), token, cursor, limit)
	} else {
		value, err = taxonomy.PageTags(t.Context(), token, cursor, limit)
	}
	if err != nil {
		if !reflect.ValueOf(value).IsZero() {
			t.Fatal("failed signed page exposed partial data or continuation")
		}
		return taxonomyWirePage{}, err
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result taxonomyWirePage
	if err = json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	result.raw = string(body)
	return result, nil
}

func seedTaxonomyPages(t *testing.T, taxonomy *Taxonomy, token string) {
	t.Helper()
	for index, name := range []string{"  家用品 😀 é  ", strings.Repeat("😀", 200), "Last"} {
		if _, err := taxonomy.CreateCategory(t.Context(), token, fmt.Sprintf("cursor-category-%d", index), 0, CategoryCreate{Name: name}); err != nil {
			t.Fatal(err)
		}
		if _, err := taxonomy.CreateTag(t.Context(), token, fmt.Sprintf("cursor-tag-%d", index), name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaxonomySignedPagesRetainExactMetadataAndPublicFieldsOnly(t *testing.T) {
	_, taxonomy, _, token := taxonomyFixture(t)
	for _, kind := range []string{"category", "tag"} {
		page, err := readTaxonomyPage(t, taxonomy, kind, token, "", 100)
		if err != nil || page.Data == nil || len(page.Data) != 0 || page.NextCursor != nil {
			t.Fatal("empty live list did not emit an empty array and explicit null cursor", err)
		}
	}
	seedTaxonomyPages(t, taxonomy, token)
	for _, kind := range []string{"category", "tag"} {
		t.Run(kind, func(t *testing.T) {
			all, err := readTaxonomyPage(t, taxonomy, kind, token, "", 100)
			if err != nil || len(all.Data) != 3 || all.NextCursor != nil {
				t.Fatal("complete live metadata page failed", err)
			}
			cursor := ""
			for index := range all.Data {
				page, err := readTaxonomyPage(t, taxonomy, kind, token, cursor, 1)
				if err != nil || len(page.Data) != 1 || page.Data[0] != all.Data[index] || page.Data[0].Revision < 1 {
					t.Fatal("signed page changed exact source fields or stable ordering", err)
				}
				if kind == "category" && !strings.Contains(page.raw, `"parent_id":null`) {
					t.Fatal("root category omitted its required nullable parent")
				}
				for _, secret := range []string{token, testAdminPassword, "NextID", `"after"`, `"admin"`, `"epoch"`} {
					if strings.Contains(page.raw, secret) {
						t.Fatal("list leaked private or unsigned continuation fields")
					}
				}
				if index == len(all.Data)-1 {
					if page.NextCursor != nil {
						t.Fatal("terminal page still advertised a continuation")
					}
				} else {
					if page.NextCursor == nil || *page.NextCursor == "" {
						t.Fatal("nonterminal page omitted its signed continuation")
					}
					cursor = *page.NextCursor
				}
			}
		})
	}
}

func TestTaxonomySignedPagesRejectCrossEntityAndPageSizeCursors(t *testing.T) {
	_, taxonomy, auth, token := taxonomyFixture(t)
	seedTaxonomyPages(t, taxonomy, token)
	locations, err := NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	createLocation(t, locations, token, "FIRST", nil)
	createLocation(t, locations, token, "SECOND", nil)
	locationPage, err := locations.Page(t.Context(), token, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	cursors := map[string]string{"location": *locationPage.NextCursor}
	for _, kind := range []string{"category", "tag"} {
		page, err := readTaxonomyPage(t, taxonomy, kind, token, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		cursors[kind] = *page.NextCursor
		for _, limit := range []int{0, 101} {
			if _, err = readTaxonomyPage(t, taxonomy, kind, token, "", limit); !errors.Is(err, ErrDomainInput) {
				t.Fatal("unbounded signed page was admitted", err)
			}
		}
		if _, err = readTaxonomyPage(t, taxonomy, kind, token, strings.Repeat("x", 2049), 1); !errors.Is(err, ErrDomainInput) {
			t.Fatal("oversized cursor was admitted", err)
		}
		if _, err = readTaxonomyPage(t, taxonomy, kind, token, cursors[kind], 2); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("cursor page-size scope changed", err)
		}
	}
	for source, cursor := range cursors {
		for _, target := range []string{"location", "category", "tag"} {
			if source == target {
				continue
			}
			var err error
			if target == "location" {
				_, err = locations.Page(t.Context(), token, cursor, 1)
			} else {
				_, err = readTaxonomyPage(t, taxonomy, target, token, cursor, 1)
			}
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatal("cursor crossed entity signing purposes", source, target, err)
			}
		}
	}
}

func TestTaxonomySignedPagesRequireFreshAuthorizationButAllowFreshLogin(t *testing.T) {
	for _, kind := range []string{"category", "tag"} {
		t.Run(kind, func(t *testing.T) {
			_, taxonomy, auth, token := taxonomyFixture(t)
			seedTaxonomyPages(t, taxonomy, token)
			first, err := readTaxonomyPage(t, taxonomy, kind, token, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = auth.Logout(t.Context(), token, "logout-page"); err != nil {
				t.Fatal(err)
			}
			if _, err = readTaxonomyPage(t, taxonomy, kind, token, *first.NextCursor, 1); !errors.Is(err, ErrAuthentication) {
				t.Fatal("cursor resurrected a revoked session", err)
			}
			if _, err = readTaxonomyPage(t, taxonomy, kind, *first.NextCursor, "", 1); !errors.Is(err, ErrAuthentication) {
				t.Fatal("cursor was accepted as a credential", err)
			}
			_, fresh := login(t, auth)
			next, err := readTaxonomyPage(t, taxonomy, kind, fresh, *first.NextCursor, 1)
			if err != nil || len(next.Data) != 1 || next.Data[0].ID <= first.Data[0].ID {
				t.Fatal("fresh completed session could not continue the same principal's read", err)
			}
		})
	}
}

func TestTaxonomySignedPageExpiryEpochAndMandatoryGate(t *testing.T) {
	for _, kind := range []string{"category", "tag"} {
		for _, state := range []string{"expired cursor", "expired session", "restored epoch", "mandatory"} {
			t.Run(kind+state, func(t *testing.T) {
				s, taxonomy, auth, token := taxonomyFixture(t)
				seedTaxonomyPages(t, taxonomy, token)
				first, err := readTaxonomyPage(t, taxonomy, kind, token, "", 1)
				if err != nil {
					t.Fatal(err)
				}
				want := ErrInvalidCursor
				switch state {
				case "expired cursor":
					now := auth.now().Add(metadataCursorLifetime)
					auth.now = func() time.Time { return now }
				case "expired session":
					execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(auth.now()))
					want = ErrAuthentication
				case "restored epoch":
					execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-epoch'")
					if _, err = readTaxonomyPage(t, taxonomy, kind, token, *first.NextCursor, 1); !errors.Is(err, ErrAuthentication) {
						t.Fatal("old epoch session authenticated", err)
					}
					_, token = login(t, auth)
				case "mandatory":
					execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
					want = ErrPasswordChangeRequired
				}
				before := domainState(t, s)
				if _, err = readTaxonomyPage(t, taxonomy, kind, token, *first.NextCursor, 1); !errors.Is(err, want) {
					t.Fatal("stale or unauthorized continuation was admitted", err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
}

func TestTaxonomySignedPagesAreLiveAndWithholdFailedReadResults(t *testing.T) {
	for _, kind := range []string{"category", "tag"} {
		t.Run(kind, func(t *testing.T) {
			s, taxonomy, auth, token := taxonomyFixture(t)
			seedTaxonomyPages(t, taxonomy, token)
			all, err := readTaxonomyPage(t, taxonomy, kind, token, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			first, err := readTaxonomyPage(t, taxonomy, kind, token, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			last := all.Data[2]
			if kind == "category" {
				_, err = taxonomy.DeleteCategory(t.Context(), token, "delete-between-pages", last.ID, last.Revision)
			} else {
				_, err = taxonomy.DeleteTag(t.Context(), token, "delete-between-pages", last.ID, last.Revision)
			}
			if err != nil {
				t.Fatal(err)
			}
			next, err := readTaxonomyPage(t, taxonomy, kind, token, *first.NextCursor, 1)
			if err != nil || len(next.Data) != 1 || next.Data[0] != all.Data[1] || next.NextCursor != nil {
				t.Fatal("live metadata page pretended to retain deleted snapshot data", err)
			}
			before := domainState(t, s)
			for _, failure := range []string{"statement", "commit", "private key"} {
				switch failure {
				case "statement":
					execSQL(t, s.db, "CREATE TRIGGER fail_page BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'session update failure'); END")
				case "commit":
					execSQL(t, s.db, "CREATE TABLE page_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
					execSQL(t, s.db, "CREATE TRIGGER fail_page AFTER UPDATE ON sessions BEGIN INSERT INTO page_commit_guard VALUES('missing'); END")
				case "private key":
					auth.serverSecret = nil
				}
				if _, err = readTaxonomyPage(t, taxonomy, kind, token, "", 1); err == nil {
					t.Fatal("failed list read exposed a signed page", failure)
				}
				requireDomainState(t, s, before)
				if failure != "private key" {
					execSQL(t, s.db, "DROP TRIGGER fail_page")
					if _, err = readTaxonomyPage(t, taxonomy, kind, token, "", 1); err != nil {
						t.Fatal("failed read poisoned the next explicit read", err)
					}
				}
			}
		})
	}
}

func TestMetadataCursorRejectsSignedAmbiguousJSONAndKeepsLocationWirePurpose(t *testing.T) {
	s, _, auth, token := taxonomyFixture(t)
	session, err := auth.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	now := auth.now()
	epoch := scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state")
	value := metadataCursor{AdminID: session.Admin.ID, Epoch: epoch, AfterID: "after", Limit: 1, Issued: now.Unix(), Expires: now.Add(metadataCursorLifetime).Unix()}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	legacy := base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(auth.authMAC("", "locations-tree-cursor-v1", body))
	encoded, err := encodeMetadataCursor(auth, locationCursorPurpose, value)
	if err != nil || encoded != legacy {
		t.Fatal("sharing the codec changed the existing location cursor format or MAC purpose", err)
	}
	for _, purpose := range []string{locationCursorPurpose, categoryCursorPurpose, tagCursorPurpose} {
		for _, ambiguous := range []string{
			strings.Replace(string(body), `"limit":1`, `"limit":1,"limit":1`, 1),
			strings.Replace(string(body), `"limit":1`, `"LIMIT":1`, 1),
			" " + string(body), strings.Replace(string(body), `"after":"after"`, `"after": "after"`, 1),
		} {
			cursor := base64.RawURLEncoding.EncodeToString([]byte(ambiguous)) + "." + base64.RawURLEncoding.EncodeToString(auth.authMAC("", purpose, []byte(ambiguous)))
			if _, err = decodeMetadataCursor(auth, purpose, cursor, session.Admin.ID, epoch, 1, now); !errors.Is(err, ErrInvalidCursor) {
				t.Fatal("signed but ambiguous or noncanonical JSON payload was accepted", err)
			}
		}
	}
}
