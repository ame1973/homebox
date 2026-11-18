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

func withQueryOnlySearchTx(t *testing.T, s *Store, check func(*sql.Tx)) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
			t.Error("restore query-only mode:", err)
		}
	}()
	check(tx)
}

func TestCurrentSearchProjectionVerificationReadsOnlyApprovedActualSource(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	inventory, err := NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	purchases, err := NewPurchases(auth)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "ManualItem", Description: "ManualDescription"})
	unit, err := inventory.CreateUnit(t.Context(), token, "source-check-unit", item.ID, item.Revision, UnitCreate{SerialNumber: testPointer("UnitSerial"), Notes: "UnitNotes"})
	if err != nil {
		t.Fatal(err)
	}
	for _, purchase := range []struct {
		key, merchant string
		unitID        *string
	}{{"source-check-unit-purchase", "UnitMerchant", &unit.ID}, {"source-check-unallocated-purchase", "ItemOnlyMerchant", nil}} {
		parent, err := purchases.Create(t.Context(), token, purchase.key, PurchaseCreate{MerchantName: purchase.merchant, PurchasedAt: "2026-11-18T00:00:00Z", Currency: "JPY"})
		if err != nil {
			t.Fatal(err)
		}
		owner := getCatalogItem(t, catalog, token, item.ID)
		if _, err := purchases.CreateLine(t.Context(), token, purchase.key+"-line", parent.ID, parent.Revision, PurchaseLineCreate{ItemID: item.ID, ItemRevision: owner.Revision, ItemUnitID: purchase.unitID, QuantityBase: 1, PaidMinor: 0}); err != nil {
			t.Fatal(err)
		}
	}
	for _, review := range []struct {
		id, state, raw      string
		corrected, reviewed *string
	}{
		{"pending", "needs_review", "PendingPrivateOCR", nil, nil},
		{"rejected", "rejected", "RejectedPrivateOCR", nil, testPointer("2026-11-18T00:00:00Z")},
		{"accepted", "accepted", "AcceptedReviewed", nil, testPointer("2026-11-18T00:00:00Z")},
		{"corrected", "corrected", "OriginalPrivateOCR", testPointer("CorrectedReviewed"), testPointer("2026-11-18T00:00:00Z")},
		{"missing-time", "accepted", "NoReviewTime", nil, nil},
	} {
		execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,corrected_value,reviewed_at,created_at,updated_at)
 VALUES(?,?,'keyword',?,?,?,?, '2026','2026')`, review.id, item.ID, review.state, review.raw, review.corrected, review.reviewed)
	}
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('source-media','private-source',?,'image/png',1,1,1,'2026')`, strings.Repeat("a", 64))
	execSQL(t, s.db, `INSERT INTO ocr_results(id,media_id,raw_text,engine,engine_version,created_at) VALUES('source-ocr','source-media','UnreviewedRawOCR','fixture','1','2026')`)
	owner := getCatalogItem(t, catalog, token, item.ID)
	if _, err := catalog.CreateAlias(t.Context(), token, "source-check-publish", item.ID, owner.Revision, "ManualAlias"); err != nil {
		t.Fatal(err)
	}
	identities := []string{projectionPair("item", item.ID), projectionPair("unit", unit.ID)}
	type frozenSearch struct {
		identity string
		revision int64
		payload  string
	}
	var records []frozenSearch
	for _, identity := range identities {
		record := frozenSearch{identity: identity}
		err := s.db.QueryRowContext(t.Context(), "SELECT entity_revision,payload_json FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity).Scan(&record.revision, &record.payload)
		if err != nil {
			t.Fatal(err)
		}
		for _, term := range []string{"ManualItem", "ManualDescription", "ManualAlias", "UnitMerchant", "AcceptedReviewed", "CorrectedReviewed"} {
			if !strings.Contains(record.payload, term) {
				t.Fatal("actual projection omitted reviewed source", term)
			}
		}
		for _, term := range []string{"PendingPrivateOCR", "RejectedPrivateOCR", "OriginalPrivateOCR", "NoReviewTime", "UnreviewedRawOCR"} {
			if strings.Contains(record.payload, term) {
				t.Fatal("actual projection included unreviewed or superseded source", term)
			}
		}
		records = append(records, record)
	}
	if !strings.Contains(records[0].payload, "ItemOnlyMerchant") || strings.Contains(records[1].payload, "ItemOnlyMerchant") ||
		strings.Contains(records[0].payload, "UnitSerial") || !strings.Contains(records[1].payload, "UnitSerial") {
		t.Fatal("fixture did not retain exact item/unit evidence scope")
	}
	// Poisoning derived caches must not affect source verification.
	execSQL(t, s.db, "UPDATE search_documents SET title='PoisonedItemCache' WHERE item_id=?", item.ID)
	execSQL(t, s.db, "UPDATE unit_search_documents SET title='PoisonedUnitCache' WHERE unit_id=?", unit.ID)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		for _, record := range records {
			original := []byte(record.payload)
			beforeBytes := bytes.Clone(original)
			if err := validateSearchProjectionSource(t.Context(), tx, record.identity, record.revision, original); err != nil || !bytes.Equal(original, beforeBytes) {
				t.Fatal("source-only check rejected or changed actual projection", err)
			}
			var value searchData
			if err := json.Unmarshal(original, &value); err != nil {
				t.Fatal(err)
			}
			value.ApprovedText += "\nPendingPrivateOCR"
			bad, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateSyncRecord("search_doc", record.identity, "upsert", record.revision, 1, bad); err != nil {
				t.Fatal("test did not create schema-valid unreviewed contamination", err)
			}
			if err := validateSearchProjectionSource(t.Context(), tx, record.identity, record.revision, bad); !errors.Is(err, ErrDomainState) {
				t.Fatal("schema-valid unreviewed contamination was accepted", err)
			}
			var indented bytes.Buffer
			if err := json.Indent(&indented, original, "", "  "); err != nil {
				t.Fatal(err)
			}
			if err := validateSearchProjectionSource(t.Context(), tx, record.identity, record.revision, indented.Bytes()); err != nil {
				t.Fatal("equivalent JSON spelling was rejected", err)
			}
		}
	})
	requireDomainState(t, s, before)

	// A previously valid frozen payload is not a proof of today's source and
	// must remain unchanged; historical delta readers must not use this check.
	owner = getCatalogItem(t, catalog, token, item.ID)
	if _, err := catalog.Patch(t.Context(), token, "source-check-rename", item.ID, owner.Revision, ItemPatch{Name: testPointer("ChangedItem")}); err != nil {
		t.Fatal(err)
	}
	var current []frozenSearch
	for _, identity := range identities {
		record := frozenSearch{identity: identity}
		if err := s.db.QueryRowContext(t.Context(), "SELECT entity_revision,payload_json FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity).Scan(&record.revision, &record.payload); err != nil {
			t.Fatal(err)
		}
		current = append(current, record)
	}
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		for index, record := range records {
			if err := validateSearchProjectionSource(t.Context(), tx, record.identity, record.revision, []byte(record.payload)); !errors.Is(err, ErrDomainState) {
				t.Fatal("historical payload was incorrectly certified as current", err)
			}
			now := current[index]
			if err := validateSearchProjectionSource(t.Context(), tx, now.identity, now.revision, []byte(now.payload)); err != nil {
				t.Fatal("new current source projection was rejected", err)
			}
		}
	})
	for _, record := range records {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='search_doc' AND entity_id=? AND entity_revision=? AND projection_json=?", record.identity, record.revision, record.payload) != 1 {
			t.Fatal("source change rewrote the original frozen projection")
		}
	}
	execSQL(t, s.db, "UPDATE ai_review_entries SET raw_value=CAST(x'ff' AS TEXT) WHERE id='accepted'")
	before = domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		for _, record := range current {
			if err := validateSearchProjectionSource(t.Context(), tx, record.identity, record.revision, []byte(record.payload)); !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid UTF-8 in actual reviewed source was accepted", err)
			}
		}
	})
	requireDomainState(t, s, before)
}

func TestCurrentSearchProjectionVerificationRejectsMissingActualUnitAuthority(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	inventory, err := NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "ActualOwner"})
	unit, err := inventory.CreateUnit(t.Context(), token, "source-unit-proof", item.ID, item.Revision, UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	identity := projectionPair("unit", unit.ID)
	payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	revision := scalar[int64](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateSearchProjectionSource(canceled, tx, identity, revision, []byte(payload)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled source verification proceeded", err)
	}
	if err := validateSearchProjectionSource(nil, tx, identity, revision, []byte(payload)); !errors.Is(err, ErrDomainState) {
		t.Fatal("nil context was accepted", err)
	}
	if _, err := tx.ExecContext(t.Context(), "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", unit.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", unit.ID); err != nil {
		t.Fatal(err)
	}
	if err := validateSearchProjectionSource(t.Context(), tx, identity, revision, []byte(payload)); !errors.Is(err, ErrDomainState) {
		t.Fatal("unbacked Unit search identity was accepted", err)
	}
}
