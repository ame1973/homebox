package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncReviewCoverageChecksEveryStateWithoutPublishingRawText(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Review coverage owner"})
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('review-media','review-claim',?,'image/png',1,1,1,'2026')`, strings.Repeat("a", 64))
	seedMediaProjection(t, s, "review-media")
	seedSyncReviewSource(t, s, item.ID, "pending", "keyword", "needs_review", "PrivatePending", nil, nil, nil, nil)
	seedSyncReviewSource(t, s, item.ID, "rejected", "keyword", "rejected", "PrivateRejected", nil, testPointer("2026"), nil, nil)
	seedSyncReviewSource(t, s, item.ID, "accepted", "keyword", "accepted", "Accepted", nil, testPointer("2026"), testPointer("review-media"), testPointer("Accepted"))
	seedSyncReviewSource(t, s, item.ID, "corrected", "specification", "corrected", "PrivateSuperseded", testPointer("Corrected"), testPointer("2026"), nil, testPointer("Corrected"))
	// Invalid unselected values are deliberately irrelevant to current projection coverage.
	execSQL(t, s.db, "UPDATE ai_review_entries SET raw_value=CAST(X'ff' AS TEXT) WHERE id IN('pending','rejected','corrected')")
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncReviewCoverage(t.Context(), tx)
		if err != nil || count != 4 {
			t.Fatal("valid redacted review sources were not covered", count, err)
		}
	})
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("review coverage enabled public synchronization", err)
	}
}

func TestSyncReviewCoverageWithholdsDamagedOrOrphanRows(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Damaged review coverage owner"})
	seedSyncReviewSource(t, s, item.ID, "first", "keyword", "needs_review", "PrivatePending", nil, nil, nil, nil)
	seedSyncReviewSource(t, s, item.ID, "second", "keyword", "accepted", "Approved", nil, testPointer("2026"), nil, testPointer("Approved"))
	before := domainState(t, s)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='ai_review' AND entity_id='second'")
		}, errSyncReviewUncovered},
		{"missing counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='ai_review' AND entity_id='second'")
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='ai_review' AND entity_id='second'")
		}, ErrDomainState},
		{"mismatched counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='ai_review' AND entity_id='second'")
		}, ErrDomainState},
		{"stale approved text", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET raw_value='New approved value' WHERE id='second'")
		}, ErrDomainState},
		{"schema-valid false approval", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.state','accepted','$.effective_value','PrivatePending') WHERE entity_type='ai_review' AND entity_id='first'")
		}, ErrDomainState},
		{"missing reviewed timestamp", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET reviewed_at=NULL WHERE id='second'")
		}, ErrDomainState},
		{"invalid selected text", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET raw_value=CAST(X'ff' AS TEXT) WHERE id='second'")
		}, ErrDomainState},
		{"missing item owner", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET item_id='missing-item' WHERE id='second'")
		}, ErrDomainState},
		{"missing media", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET media_id='missing-media' WHERE id='second'")
		}, ErrDomainState},
		{"orphan current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('ai_review','orphan',1)")
			execSyncCoverageSQL(t, tx, "INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('ai_review','orphan',1,'{}','2026')")
		}, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			test.damage(t, tx)
			execSyncCoverageSQL(t, tx, "PRAGMA query_only=ON")
			defer func() {
				if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
					t.Error("restore query-only mode:", err)
				}
			}()
			count, err := verifySyncReviewCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || count != 0 {
				t.Fatal("damaged review returned coverage", count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncReviewCoveragePagesExactIDsAndWithholdsLateFailure(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Review pagination owner"})
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncReviewCoverage(t.Context(), tx)
		if err != nil || count != 0 {
			t.Fatal("empty review coverage failed", count, err)
		}
	})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("review-%03d", index))
	}
	ids = append(ids, "review-098\x00-家庭", "review-197\x00-第二", strings.Repeat("😀", 128), "zz-uncovered")
	seedSyncReviewCoverageRows(t, s, item.ID, ids[:100])
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncReviewCoverage(t.Context(), tx)
		if err != nil || count != 100 {
			t.Fatal("exact review page boundary failed", count, err)
		}
	})
	seedSyncReviewCoverageRows(t, s, item.ID, ids[100:])
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "ai_review", "", false)
		if err != nil || len(first) != 100 || first[99] != "review-098\x00-家庭" {
			t.Fatal("first review page changed its NUL boundary", first, err)
		}
		second, err := readSyncCoverageIDs(t.Context(), tx, "ai_review", first[99], true)
		if err != nil || len(second) != 100 || second[99] != "review-197\x00-第二" {
			t.Fatal("second review page changed its NUL boundary", second, err)
		}
		count, err := verifySyncReviewCoverage(t.Context(), tx)
		if err != nil || count != int64(len(ids)) {
			t.Fatal("multi-page review coverage changed identities", count, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='ai_review' AND entity_id='zz-uncovered'")
		count, err := verifySyncReviewCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncReviewUncovered) || count != 0 {
			t.Fatal("late missing review returned partial coverage", count, err)
		}
	}()
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		invalid := "review-150-" + strings.Repeat("家", 171)
		execSyncCoverageSQL(t, tx, `INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,created_at,updated_at)
 VALUES(?,?,'keyword','PrivatePending','needs_review','2026','2026')`, invalid, item.ID)
		count, err := verifySyncReviewCoverage(t.Context(), tx)
		if !errors.Is(err, ErrDomainState) || count != 0 {
			t.Fatal("an oversized identity after the first page was skipped", count, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncReviewCoverageRows(t *testing.T, s *Store, itemID string, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		execSyncCoverageSQL(t, tx, `INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,created_at,updated_at)
 VALUES(?,?,'keyword','PrivatePending','needs_review','2026','2026')`, id, itemID)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('ai_review',?,1)", id)
		payload, err := json.Marshal(syncReviewData{ID: id, ItemID: itemID, FieldKey: "keyword", State: "needs_review"})
		if err != nil {
			t.Fatal(err)
		}
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('ai_review',?,1,?,'2026')`, id, string(payload))
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncReviewCoverageRejectsInvalidSourceIdentities(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid review identity owner"})
	seedSyncReviewCoverageRows(t, s, item.ID, []string{"existing"})
	before := domainState(t, s)
	for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("bytes-%d", len(id)), func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET id=? WHERE id='existing'", id)
			count, err := verifySyncReviewCoverage(t.Context(), tx)
			if !errors.Is(err, ErrDomainState) || count != 0 {
				t.Fatal("invalid review identity returned coverage", count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncReviewCoverageBoundsSelectedSourceAndCurrent(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Large review coverage owner"})
	seedSyncReviewSource(t, s, item.ID, "pending", "keyword", "needs_review", "Private", nil, nil, nil, nil)
	seedSyncReviewSource(t, s, item.ID, "accepted", "keyword", "accepted", "Approved", nil, testPointer("2026"), nil, testPointer("Approved"))
	before := domainState(t, s)
	for _, test := range []struct {
		name, statement, value string
		want                   error
	}{
		{"large current", "UPDATE current_projections SET payload_json=? WHERE entity_type='ai_review' AND entity_id='accepted'", `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`, errSyncPayloadTooLarge},
		{"large approved", "UPDATE ai_review_entries SET raw_value=? WHERE id='accepted'", strings.Repeat("😀", 10001), ErrDomainState},
		{"large pending raw is not selected", "UPDATE ai_review_entries SET raw_value=? WHERE id='pending'", strings.Repeat("x", maxSyncPageBytes+1), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, test.statement, test.value)
			count, err := verifySyncReviewCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || test.want == nil && count != 2 || test.want != nil && count != 0 {
				t.Fatal("review size handling returned incorrect coverage", count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncReviewCoverageRejectsInvalidContextAndTransaction(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncReviewCoverage(nil, tx); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil context returned coverage", count, err)
	}
	if count, err := verifySyncReviewCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil transaction returned coverage", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if count, err := verifySyncReviewCoverage(ctx, tx); !errors.Is(err, context.Canceled) || count != 0 {
		t.Fatal("canceled context returned coverage", count, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncReviewCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || count != 0 {
		t.Fatal("closed transaction returned coverage", count, err)
	}
}
