package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"homebox/internal/barcode"
)

// Exercise the private transaction/materialization boundary, not a fictional
// public owned-write service. Each future service must supply its own proofs.
func mutateOwnedCatalogFixture(ctx context.Context, auth *Auth, token, key, itemID, final string) (Alias, error) {
	request := struct{ ItemID, Final string }{itemID, final}
	return runDomainMutation(ctx, auth, token, key, "test_owned_catalog_publication", request, func(m *mutationTx) (Alias, error) {
		for _, stage := range []string{"Intermediate", final} {
			raw := stage + "Barcode00123"
			normalized, err := barcode.Normalize("CODE_128", raw)
			if err != nil {
				return Alias{}, err
			}
			for _, source := range []struct {
				kind, query string
				args        []any
			}{
				{"barcode", "UPDATE item_barcodes SET raw_value=?,normalized_value=? WHERE id='owned-barcode' AND item_id=?", []any{raw, normalized, itemID}},
				{"alias", "UPDATE item_aliases SET alias=? WHERE id='owned-alias' AND item_id=?", []any{stage + "Alias", itemID}},
				{"attribute", "UPDATE item_attributes SET attribute_key=?,attribute_value=? WHERE id='owned-attribute' AND item_id=?", []any{stage + "Key", stage + "Value", itemID}},
			} {
				if _, err := m.tx.ExecContext(ctx, source.query, source.args...); err != nil {
					return Alias{}, err
				}
				// A transient staged deletion must not leak if final state is live.
				if _, err := m.touch(source.kind, "owned-"+source.kind, stage == "Intermediate"); err != nil {
					return Alias{}, err
				}
			}
			if err := m.touchItemSearch(itemID, true); err != nil {
				return Alias{}, err
			}
		}
		return loadAlias(ctx, m.tx, "owned-alias")
	})
}

func TestCatalogLinkPublicationCoalescesFinalSourceOwnerAndUnitSearchWithoutCrossingOwnership(t *testing.T) {
	fixture := catalogWithOwnedRows(t)
	s, item := fixture.store, fixture.item
	execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unit',?,'UNIT-001','2026','2026')", item.ID)
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"id\":\"NeverCopyProjectionCache\"}' WHERE entity_type IN('barcode','alias','attribute') AND entity_id LIKE 'owned-%'")
	before := domainState(t, s)
	var reply Alias
	changed := requireProjectionReplay(t, s, func() {
		var err error
		reply, err = mutateOwnedCatalogFixture(t.Context(), fixture.auth, fixture.token, "owned-final-state", item.ID, "Final")
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, key := range []projectionKey{{"barcode", "owned-barcode"}, {"alias", "owned-alias"}, {"attribute", "owned-attribute"}, {"item", item.ID}, {"search_doc", projectionPair("item", item.ID)}, {"search_doc", projectionPair("unit", "unit")}} {
		if !changed[key] {
			t.Fatal("final-state publication omitted an independent row, owner or dependent search", key)
		}
	}
	if len(changed) != 6 || reply.Revision != 2 || reply.Alias != "FinalAlias" || getCatalogItem(t, fixture.catalog, fixture.token, item.ID).Revision != item.Revision+1 ||
		!reflect.DeepEqual(fixture.other, getCatalogItem(t, fixture.catalog, fixture.token, fixture.other.ID)) {
		t.Fatal("coalescing changed unrelated ownership or bumped a retained identity more than once")
	}
	b, err := loadBarcode(t.Context(), s.db, "owned-barcode")
	if err != nil {
		t.Fatal(err)
	}
	a, err := loadAttribute(t.Context(), s.db, "owned-attribute")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []struct {
		kind string
		data any
	}{{"barcode", b.BarcodeData}, {"alias", reply.AliasData}, {"attribute", a.AttributeData}} {
		encoded, err := json.Marshal(value.data)
		if err != nil || scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", value.kind, "owned-"+value.kind) != string(encoded) ||
			scalar[int](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type=? AND entity_id=?", value.kind, "owned-"+value.kind) != 2 {
			t.Fatal("publication copied stale cache text, private fields or an inner revision", value.kind, err)
		}
	}
	for _, text := range []string{"FinalBarcode00123", "FinalAlias", "FinalKey", "FinalValue"} {
		if !strings.Contains(approvedText(t, s, item.ID), text) || !strings.Contains(projectedSearchText(t, s, "unit", "unit"), text) ||
			scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH ?", text) != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH ?", text) != 1 {
			t.Fatal("final manual source was not published with its exact owner/unit search", text)
		}
	}
	for _, text := range []string{"Intermediate", "NeverCopyProjectionCache", "NeverPublishRawOCR"} {
		if strings.Contains(approvedText(t, s, item.ID), text) || strings.Contains(projectedSearchText(t, s, "unit", "unit"), text) ||
			scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE ?", "%"+text+"%") != 0 {
			t.Fatal("intermediate, cached or unreviewed evidence entered frozen publication", text)
		}
	}
	after := domainState(t, s)
	for _, table := range []string{"item_units", "stock_balances", "purchases", "purchase_lines", "media", "media_links", "ai_jobs", "ai_review_entries", "ocr_results"} {
		if after[table] != before[table] {
			t.Fatal("metadata publication mutated physical holdings or private evidence", table)
		}
	}
	if ack, err := fixture.catalog.Delete(t.Context(), fixture.token, "stale-owner-delete", item.ID, item.Revision); !errors.Is(err, ErrRevisionConflict) || ack.OK {
		t.Fatal("the old Item proof silently included subsequently changed owned metadata", err)
	}
	if _, err := mutateOwnedCatalogFixture(t.Context(), fixture.auth, fixture.token, "owned-later-state", item.ID, "Later"); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, fixture.auth)
	before = domainState(t, s)
	if frozen, err := mutateOwnedCatalogFixture(t.Context(), fixture.auth, fresh, "owned-final-state", item.ID, "Final"); err != nil || !reflect.DeepEqual(reply, frozen) {
		t.Fatal("a new active session could not reconcile the original frozen row response", err)
	}
	requireDomainState(t, s, before)

	changed = requireProjectionReplay(t, s, func() {
		_, err := runDomainMutation(t.Context(), fixture.auth, fresh, "owned-independent-deletion", "test_owned_tombstones", struct{}{}, func(m *mutationTx) (ActionAck, error) {
			var revision int64
			for _, source := range []struct{ table, kind string }{{"item_barcodes", "barcode"}, {"item_aliases", "alias"}, {"item_attributes", "attribute"}} {
				if _, err := m.tx.ExecContext(m.ctx, "DELETE FROM "+source.table+" WHERE id=? AND item_id=?", "owned-"+source.kind, item.ID); err != nil {
					return ActionAck{}, err
				}
				var err error
				revision, err = m.touch(source.kind, "owned-"+source.kind, true)
				if err != nil {
					return ActionAck{}, err
				}
			}
			if err := m.touchItemSearch(item.ID, true); err != nil {
				return ActionAck{}, err
			}
			return ActionAck{OK: true, Revision: revision, OperationID: m.eventID}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, kind := range []string{"barcode", "alias", "attribute"} {
		if !changed[projectionKey{kind, "owned-" + kind}] || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type=? AND entity_id=?", kind, "owned-"+kind) != 0 ||
			scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, "owned-"+kind) != 4 ||
			scalar[int](t, s.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", kind, "owned-"+kind) != 1 ||
			scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type=? AND entity_id=?", kind, "other-"+kind) != 1 {
			t.Fatal("deletion lost an independent retained identity, null tombstone or the other owner", kind)
		}
	}
	before = domainState(t, s)
	if frozen, err := mutateOwnedCatalogFixture(t.Context(), fixture.auth, fresh, "owned-final-state", item.ID, "Final"); err != nil || !reflect.DeepEqual(reply, frozen) {
		t.Fatal("source deletion prevented original-key acknowledgement reconciliation", err)
	}
	requireDomainState(t, s, before)
}

func TestCatalogLinkPublicationCreatesOneIndependentVersionOnePayloadPerNewSource(t *testing.T) {
	for _, kind := range []string{"barcode", "alias", "attribute"} {
		t.Run(kind, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			changed := requireProjectionReplay(t, s, func() {
				ack, err := runDomainMutation(t.Context(), auth, token, "new-independent-source", "test_new_owned_source", kind, func(m *mutationTx) (ActionAck, error) {
					var query string
					switch kind {
					case "barcode":
						query = "INSERT INTO item_barcodes VALUES('new-row',?,'CODE_128','00123','00123','2026')"
					case "alias":
						query = "INSERT INTO item_aliases VALUES('new-row',?,'  RetainedAlias  ','2026')"
					case "attribute":
						query = "INSERT INTO item_attributes VALUES('new-row',?,'Key','','2026')"
					}
					if _, err := m.tx.ExecContext(m.ctx, query, item.ID); err != nil {
						return ActionAck{}, err
					}
					revision, err := m.touch(kind, "new-row", false)
					if err != nil {
						return ActionAck{}, err
					}
					if err := m.touchItemSearch(item.ID, true); err != nil {
						return ActionAck{}, err
					}
					return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, nil
				})
				if err != nil || ack.Revision != 1 {
					t.Fatal("new source did not atomically acquire its own revision one", err)
				}
			})
			if len(changed) != 3 || !changed[projectionKey{kind, "new-row"}] || getCatalogItem(t, catalog, token, item.ID).Revision != item.Revision+1 {
				t.Fatal("new source did not publish its own row plus the owner/search dependency")
			}
		})
	}
}

func TestCatalogLinkPublicationRollsBackEverySourceProjectionSearchAndReceiptFailure(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"item_barcodes", "UPDATE"}, {"item_aliases", "UPDATE"}, {"item_attributes", "UPDATE"}, {"items", "UPDATE"},
		{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"}, {"current_projections", "INSERT"}, {"current_projections", "UPDATE"},
		{"search_documents", "INSERT"}, {"search_documents", "UPDATE"}, {"unit_search_documents", "INSERT"},
		{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			fixture := catalogWithOwnedRows(t)
			s := fixture.store
			execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unit',?,'UNIT-001','2026','2026')", fixture.item.ID)
			now := fixture.auth.now().Add(time.Minute)
			fixture.auth.now = func() time.Time { return now }
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_owned_publication BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'owned publication failure'); END")
			if value, err := mutateOwnedCatalogFixture(t.Context(), fixture.auth, fixture.token, "owned-failure-original-key", fixture.item.ID, "After"); err == nil || value.ID != "" {
				t.Fatal("failed publication exposed a successful-looking owned resource", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AliasTerm'") != 2 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AfterAlias'") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts") != 0 {
				t.Fatal("failed owned publication left partial FTS terms")
			}
			execSQL(t, s.db, "DROP TRIGGER fail_owned_publication")
			if value, err := mutateOwnedCatalogFixture(t.Context(), fixture.auth, fixture.token, "owned-failure-original-key", fixture.item.ID, "After"); err != nil || value.Revision != 2 {
				t.Fatal("rolled-back original command/key or SQL connection could not be explicitly reused", err)
			}
		})
	}
}

func TestCatalogLinkMaterializationWithholdsCorruptMissingAndExhaustedRows(t *testing.T) {
	for _, source := range []struct{ kind, table, field string }{{"barcode", "item_barcodes", "raw_value"}, {"alias", "item_aliases", "alias"}, {"attribute", "item_attributes", "attribute_key"}} {
		for _, failure := range []string{"corrupt", "missing", "overflow"} {
			t.Run(source.kind+failure, func(t *testing.T) {
				fixture := catalogWithOwnedRows(t)
				s := fixture.store
				id := "owned-" + source.kind
				if failure == "overflow" {
					setCatalogProjectionRevision(t, s, source.kind, id, maxSafeInteger)
				}
				before := domainState(t, s)
				ack, err := runDomainMutation(t.Context(), fixture.auth, fixture.token, "invalid-owned-publication", "test_invalid_owned_source", failure, func(m *mutationTx) (ActionAck, error) {
					query := "UPDATE " + source.table + " SET " + source.field + "='' WHERE id=?"
					if failure == "missing" {
						query = "DELETE FROM " + source.table + " WHERE id=?"
					}
					if _, err := m.tx.ExecContext(m.ctx, query, id); err != nil {
						return ActionAck{}, err
					}
					revision, err := m.touch(source.kind, id, false)
					if err != nil {
						return ActionAck{}, err
					}
					return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, nil
				})
				want := ErrDomainState
				if failure == "missing" {
					want = ErrNotFound
				} else if failure == "overflow" {
					want = ErrRevisionOverflow
				}
				if !errors.Is(err, want) || ack.OK {
					t.Fatal("invalid final source escaped a materializer as a successful acknowledgement", err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
}

func TestCatalogLinkPublicationLateAuthorizationAndCommitFailuresWithholdOwnedDTOs(t *testing.T) {
	for _, failure := range []string{"late revocation", "deferred commit"} {
		t.Run(failure, func(t *testing.T) {
			fixture := catalogWithOwnedRows(t)
			s := fixture.store
			if failure == "late revocation" {
				execSQL(t, s.db, "CREATE TRIGGER fail_owned_late AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
			} else {
				execSQL(t, s.db, "CREATE TABLE owned_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_owned_late AFTER INSERT ON mutation_events BEGIN INSERT INTO owned_commit_guard VALUES('missing'); END")
			}
			before := domainState(t, s)
			if value, err := mutateOwnedCatalogFixture(t.Context(), fixture.auth, fixture.token, "owned-late-original-key", fixture.item.ID, "After"); err == nil || value.ID != "" || failure == "late revocation" && !errors.Is(err, ErrAuthentication) {
				t.Fatal("late transaction failure exposed an owned success response", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AfterAlias'") != 0 {
				t.Fatal("failed COMMIT/authentication left changed published text")
			}
		})
	}
}
