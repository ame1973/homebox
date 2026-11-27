package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncMediaMetadataCoveragePreservesSharedLinksAndRetainedClaims(t *testing.T) {
	for _, owner := range []string{"item", "unit", "location"} {
		t.Run(owner, func(t *testing.T) {
			fixture, library, input := mediaLibraryFixture(t, owner)
			// These are database claims only. No files are opened or verified.
			execSQL(t, fixture.store.db, "UPDATE media SET verification='verified' WHERE id='shared-b'")
			execSQL(t, fixture.store.db, "INSERT INTO media_thumbnails(media_id,sha256,width,height,bytes) VALUES('shared-b',?,5,10,20)", strings.Repeat("b", 64))
			execSQL(t, fixture.store.db, "UPDATE media SET mime_type='image/heic' WHERE id='shared-a'")
			execSQL(t, fixture.store.db, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.mime_type','image/heic') WHERE entity_type='media' AND entity_id='shared-a'")
			link, err := library.Attach(t.Context(), fixture.token, "coverage-media-attach", "shared-a", 7, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = library.Unlink(t.Context(), fixture.token, "coverage-media-unlink", link.ID, link.Revision); err != nil {
				t.Fatal(err)
			}
			before := domainState(t, fixture.store)
			withQueryOnlySearchTx(t, fixture.store, func(tx *sql.Tx) {
				coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx)
				if err != nil || coverage.Media != 2 || coverage.Links != 4 {
					t.Fatal("shared metadata and independently identified links were not covered", coverage, err)
				}
			})
			requireDomainState(t, fixture.store, before)
			if scalar[int](t, fixture.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='media_link' AND entity_id=?", link.ID) != 2 {
				t.Fatal("removed link lost its independent retained counter")
			}
			status, err := fixture.auth.ReadSyncStatus(t.Context(), fixture.token)
			if err != nil || status.BootstrapReady || status.RetentionEnabled {
				t.Fatal("Media metadata coverage enabled public synchronization", err)
			}
		})
	}
}

func TestSyncMediaMetadataCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	before := domainState(t, fixture.store)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing Media current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='media' AND entity_id='shared-a'")
		}, errSyncMediaMetadataUncovered},
		{"missing link current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='owned-a'")
		}, errSyncMediaMetadataUncovered},
		{"changed Media claim", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.bytes',31) WHERE entity_type='media' AND entity_id='shared-a'")
		}, ErrDomainState},
		{"changed link owner", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.owner_id','wrong') WHERE entity_type='media_link' AND entity_id='owned-a'")
		}, ErrDomainState},
		{"Media counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='media' AND entity_id='shared-a'")
		}, ErrDomainState},
		{"link counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='owned-a'")
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='media_link' AND entity_id='owned-a'")
		}, ErrDomainState},
		{"invalid claimed digest", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE media SET sha256='not-a-digest' WHERE id='shared-a'")
		}, ErrDomainState},
		{"invalid claimed thumbnail dimensions", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE media SET verification='verified' WHERE id='shared-b'")
			execSyncCoverageSQL(t, tx, "INSERT INTO media_thumbnails(media_id,sha256,width,height,bytes) VALUES('shared-b',?,11,10,20)", strings.Repeat("b", 64))
		}, ErrDomainState},
		{"unsupported verified MIME", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE media SET verification='verified',mime_type='image/heic' WHERE id='shared-a'")
		}, ErrDomainState},
		{"oversized NUL-prefixed caption", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE media_links SET description=? WHERE id='owned-a'", "\x00"+strings.Repeat("😀", 10000))
		}, ErrDomainState},
		{"oversized Media owner identity", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE media_links SET item_id=? WHERE id='owned-a'", strings.Repeat("家", 171))
		}, ErrDomainState},
		{"invalid source caption", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE media_links SET description=CAST(X'ff' AS TEXT) WHERE id='owned-a'")
		}, ErrDomainState},
		{"missing linked Media", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE media_links SET media_id='missing-media' WHERE id='owned-a'")
		}, ErrDomainState},
		{"missing link owner", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE media_links SET item_id='missing-owner' WHERE id='owned-a'")
		}, ErrDomainState},
		{"stale thumbnail projection claim", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.thumb_url','/api/v1/media/shared-a/thumbnail','$.thumb_sha256',?) WHERE entity_type='media' AND entity_id='shared-a'", strings.Repeat("b", 64))
		}, ErrDomainState},
		{"orphan thumbnail source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER media_thumbnails_verified_insert")
			execSyncCoverageSQL(t, tx, "INSERT INTO media_thumbnails(media_id,sha256,width,height,bytes) VALUES('missing-media',?,1,1,1)", strings.Repeat("b", 64))
		}, ErrDomainState},
		{"orphan Media current", func(t *testing.T, tx *sql.Tx) {
			seedOrphanMediaCoverageCurrent(t, tx, "media")
		}, ErrDomainState},
		{"orphan link current", func(t *testing.T, tx *sql.Tx) {
			seedOrphanMediaCoverageCurrent(t, tx, "media_link")
		}, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			test.damage(t, tx)
			if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
					t.Error("restore query-only mode:", err)
				}
			}()
			coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || coverage != (syncMediaMetadataCoverage{}) {
				t.Fatal("damaged Media metadata returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, fixture.store, before)
}

func seedOrphanMediaCoverageCurrent(t *testing.T, tx *sql.Tx, kind string) {
	t.Helper()
	execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?,'orphan-media-coverage',1)", kind)
	execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,'orphan-media-coverage',1,'{}','2026')`, kind)
}

func TestSyncMediaMetadataCoveragePagesExactIDsAndWithholdsLateMissingLink(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Media coverage item"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("entry-%03d", index))
	}
	ids = append(ids, "entry-098\x00-家庭", "entry-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncMediaMetadataCoverage(t, s, item.ID, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		for _, kind := range []string{"media", "media_link"} {
			first, err := readSyncCoverageIDs(t.Context(), tx, kind, "", false)
			if err != nil || len(first) != 100 || first[99] != "entry-098\x00-家庭" {
				t.Fatal("first Media page changed its embedded-NUL boundary", kind, first, err)
			}
			second, err := readSyncCoverageIDs(t.Context(), tx, kind, first[99], true)
			if err != nil || len(second) != 100 || second[99] != "entry-197\x00-第二" {
				t.Fatal("second Media page changed its embedded-NUL boundary", kind, second, err)
			}
		}
		coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx)
		if err != nil || coverage.Media != int64(len(ids)) || coverage.Links != int64(len(ids)) {
			t.Fatal("multi-page Media metadata changed identities", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='zz-uncovered'")
		coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncMediaMetadataUncovered) || coverage != (syncMediaMetadataCoverage{}) {
			t.Fatal("late missing link returned partial coverage", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncMediaMetadataCoverage(t *testing.T, s *Store, itemID string, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, id := range ids {
		execSyncCoverageSQL(t, tx, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES(?,?,?,'image/png',10,20,30,'2026')`, id, fmt.Sprintf("claim-%03d", index), strings.Repeat("a", 64))
		execSyncCoverageSQL(t, tx, "INSERT INTO media_links(id,media_id,item_id,description) VALUES(?,?,?,'retained')", id, id, itemID)
		for _, kind := range []string{"media", "media_link"} {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?,?,1)", kind, id)
		}
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'media',id,1,json_object('id',id,'sha256',sha256,'thumb_url',NULL,'width',width,'height',height,'thumb_sha256',NULL,'mime_type',mime_type,'bytes',bytes,'perceptual_hash',perceptual_hash),'2026' FROM media WHERE id=?`, id)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'media_link',id,1,json_object('id',id,'media_id',media_id,'owner_type','item','owner_id',item_id,'is_cover',json('false'),'sort_order',sort_order,'description',description),'2026' FROM media_links WHERE id=?`, id)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncMediaMetadataCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx); err != nil || coverage != (syncMediaMetadataCoverage{}) {
			t.Fatal("empty Media metadata coverage failed", coverage, err)
		}
	})
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Media exact page owner"})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncMediaMetadataCoverage(t, s, item.ID, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx)
		if err != nil || coverage.Media != 100 || coverage.Links != 100 {
			t.Fatal("exact-boundary Media metadata coverage failed", coverage, err)
		}
	})
}

func TestSyncMediaMetadataCoverageRejectsInvalidSourceIDs(t *testing.T) {
	for _, kind := range []string{"media", "media_link"} {
		for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
			t.Run(fmt.Sprintf("%s-bytes-%d", kind, len(id)), func(t *testing.T) {
				s, catalog, _, token := catalogFixture(t)
				item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid Media identity owner"})
				if kind == "media" {
					seedSyncMediaMetadataCoverage(t, s, item.ID, []string{id})
				} else {
					seedSyncMediaMetadataCoverage(t, s, item.ID, []string{"valid"})
					execSQL(t, s.db, "UPDATE media_links SET id=? WHERE id='valid'", id)
				}
				withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
					coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx)
					if !errors.Is(err, ErrDomainState) || coverage != (syncMediaMetadataCoverage{}) {
						t.Fatal("invalid Media source identity was skipped", coverage, err)
					}
				})
			})
		}
	}
}

func TestSyncMediaMetadataCoverageWithholdsOversizedCurrent(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	before := domainState(t, fixture.store)
	for _, target := range []struct{ kind, id string }{{"media", "shared-a"}, {"media_link", "owned-a"}} {
		t.Run(target.kind, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type=? AND entity_id=?", oversized, target.kind, target.id)
			coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx)
			if !errors.Is(err, errSyncPayloadTooLarge) || coverage != (syncMediaMetadataCoverage{}) {
				t.Fatal("oversized Media metadata projection returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, fixture.store, before)
}

func TestSyncMediaMetadataCoverageWithholdsOversizedPrivateSourceFields(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	before := domainState(t, fixture.store)
	for _, column := range []string{"storage_key", "created_at"} {
		t.Run(column, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "UPDATE media SET "+column+"=? WHERE id='shared-a'", strings.Repeat("x", maxSyncPageBytes+1))
			coverage, err := verifySyncMediaMetadataCoverage(t.Context(), tx)
			if !errors.Is(err, ErrDomainState) || coverage != (syncMediaMetadataCoverage{}) {
				t.Fatal("oversized private source field returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, fixture.store, before)
}

func TestSyncMediaMetadataCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncMediaMetadataCoverage(nil, tx); !errors.Is(err, ErrDomainState) || result != (syncMediaMetadataCoverage{}) {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncMediaMetadataCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || result != (syncMediaMetadataCoverage{}) {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncMediaMetadataCoverage(ctx, tx); !errors.Is(err, context.Canceled) || result != (syncMediaMetadataCoverage{}) {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncMediaMetadataCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || result != (syncMediaMetadataCoverage{}) {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}
