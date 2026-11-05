package store

import (
	"errors"
	"strings"
	"testing"
)

func TestCatalogOwnedDuplicateEditsStayOwnerAndSymbologyScoped(t *testing.T) {
	for index, kind := range []string{"barcode", "alias"} {
		t.Run(kind, func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			writer := catalogLinkMutators(catalog)[index]
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			other := createCatalogItem(t, catalog, token, ItemCreate{Name: "Other"})
			value, err := writer.create(t.Context(), catalog, token, "owned-duplicate-first", item.ID, 1, "First")
			first := catalogLinkResult(t, value, err)
			value, err = writer.create(t.Context(), catalog, token, "owned-duplicate-second", item.ID, 2, "Second")
			second := catalogLinkResult(t, value, err)
			want := ErrBarcodeDuplicate
			if kind == "alias" {
				want = ErrAliasDuplicate
			}
			before := domainState(t, s)
			if _, err := writer.patch(t.Context(), catalog, token, "owned-duplicate-edit", second.ID, 1, "First"); !errors.Is(err, want) {
				t.Fatal("same-owner duplicate edit was upserted or partly applied", err)
			}
			requireDomainState(t, s, before)
			value, err = writer.create(t.Context(), catalog, token, "owned-duplicate-other", other.ID, 1, "First")
			shared := catalogLinkResult(t, value, err)
			if shared.ID == first.ID || shared.ItemID == first.ItemID {
				t.Fatal("a different owner shared identity rather than just a product value")
			}
			if kind == "barcode" {
				owner := getCatalogItem(t, catalog, token, item.ID)
				code, err := catalog.CreateBarcode(t.Context(), token, "same-text-other-symbology", item.ID, owner.Revision, BarcodeWrite{"OTHER", "First"})
				if err != nil || code.ID == first.ID {
					t.Fatal("normalization silently merged distinct symbology identities", err)
				}
			}
		})
	}
}

func TestCatalogOwnedRetainedInvalidBarcodeCanBeDeletedOrExplicitlyReplacedWithoutHiddenRepair(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	for _, retained := range []BarcodeData{
		{"new", item.ID, "EAN_13", "legacy unvalidated raw", "historical normalization"},
		{"tree", item.ID, "CODE_128", "舊條碼文字", "retained normalized"},
		{"valid-pair", item.ID, "EAN_13", "4006381333931", "older normalization"},
	} {
		execSQL(t, s.db, "INSERT INTO item_barcodes VALUES(?,?,?,?,?,'2026')", retained.ID, retained.ItemID, retained.Symbology, retained.RawValue, retained.NormalizedValue)
		seedCatalogProjection(t, s, "barcode", retained.ID, retained)
	}
	legacy, err := loadBarcode(t.Context(), s.db, "new")
	if err != nil || legacy.RawValue != "legacy unvalidated raw" || legacy.NormalizedValue != "historical normalization" {
		t.Fatal("retained representation was silently normalized on load", err)
	}
	before := domainState(t, s)
	if value, err := catalog.PatchBarcode(t.Context(), token, "invalid-legacy-replacement", legacy.ID, 1, BarcodeWrite{legacy.Symbology, legacy.RawValue}); !errors.Is(err, ErrDomainInput) || value.ID != "" {
		t.Fatal("a new replacement bypassed validation because its source was historical", err)
	}
	requireDomainState(t, s, before)
	replaced, err := catalog.PatchBarcode(t.Context(), token, "explicit-legacy-replacement", legacy.ID, 1, BarcodeWrite{"UPC_E", "04252614"})
	if err != nil || replaced.ID != "new" || replaced.ItemID != item.ID || replaced.Revision != 2 || replaced.NormalizedValue != "04252614" {
		t.Fatal("explicit reviewed replacement changed imported identity or failed current validation", err)
	}
	if ack, err := catalog.DeleteBarcode(t.Context(), token, "delete-invalid-legacy-value", "tree", 1); err != nil || ack.Revision != 2 {
		t.Fatal("deletion incorrectly required an invalid historical value to pass a new-write alphabet policy", err)
	}
	adopted, err := catalog.PatchBarcode(t.Context(), token, "explicit-current-normalization", "valid-pair", 1, BarcodeWrite{"EAN_13", "4006381333931"})
	if err != nil || adopted.NormalizedValue != adopted.RawValue || adopted.Revision != 2 {
		t.Fatal("explicit valid replacement retained a stale derived value as a false no-op", err)
	}
	id := strings.Repeat("界", 128)
	execSQL(t, s.db, "INSERT INTO item_aliases VALUES(?,?,'ImportedAlias','2026')", id, item.ID)
	seedCatalogProjection(t, s, "alias", id, AliasData{id, item.ID, "ImportedAlias"})
	alias, err := catalog.PatchAlias(t.Context(), token, "edit-full-length-imported-alias", id, 1, "ReviewedAlias")
	if err != nil || alias.ID != id || alias.Revision != 2 {
		t.Fatal("an imported original ID was rewritten or narrowed", err)
	}
	owner := getCatalogItem(t, catalog, token, item.ID)
	owner, err = catalog.Archive(t.Context(), token, "archive-owned-metadata", item.ID, owner.Revision, "Retain metadata")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.CreateAlias(t.Context(), token, "metadata-on-archived-item", item.ID, owner.Revision, "StillMetadata"); err != nil {
		t.Fatal("archival invented a metadata prohibition or physical holding mutation", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM item_units") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM stock_balances") != 0 {
		t.Fatal("owned metadata replacement or archival fabricated physical inventory")
	}
}

func TestCatalogOwnedAttributeFingerprintPreservesSuppliedFieldPresence(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	attribute, err := catalog.CreateAttribute(t.Context(), token, "attribute-presence-create", item.ID, 1, AttributeWrite{"Key", "Before"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.PatchAttribute(t.Context(), token, "attribute-presence-edit", attribute.ID, 1, AttributePatch{AttributeValue: testPointer("After")}); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	if value, err := catalog.PatchAttribute(t.Context(), token, "attribute-presence-edit", attribute.ID, 1, AttributePatch{AttributeKey: testPointer("Key"), AttributeValue: testPointer("After")}); !errors.Is(err, ErrIdempotencyConflict) || value.ID != "" {
		t.Fatal("equivalent realized values erased the original partial command's field presence", err)
	}
	requireDomainState(t, s, before)
}
