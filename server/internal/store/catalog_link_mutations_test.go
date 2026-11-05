package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// These adapters exercise the real typed methods, not a fake storage backend.
type catalogLinkMutator struct {
	kind, table string
	create      func(context.Context, *Catalog, string, string, string, int64, string) (any, error)
	patch       func(context.Context, *Catalog, string, string, string, int64, string) (any, error)
	delete      func(context.Context, string, string, string, int64) (ActionAck, error)
}

func catalogLinkMutators(catalog *Catalog) []catalogLinkMutator {
	return []catalogLinkMutator{
		{"barcode", "item_barcodes", func(ctx context.Context, c *Catalog, token, key, id string, revision int64, value string) (any, error) {
			return c.CreateBarcode(ctx, token, key, id, revision, BarcodeWrite{Symbology: "CODE_128", RawValue: value})
		}, func(ctx context.Context, c *Catalog, token, key, id string, revision int64, value string) (any, error) {
			return c.PatchBarcode(ctx, token, key, id, revision, BarcodeWrite{Symbology: "CODE_128", RawValue: value})
		}, catalog.DeleteBarcode},
		{"alias", "item_aliases", func(ctx context.Context, c *Catalog, token, key, id string, revision int64, value string) (any, error) {
			return c.CreateAlias(ctx, token, key, id, revision, value)
		}, func(ctx context.Context, c *Catalog, token, key, id string, revision int64, value string) (any, error) {
			return c.PatchAlias(ctx, token, key, id, revision, value)
		}, catalog.DeleteAlias},
		{"attribute", "item_attributes", func(ctx context.Context, c *Catalog, token, key, id string, revision int64, value string) (any, error) {
			return c.CreateAttribute(ctx, token, key, id, revision, AttributeWrite{AttributeKey: "RepeatedKey", AttributeValue: value})
		}, func(ctx context.Context, c *Catalog, token, key, id string, revision int64, value string) (any, error) {
			return c.PatchAttribute(ctx, token, key, id, revision, AttributePatch{AttributeValue: &value})
		}, catalog.DeleteAttribute},
	}
}

type catalogLinkReference struct {
	ID       string `json:"id"`
	ItemID   string `json:"item_id"`
	Revision int64  `json:"revision"`
}

func catalogLinkResult(t *testing.T, value any, err error) catalogLinkReference {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result catalogLinkReference
	if json.Unmarshal(encoded, &result) != nil || !validDomainID(result.ID) || !validDomainID(result.ItemID) || result.Revision < 1 {
		t.Fatal("owned write did not return a complete independent resource")
	}
	return result
}

func TestCatalogOwnedWritesKeepIndependentRowsFrozenRepliesAndExactAtomicDependencies(t *testing.T) {
	for _, kind := range []string{"barcode", "alias", "attribute"} {
		t.Run(kind, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			other := createCatalogItem(t, catalog, token, ItemCreate{Name: "OtherOwner"})
			var writer catalogLinkMutator
			for _, candidate := range catalogLinkMutators(catalog) {
				if candidate.kind == kind {
					writer = candidate
				}
			}
			// Units/AI remain explicit fixtures until their domain services exist.
			execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unit',?,'UNIT-001','2026','2026')", item.ID)
			execSQL(t, s.db, "INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,created_at,updated_at) VALUES('unreviewed',?,'keyword','NeverPublishUnreviewedOwned','needs_review','2026','2026')", item.ID)
			before := domainState(t, s)
			var original any
			var row catalogLinkReference
			changed := requireProjectionReplay(t, s, func() {
				var err error
				original, err = writer.create(t.Context(), catalog, token, "owned-create-original", item.ID, item.Revision, "BeforeTerm")
				row = catalogLinkResult(t, original, err)
			})
			if row.ItemID != item.ID || row.Revision != 1 || len(changed) != 4 || !changed[projectionKey{kind, row.ID}] || !changed[projectionKey{"item", item.ID}] ||
				!changed[projectionKey{"search_doc", projectionPair("item", item.ID)}] || !changed[projectionKey{"search_doc", projectionPair("unit", "unit")}] ||
				!regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(row.ID) {
				t.Fatal("owned creation mixed identity, proof or atomic publication scopes")
			}
			createdState := domainState(t, s)
			for _, table := range []string{"item_units", "stock_balances", "purchases", "purchase_lines", "ai_review_entries", "media"} {
				if createdState[table] != before[table] {
					t.Fatal("owned metadata creation fabricated physical or machine evidence", table)
				}
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE '%NeverPublishUnreviewedOwned%'") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'BeforeTerm'") != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'BeforeTerm'") != 1 {
				t.Fatal("owned creation omitted approved search or published a raw AI candidate")
			}
			if ack, err := catalog.Delete(t.Context(), token, "stale-owned-item-delete", item.ID, item.Revision); !errors.Is(err, ErrRevisionConflict) || ack.OK {
				t.Fatal("old Item deletion proof silently absorbed a newly created row", err)
			}
			// Independent edits remain valid when unrelated owner metadata advances.
			current := getCatalogItem(t, catalog, token, item.ID)
			if _, err := catalog.Patch(t.Context(), token, "unrelated-owner-metadata", item.ID, current.Revision, ItemPatch{Name: testPointer("ChangedOwner")}); err != nil {
				t.Fatal(err)
			}
			ownerBefore := getCatalogItem(t, catalog, token, item.ID)
			changed = requireProjectionReplay(t, s, func() {
				value, err := writer.patch(t.Context(), catalog, token, "owned-patch-original", row.ID, row.Revision, "AfterTerm")
				patched := catalogLinkResult(t, value, err)
				if patched.ID != row.ID || patched.ItemID != item.ID || patched.Revision != 2 {
					t.Fatal("row edit used the Item's revision or changed immutable identity")
				}
			})
			if len(changed) != 4 || getCatalogItem(t, catalog, token, item.ID).Revision != ownerBefore.Revision+1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'BeforeTerm'") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'AfterTerm'") != 1 ||
				!reflect.DeepEqual(other, getCatalogItem(t, catalog, token, other.ID)) {
				t.Fatal("row edit did not atomically replace only its owner's text/dependencies")
			}
			before = domainState(t, s)
			value, err := writer.patch(t.Context(), catalog, token, "owned-patch-noop", row.ID, 2, "AfterTerm")
			noop := catalogLinkResult(t, value, err)
			if noop.Revision != 2 {
				t.Fatal("equal metadata bumped the independent row")
			}
			after := domainState(t, s)
			for table, contents := range before {
				if table != "api_idempotency" && table != "mutation_events" && after[table] != contents {
					t.Fatal("no-op changed more than its immutable receipt", table)
				}
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation=?", "patch_"+strings.TrimPrefix(writer.table, "item_")+"_id") != 2 {
				t.Fatal("no-op did not receive its own immutable operation receipt")
			}
			_, fresh := login(t, auth)
			before = domainState(t, s)
			if frozen, err := writer.create(t.Context(), catalog, fresh, "owned-create-original", item.ID, item.Revision, "BeforeTerm"); err != nil || !reflect.DeepEqual(frozen, original) {
				t.Fatal("original-key replay was rebased onto current owner/row metadata", err)
			}
			requireDomainState(t, s, before)
			var ack ActionAck
			changed = requireProjectionReplay(t, s, func() {
				var err error
				ack, err = writer.delete(t.Context(), fresh, "owned-delete-original", row.ID, 2)
				if err != nil || !ack.OK || ack.Revision != 3 || ack.OperationID == "" {
					t.Fatal("deletion acknowledgement did not use the independent row proof", err)
				}
			})
			if len(changed) != 4 || scalar[int](t, s.db, "SELECT COUNT(*) FROM "+writer.table+" WHERE id=?", row.ID) != 0 ||
				scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, row.ID) != 3 ||
				scalar[int](t, s.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", kind, row.ID) != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AfterTerm'") != 0 {
				t.Fatal("deletion lost retained identity, independent tombstone or search cleanup")
			}
			before = domainState(t, s)
			if replay, err := writer.delete(t.Context(), fresh, "owned-delete-original", row.ID, 2); err != nil || replay != ack {
				t.Fatal("explicit deletion reconciliation depended on a now-missing source", err)
			}
			if frozen, err := writer.create(t.Context(), catalog, fresh, "owned-create-original", item.ID, item.Revision, "BeforeTerm"); err != nil || !reflect.DeepEqual(frozen, original) {
				t.Fatal("source deletion invalidated the frozen creation acknowledgement", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogOwnedExactValuesSharedProductsAndDuplicateRules(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	other := createCatalogItem(t, catalog, token, ItemCreate{Name: "Other"})
	first, err := catalog.CreateBarcode(t.Context(), token, "exact-first-barcode", item.ID, 1, BarcodeWrite{"UPC_A", "036000291452"})
	if err != nil || first.RawValue != "036000291452" || first.NormalizedValue != first.RawValue {
		t.Fatal("new barcode did not preserve its validated leading zero", err)
	}
	shared, err := catalog.CreateBarcode(t.Context(), token, "exact-shared-barcode", other.ID, 1, BarcodeWrite{"UPC_A", "036000291452"})
	if err != nil || shared.ID == first.ID || shared.ItemID == first.ItemID {
		t.Fatal("shared commercial value acquired a global uniqueness/identity constraint", err)
	}
	before := domainState(t, s)
	if value, err := catalog.CreateBarcode(t.Context(), token, "duplicate-product-value", item.ID, 2, BarcodeWrite{"UPC_A", "036000291452"}); !errors.Is(err, ErrBarcodeDuplicate) || value.ID != "" {
		t.Fatal("same-owner/symbology duplicate was implicitly upserted", err)
	}
	requireDomainState(t, s, before)
	for i, text := range []string{"ExactAlias", "exactalias", " ExactAlias ", "é", "é"} {
		owner := getCatalogItem(t, catalog, token, item.ID)
		alias, err := catalog.CreateAlias(t.Context(), token, "exact-alias-"+string(rune('a'+i)), item.ID, owner.Revision, text)
		if err != nil || alias.Alias != text {
			t.Fatal("alias text was case/whitespace/Unicode normalized", err)
		}
	}
	owner := getCatalogItem(t, catalog, token, item.ID)
	before = domainState(t, s)
	if value, err := catalog.CreateAlias(t.Context(), token, "duplicate-exact-alias", item.ID, owner.Revision, "ExactAlias"); !errors.Is(err, ErrAliasDuplicate) || value.ID != "" {
		t.Fatal("exact duplicate alias became an implicit replacement", err)
	}
	requireDomainState(t, s, before)
	for _, key := range []string{"repeat-attribute-one", "repeat-attribute-two"} {
		owner = getCatalogItem(t, catalog, token, item.ID)
		attribute, err := catalog.CreateAttribute(t.Context(), token, key, item.ID, owner.Revision, AttributeWrite{"RepeatedKey", ""})
		if err != nil || attribute.AttributeKey != "RepeatedKey" || attribute.AttributeValue != "" {
			t.Fatal("duplicate-key/empty attributes were merged or rejected", err)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM item_attributes WHERE item_id=? AND attribute_key='RepeatedKey'", item.ID) != 2 {
		t.Fatal("repeated keys lost independent stable rows")
	}
}

func TestCatalogOwnedAttributePartialEditsAndBarcodeAtomicPairReplacement(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	attribute, err := catalog.CreateAttribute(t.Context(), token, "partial-attribute-create", item.ID, 1, AttributeWrite{"OriginalKey", "OriginalValue"})
	if err != nil {
		t.Fatal(err)
	}
	attribute, err = catalog.PatchAttribute(t.Context(), token, "partial-attribute-key", attribute.ID, 1, AttributePatch{AttributeKey: testPointer("NewKey")})
	if err != nil || attribute.AttributeValue != "OriginalValue" || attribute.Revision != 2 {
		t.Fatal("partial key edit lost the omitted value", err)
	}
	attribute, err = catalog.PatchAttribute(t.Context(), token, "partial-attribute-clear", attribute.ID, 2, AttributePatch{AttributeValue: testPointer("")})
	if err != nil || attribute.AttributeKey != "NewKey" || attribute.AttributeValue != "" || attribute.Revision != 3 {
		t.Fatal("partial supplied-empty edit was ignored or replaced the omitted key", err)
	}
	owner := getCatalogItem(t, catalog, token, item.ID)
	code, err := catalog.CreateBarcode(t.Context(), token, "atomic-pair-create", item.ID, owner.Revision, BarcodeWrite{"EAN_13", "4006381333931"})
	if err != nil {
		t.Fatal(err)
	}
	code, err = catalog.PatchBarcode(t.Context(), token, "atomic-pair-replace", code.ID, 1, BarcodeWrite{"UPC_E", "04252614"})
	if err != nil || code.Symbology != "UPC_E" || code.RawValue != "04252614" || code.NormalizedValue != "04252614" || code.Revision != 2 {
		t.Fatal("atomic replacement mixed old/new barcode interpretation or expanded its identity", err)
	}
	before := domainState(t, s)
	if value, err := catalog.PatchBarcode(t.Context(), token, "atomic-pair-invalid", code.ID, 2, BarcodeWrite{"EAN_13", "04252614"}); !errors.Is(err, ErrDomainInput) || value.ID != "" {
		t.Fatal("invalid replacement partly changed symbology/raw/normalization", err)
	}
	requireDomainState(t, s, before)
}
