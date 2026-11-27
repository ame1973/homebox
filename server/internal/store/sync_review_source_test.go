package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSyncReviewSourceProjectsOnlyActuallyReviewedValues(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Review source owner"})
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('review-media','review-claim',?,'image/png',1,1,1,'2026')`, strings.Repeat("a", 64))
	seedMediaProjection(t, s, "review-media")
	for _, review := range []struct {
		id, state, raw, field   string
		corrected, reviewed     *string
		mediaID, effectiveValue *string
	}{
		{"pending", "needs_review", "PrivatePendingOCR", "keyword", nil, nil, nil, nil},
		{"rejected", "rejected", "PrivateRejectedOCR", "keyword", nil, testPointer("2026"), nil, nil},
		{"accepted", "accepted", "ApprovedExactOCR", "keyword", nil, testPointer("2026"), testPointer("review-media"), testPointer("ApprovedExactOCR")},
		{"corrected", "corrected", "PrivateSupersededOCR", "specification", testPointer("CorrectedExactOCR"), testPointer("2026"), nil, testPointer("CorrectedExactOCR")},
	} {
		seedSyncReviewSource(t, s, item.ID, review.id, review.field, review.state, review.raw, review.corrected, review.reviewed, review.mediaID, review.effectiveValue)
	}
	// Pending/rejected raw OCR is not a source for the offline projection, even
	// if the unselected value is not valid UTF-8.
	execSQL(t, s.db, "UPDATE ai_review_entries SET raw_value=CAST(X'ff' AS TEXT) WHERE id IN('pending','rejected')")
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		for _, id := range []string{"pending", "rejected", "accepted", "corrected"} {
			var payload string
			if err := tx.QueryRowContext(t.Context(), "SELECT payload_json FROM current_projections WHERE entity_type='ai_review' AND entity_id=?", id).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			actual, err := readProjectionSource(t.Context(), tx, projectionKey{"ai_review", id})
			if err != nil {
				t.Fatal("valid review source was rejected", id, err)
			}
			var expected, source map[string]any
			if json.Unmarshal([]byte(payload), &expected) != nil || json.Unmarshal(actual, &source) != nil || !reflect.DeepEqual(expected, source) {
				t.Fatal("review source changed selected values", id)
			}
			if err = validateCurrentProjectionSource(t.Context(), tx, "ai_review", id, 1, []byte(payload)); err != nil {
				t.Fatal("actual review projection failed source comparison", id, err)
			}
			for _, private := range []string{"PrivatePendingOCR", "PrivateRejectedOCR", "PrivateSupersededOCR"} {
				if bytes.Contains(actual, []byte(private)) {
					t.Fatal("unreviewed or superseded raw OCR entered review projection", id)
				}
			}
		}
	})
	requireDomainState(t, s, before)
}

func seedSyncReviewSource(t *testing.T, s *Store, itemID, id, field, state, raw string, corrected, reviewed, mediaID, effective *string) {
	t.Helper()
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,media_id,field_key,raw_value,corrected_value,state,reviewed_at,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,'2026','2026')`, id, itemID, mediaID, field, raw, corrected, state, reviewed)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('ai_review',?,1)", id)
	payload, err := json.Marshal(map[string]any{"id": id, "item_id": itemID, "media_id": mediaID, "field_key": field, "state": state, "effective_value": effective})
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('ai_review',?,1,?,'2026')", id, string(payload))
}

func TestSyncReviewSourceRejectsDamageWithoutWriting(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Damaged review owner"})
	seedSyncReviewSource(t, s, item.ID, "review", "keyword", "accepted", "Approved", nil, testPointer("2026"), nil, testPointer("Approved"))
	payload := []byte(scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='ai_review' AND entity_id='review'"))
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if err := validateCurrentProjectionSource(t.Context(), tx, "ai_review", "review", 1, payload); err != nil {
			t.Fatal("fixture did not start with a valid review projection", err)
		}
	})
	before := domainState(t, s)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
	}{
		{"no review timestamp", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET reviewed_at=NULL WHERE id='review'")
		}},
		{"empty review timestamp", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET reviewed_at='' WHERE id='review'")
		}},
		{"invalid review timestamp", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET reviewed_at=CAST(X'ff' AS TEXT) WHERE id='review'")
		}},
		{"invalid effective UTF-8", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET raw_value=CAST(X'ff' AS TEXT) WHERE id='review'")
		}},
		{"invalid corrected UTF-8", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET state='corrected',corrected_value=CAST(X'ff' AS TEXT) WHERE id='review'")
		}},
		{"oversized NUL-prefixed field", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET field_key=? WHERE id='review'", "\x00"+strings.Repeat("😀", 100))
		}},
		{"oversized effective value", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET raw_value=? WHERE id='review'", "\x00"+strings.Repeat("😀", 10000))
		}},
		{"invalid field key", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET field_key='' WHERE id='review'")
		}},
		{"oversized Item ID", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET item_id=? WHERE id='review'", strings.Repeat("家", 171))
		}},
		{"missing Item source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET item_id='missing-item' WHERE id='review'")
		}},
		{"missing Item counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", item.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", item.ID)
		}},
		{"missing review source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM ai_review_entries WHERE id='review'")
		}},
		{"missing Media source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET media_id='missing-media' WHERE id='review'")
		}},
		{"missing counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='ai_review' AND entity_id='review'")
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='ai_review' AND entity_id='review'")
		}},
		{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='ai_review' AND entity_id='review'")
		}},
		{"different reviewed source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET raw_value='ChangedApproved' WHERE id='review'")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
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
			if err = validateCurrentProjectionSource(t.Context(), tx, "ai_review", "review", 1, payload); !errors.Is(err, ErrDomainState) {
				t.Fatal("damaged review source was certified", err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncReviewSourceRejectsReviewedStatesWithoutTimestamp(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Missing review timestamp"})
	seedSyncReviewSource(t, s, item.ID, "review", "keyword", "accepted", "Approved", nil, testPointer("2026"), nil, testPointer("Approved"))
	before := domainState(t, s)
	for _, state := range []string{"accepted", "corrected", "rejected"} {
		t.Run(state, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "UPDATE ai_review_entries SET state=?,corrected_value='Corrected',reviewed_at=NULL WHERE id='review'", state)
			if _, err = loadSyncReviewSource(t.Context(), tx, "review"); !errors.Is(err, ErrDomainState) {
				t.Fatal("reviewed state without a timestamp was certified", state, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncReviewSourceAcceptsMaximumWidthApprovedText(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Maximum review source"})
	id, field, approved := strings.Repeat("😀", 128), strings.Repeat("😀", 100), strings.Repeat("😀", 10000)
	seedSyncReviewSource(t, s, item.ID, id, field, "accepted", approved, nil, testPointer("2026"), nil, &approved)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		payload, err := readProjectionSource(t.Context(), tx, projectionKey{"ai_review", id})
		if err != nil || validateSyncRecord("ai_review", id, "upsert", 1, 1, payload) != nil {
			t.Fatal("maximum-width approved review source was rejected", err)
		}
		var stored string
		if err = tx.QueryRowContext(t.Context(), "SELECT payload_json FROM current_projections WHERE entity_type='ai_review' AND entity_id=?", id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if err = validateCurrentProjectionSource(t.Context(), tx, "ai_review", id, 1, []byte(stored)); err != nil {
			t.Fatal("maximum-width stored review was rejected", err)
		}
	})
	requireDomainState(t, s, before)
}

func TestSyncReviewSourceRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = loadSyncReviewSource(nil, tx, "id"); !errors.Is(err, ErrDomainState) {
		t.Fatal("nil context returned source", err)
	}
	if _, err = loadSyncReviewSource(t.Context(), nil, "id"); !errors.Is(err, ErrDomainState) {
		t.Fatal("nil transaction returned source", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = loadSyncReviewSource(ctx, tx, "id"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled context returned source", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = loadSyncReviewSource(t.Context(), tx, "id"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("closed transaction returned source", err)
	}
}
