package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Source comparison is not session, completeness or retention authority; a
// future snapshot producer must establish those separately.
func TestCurrentProjectionSourceRequiresExactStoredBytesAndCounter(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "SOURCE-CURRENT", nil)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location' AND entity_id=?", location.ID)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var reformatted bytes.Buffer
	if err = json.Indent(&reformatted, []byte(payload), "", "  "); err != nil {
		t.Fatal(err)
	}
	if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, reformatted.Bytes()); !errors.Is(err, ErrDomainState) {
		t.Fatal("bytes not stored in the current row were certified", err)
	}
	if _, err = tx.ExecContext(t.Context(), "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='location' AND entity_id=?", location.ID); err != nil {
		t.Fatal(err)
	}
	if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, []byte(payload)); !errors.Is(err, ErrDomainState) {
		t.Fatal("a current row with a different counter was certified", err)
	}
}

func TestCurrentProjectionSourceRejectsDamagedCurrentRowsWithoutRepair(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "SOURCE-DAMAGE", nil)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location' AND entity_id=?", location.ID)
	before := domainState(t, s)
	for _, test := range []struct {
		name, statement string
		ignoreChecks    bool
	}{
		{"missing current row", "DELETE FROM current_projections WHERE entity_type='location' AND entity_id=?", false},
		{"missing source", "DELETE FROM locations WHERE id=?", false},
		{"counter differs", "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='location' AND entity_id=?", false},
		{"invalid version", "UPDATE current_projections SET projection_version=2 WHERE entity_type='location' AND entity_id=?", true},
		{"empty timestamp", "UPDATE current_projections SET updated_at='' WHERE entity_type='location' AND entity_id=?", false},
		{"oversized timestamp", "UPDATE current_projections SET updated_at=printf('%040001d',0) WHERE entity_type='location' AND entity_id=?", false},
		{"invalid timestamp UTF-8", "UPDATE current_projections SET updated_at=CAST(X'ff' AS TEXT) WHERE entity_type='location' AND entity_id=?", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if test.ignoreChecks {
				if _, err = tx.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=ON"); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := tx.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=OFF"); err != nil {
						t.Error("restore constraint checking:", err)
					}
				}()
			}
			if _, err = tx.ExecContext(t.Context(), test.statement, location.ID); err != nil {
				t.Fatal(err)
			}
			if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, []byte(payload)); !errors.Is(err, ErrDomainState) {
				t.Fatal("damaged current state was certified", err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestCurrentProjectionSourceRejectsSourceOutsideProjectionSchema(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	parent := createLocation(t, locations, token, "SOURCE-PARENT", nil)
	child := createLocation(t, locations, token, "SOURCE-CHILD", nil)
	if _, err := locations.Move(t.Context(), token, "source-event-move", child.ID, child.Revision, &parent.ID); err != nil {
		t.Fatal(err)
	}
	identity := scalar[string](t, s.db, "SELECT id FROM location_events WHERE location_id=?", child.ID)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location_event' AND entity_id=?", identity)
	revision := scalar[int64](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type='location_event' AND entity_id=?", identity)
	before := domainState(t, s)
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(t.Context(), "DROP TRIGGER location_events_no_update"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), "UPDATE location_events SET occurred_at='' WHERE id=?", identity); err != nil {
			t.Fatal(err)
		}
		actual, err := readProjectionSource(t.Context(), tx, projectionKey{"location_event", identity})
		if err != nil || !errors.Is(validateSyncRecord("location_event", identity, "upsert", revision, 1, actual), ErrDomainState) {
			t.Fatal("fixture did not produce a source payload outside the schema", err)
		}
		if err = validateCurrentProjectionSource(t.Context(), tx, "location_event", identity, revision, []byte(payload)); !errors.Is(err, ErrDomainState) {
			t.Fatal("a source payload outside the offline schema was certified", err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestCurrentProjectionSourceBoundsOversizedStoredPayload(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "SOURCE-LARGE", nil)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location' AND entity_id=?", location.ID)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
	if _, err = tx.ExecContext(t.Context(), "UPDATE current_projections SET payload_json=? WHERE entity_type='location' AND entity_id=?", oversized, location.ID); err != nil {
		t.Fatal(err)
	}
	if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, []byte(payload)); !errors.Is(err, errSyncPayloadTooLarge) {
		t.Fatal("an oversized stored projection was read", err)
	}
}

func TestCurrentProjectionSourceRejectsOversizedRegeneratedPayload(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	parent := createLocation(t, locations, token, "SOURCE-LARGE-PARENT", nil)
	child := createLocation(t, locations, token, "SOURCE-LARGE-CHILD", nil)
	if _, err := locations.Move(t.Context(), token, "source-large-move", child.ID, child.Revision, &parent.ID); err != nil {
		t.Fatal(err)
	}
	identity := scalar[string](t, s.db, "SELECT id FROM location_events WHERE location_id=?", child.ID)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location_event' AND entity_id=?", identity)
	revision := scalar[int64](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type='location_event' AND entity_id=?", identity)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), "DROP TRIGGER location_events_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "UPDATE location_events SET occurred_at=? WHERE id=?", strings.Repeat("x", maxSyncPageBytes), identity); err != nil {
		t.Fatal(err)
	}
	if err = validateCurrentProjectionSource(t.Context(), tx, "location_event", identity, revision, []byte(payload)); !errors.Is(err, errSyncPayloadTooLarge) {
		t.Fatal("an oversized regenerated source payload was certified", err)
	}
}

func TestCurrentProjectionSourcePropagatesDatabaseFailure(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "SOURCE-CLOSED", nil)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location' AND entity_id=?", location.ID)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, []byte(payload)); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("closed transaction was not propagated", err)
	}
}

func TestCurrentProjectionSourceAcceptsEquivalentPersistedSpelling(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location, err := locations.Create(t.Context(), token, "source-spelling", 0, LocationCreate{Code: "SOURCE-SPELLING", Name: "Source spelling", SortOrder: maxSafeInteger})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location' AND entity_id=?", location.ID))
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, original, "", "  "); err != nil {
		t.Fatal(err)
	}
	exponent := bytes.Replace(original, []byte(`"sort_order":9007199254740991`), []byte(`"sort_order":9007199254740991e0`), 1)
	if bytes.Equal(exponent, original) {
		t.Fatal("fixture did not contain an integral sort order")
	}
	for _, test := range []struct {
		name    string
		payload []byte
	}{{"indented", formatted.Bytes()}, {"integer exponent", exponent}} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(t.Context(), "UPDATE current_projections SET payload_json=? WHERE entity_type='location' AND entity_id=?", string(test.payload), location.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
					t.Error("restore query-only mode:", err)
				}
			}()
			if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, test.payload); err != nil {
				t.Fatal("equivalent persisted source spelling was rejected", err)
			}
			if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, original); !errors.Is(err, ErrDomainState) {
				t.Fatal("different bytes were certified as the persisted row", err)
			}
		})
	}
	if actual := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location' AND entity_id=?", location.ID); actual != string(original) {
		t.Fatal("test transaction changed the original projection")
	}
}

func TestCurrentProjectionSourceRejectsPersistedUnreviewedSearch(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Reviewed item"})
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,created_at,updated_at)
 VALUES('pending',?,'keyword','Private pending OCR','needs_review','2026','2026')`, item.ID)
	identity := projectionPair("item", item.ID)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	revision := scalar[int64](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	var value searchData
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		t.Fatal(err)
	}
	value.ApprovedText += " Private pending OCR"
	bad, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateSyncRecord("search_doc", identity, "upsert", revision, 1, bad); err != nil {
		t.Fatal("fixture did not produce a schema-valid search row", err)
	}
	before := domainState(t, s)
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(t.Context(), "UPDATE current_projections SET payload_json=? WHERE entity_type='search_doc' AND entity_id=?", string(bad), identity); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
				t.Error("restore query-only mode:", err)
			}
		}()
		if err = validateCurrentProjectionSource(t.Context(), tx, "search_doc", identity, revision, bad); !errors.Is(err, ErrDomainState) {
			t.Fatal("a persisted unreviewed search term was certified", err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestCurrentProjectionSourceRejectsUnsupportedReviewSource(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Review owner"})
	payload, err := json.Marshal(map[string]any{"id": "review", "item_id": item.ID, "state": "needs_review", "effective_value": nil, "field_key": "keyword", "media_id": nil})
	if err != nil || validateSyncRecord("ai_review", "review", "upsert", 1, 1, payload) != nil {
		t.Fatal("test did not produce a valid review projection", err)
	}
	before := domainState(t, s)
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,created_at,updated_at)
 VALUES('review',?,'keyword','Unreviewed','needs_review','2026','2026')`, item.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('ai_review','review',1)"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('ai_review','review',1,?,'2026')", string(payload)); err != nil {
			t.Fatal(err)
		}
		if err = validateCurrentProjectionSource(t.Context(), tx, "ai_review", "review", 1, payload); !errors.Is(err, ErrDomainState) {
			t.Fatal("an unimplemented review source was certified", err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestCurrentProjectionSourceMatchesActualLocationWithoutWriting(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	location := createLocation(t, locations, token, "SOURCE-LOCATION", nil)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location' AND entity_id=?", location.ID)
	original := []byte(payload)
	copyOfOriginal := bytes.Clone(original)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if err := validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, original); err != nil {
			t.Fatal("actual location source was rejected", err)
		}
		var changed LocationData
		if err := json.Unmarshal(original, &changed); err != nil {
			t.Fatal(err)
		}
		changed.Name = "other valid name"
		stale, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, stale); !errors.Is(err, ErrDomainState) {
			t.Fatal("source-inconsistent payload was accepted", err)
		}
		changed.ID = "missing-location"
		missing, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if err = validateCurrentProjectionSource(t.Context(), tx, "location", changed.ID, 1, missing); !errors.Is(err, ErrDomainState) {
			t.Fatal("missing source was accepted", err)
		}
		if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, []byte(`{"id":"bad","id":"bad"}`)); !errors.Is(err, ErrDomainState) {
			t.Fatal("malformed or duplicate payload was accepted", err)
		}
		if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, 0, original); !errors.Is(err, ErrDomainState) {
			t.Fatal("invalid revision was accepted", err)
		}
		if err = validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, make([]byte, maxSyncPageBytes+1)); !errors.Is(err, errSyncPayloadTooLarge) {
			t.Fatal("an oversized input was parsed", err)
		}
		if err = validateCurrentProjectionSource(nil, tx, "location", location.ID, location.Revision, original); !errors.Is(err, ErrDomainState) {
			t.Fatal("nil context was accepted", err)
		}
		if err = validateCurrentProjectionSource(t.Context(), nil, "location", location.ID, location.Revision, original); !errors.Is(err, ErrDomainState) {
			t.Fatal("nil transaction was accepted", err)
		}
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if err = validateCurrentProjectionSource(canceled, tx, "location", location.ID, location.Revision, original); !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation was ignored", err)
		}
	})
	if !bytes.Equal(original, copyOfOriginal) {
		t.Fatal("source verification changed its input bytes")
	}
	requireDomainState(t, s, before)
	execSQL(t, s.db, "UPDATE locations SET name='changed actual source' WHERE id=?", location.ID)
	damaged := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if err := validateCurrentProjectionSource(t.Context(), tx, "location", location.ID, location.Revision, original); !errors.Is(err, ErrDomainState) {
			t.Fatal("a stale projection was certified after source changed", err)
		}
	})
	requireDomainState(t, s, damaged)
}
