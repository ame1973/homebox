package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCatalogCommandsSnapshotCallerInputsBeforeFingerprintAndClock(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		s, catalog, auth, token := catalogFixture(t)
		taxonomy, err := NewTaxonomy(auth)
		if err != nil {
			t.Fatal(err)
		}
		category := createCategory(t, taxonomy, token, "ReviewedCategory", nil)
		other := createCategory(t, taxonomy, token, "DifferentCategory", nil)
		brand, model, categoryID := "ReviewedBrand", "ReviewedModel", category.ID
		mode, unit, base, status := "quantity", "kg", "g", "active"
		scale := int64(1000)
		input := ItemCreate{Name: "Reviewed item", Brand: &brand, Model: &model, CategoryID: &categoryID, TrackingMode: &mode,
			UnitCode: &unit, BaseUnit: &base, ScaleFactor: &scale, Status: &status}
		clock := auth.now
		auth.now = func() time.Time {
			brand, model, categoryID = "ChangedBrand", "ChangedModel", other.ID
			mode, unit, base, status, scale = "individual", "pcs", "pcs", "archived", 1
			return clock()
		}
		created, err := catalog.Create(t.Context(), token, "catalog-create-input-snapshot", input)
		auth.now = clock
		if err != nil || created.Brand == nil || *created.Brand != "ReviewedBrand" || created.Model == nil || *created.Model != "ReviewedModel" ||
			created.CategoryID == nil || *created.CategoryID != category.ID || created.TrackingMode != "quantity" || created.UnitCode != "kg" ||
			created.BaseUnit != "g" || created.ScaleFactor != 1000 || created.Status != "active" {
			t.Fatal("caller pointers changed the fingerprinted Item creation", created, err)
		}
		text := approvedText(t, s, created.ID)
		if !strings.Contains(text, "ReviewedBrand") || !strings.Contains(text, "ReviewedCategory") || strings.Contains(text, "ChangedBrand") ||
			getCategory(t, taxonomy, token, category.ID).Revision != category.Revision+1 || getCategory(t, taxonomy, token, other.ID).Revision != other.Revision {
			t.Fatal("Item creation published the wrong search or category authority")
		}
		brand, model, categoryID = "ReviewedBrand", "ReviewedModel", category.ID
		mode, unit, base, status, scale = "quantity", "kg", "g", "active", 1000
		replay, err := catalog.Create(t.Context(), token, "catalog-create-input-snapshot", input)
		if err != nil || !reflect.DeepEqual(created, replay) {
			t.Fatal("original Item creation did not reconcile", err)
		}
	})
	t.Run("patch", func(t *testing.T) {
		s, catalog, auth, token := catalogFixture(t)
		taxonomy, err := NewTaxonomy(auth)
		if err != nil {
			t.Fatal(err)
		}
		category := createCategory(t, taxonomy, token, "ReviewedCategory", nil)
		other := createCategory(t, taxonomy, token, "DifferentCategory", nil)
		item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Original item"})
		name, brand, model, categoryID := "Reviewed item", "ReviewedBrand", "ReviewedModel", category.ID
		description, notes := "Reviewed description", "Reviewed notes"
		mode, unit, base, status := "quantity", "kg", "g", "active"
		scale := int64(1000)
		input := ItemPatch{Name: &name, Brand: NullableString{Set: true, Value: &brand}, Model: NullableString{Set: true, Value: &model},
			CategoryID: NullableID{Set: true, Value: &categoryID}, Description: &description, Notes: &notes,
			TrackingMode: &mode, UnitCode: &unit, BaseUnit: &base, ScaleFactor: &scale, Status: &status}
		clock := auth.now
		auth.now = func() time.Time {
			name, brand, model, categoryID = "Changed item", "ChangedBrand", "ChangedModel", other.ID
			description, notes = "Changed description", "Changed notes"
			mode, unit, base, status, scale = "individual", "pcs", "pcs", "archived", 1
			return clock()
		}
		updated, err := catalog.Patch(t.Context(), token, "catalog-patch-input-snapshot", item.ID, item.Revision, input)
		auth.now = clock
		if err != nil || updated.Name != "Reviewed item" || updated.Brand == nil || *updated.Brand != "ReviewedBrand" || updated.Model == nil || *updated.Model != "ReviewedModel" ||
			updated.CategoryID == nil || *updated.CategoryID != category.ID || updated.Description != "Reviewed description" || updated.Notes != "Reviewed notes" ||
			updated.TrackingMode != "quantity" || updated.UnitCode != "kg" || updated.BaseUnit != "g" || updated.ScaleFactor != 1000 || updated.Status != "active" || updated.Revision != 2 {
			t.Fatal("caller pointers changed the fingerprinted Item patch", updated, err)
		}
		text := approvedText(t, s, item.ID)
		if !strings.Contains(text, "ReviewedBrand") || !strings.Contains(text, "ReviewedCategory") || strings.Contains(text, "ChangedBrand") ||
			getCategory(t, taxonomy, token, category.ID).Revision != category.Revision+1 || getCategory(t, taxonomy, token, other.ID).Revision != other.Revision {
			t.Fatal("Item patch published the wrong search or category authority")
		}
		name, brand, model, categoryID = "Reviewed item", "ReviewedBrand", "ReviewedModel", category.ID
		description, notes = "Reviewed description", "Reviewed notes"
		mode, unit, base, status, scale = "quantity", "kg", "g", "active", 1000
		replay, err := catalog.Patch(t.Context(), token, "catalog-patch-input-snapshot", item.ID, item.Revision, input)
		if err != nil || !reflect.DeepEqual(updated, replay) {
			t.Fatal("original Item patch did not reconcile", err)
		}
	})
	t.Run("equal item patch", func(t *testing.T) {
		s, catalog, auth, token := catalogFixture(t)
		item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Original item"})
		name := item.Name
		input := ItemPatch{Name: &name}
		changes := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes")
		clock := auth.now
		auth.now = func() time.Time {
			name = "Changed after fingerprint"
			return clock()
		}
		result, err := catalog.Patch(t.Context(), token, "catalog-equal-input-snapshot", item.ID, item.Revision, input)
		auth.now = clock
		if err != nil || !reflect.DeepEqual(item, result) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != changes {
			t.Fatal("fingerprinted equal Item intent published a change", result, err)
		}
		name = item.Name
		replay, err := catalog.Patch(t.Context(), token, "catalog-equal-input-snapshot", item.ID, item.Revision, input)
		if err != nil || !reflect.DeepEqual(item, replay) {
			t.Fatal("equal original Item intent did not reconcile", err)
		}
	})
	t.Run("attribute patch", func(t *testing.T) {
		s, catalog, auth, token := catalogFixture(t)
		item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Attribute owner"})
		attribute, err := catalog.CreateAttribute(t.Context(), token, "catalog-attribute-setup", item.ID, item.Revision, AttributeWrite{AttributeKey: "Old key", AttributeValue: "Old value"})
		if err != nil {
			t.Fatal(err)
		}
		key, value := "Reviewed key", "Reviewed value"
		input := AttributePatch{AttributeKey: &key, AttributeValue: &value}
		clock := auth.now
		auth.now = func() time.Time {
			key, value = "Changed key", "Changed value"
			return clock()
		}
		updated, err := catalog.PatchAttribute(t.Context(), token, "attribute-patch-input-snapshot", attribute.ID, attribute.Revision, input)
		auth.now = clock
		if err != nil || updated.AttributeKey != "Reviewed key" || updated.AttributeValue != "Reviewed value" || updated.Revision != 2 {
			t.Fatal("caller pointers changed the fingerprinted attribute patch", updated, err)
		}
		text := approvedText(t, s, item.ID)
		if !strings.Contains(text, "Reviewed value") || strings.Contains(text, "Changed value") {
			t.Fatal("attribute patch published the wrong approved search")
		}
		key, value = "Reviewed key", "Reviewed value"
		replay, err := catalog.PatchAttribute(t.Context(), token, "attribute-patch-input-snapshot", attribute.ID, attribute.Revision, input)
		if err != nil || !reflect.DeepEqual(updated, replay) {
			t.Fatal("original attribute patch did not reconcile", err)
		}
	})
	t.Run("equal attribute patch", func(t *testing.T) {
		s, catalog, auth, token := catalogFixture(t)
		item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Attribute owner"})
		attribute, err := catalog.CreateAttribute(t.Context(), token, "catalog-equal-attribute-setup", item.ID, item.Revision, AttributeWrite{AttributeKey: "Key", AttributeValue: "Original value"})
		if err != nil {
			t.Fatal(err)
		}
		value := attribute.AttributeValue
		input := AttributePatch{AttributeValue: &value}
		changes := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes")
		clock := auth.now
		auth.now = func() time.Time {
			value = "Changed after fingerprint"
			return clock()
		}
		result, err := catalog.PatchAttribute(t.Context(), token, "attribute-equal-input-snapshot", attribute.ID, attribute.Revision, input)
		auth.now = clock
		if err != nil || !reflect.DeepEqual(attribute, result) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != changes {
			t.Fatal("fingerprinted equal attribute intent published a change", result, err)
		}
		value = attribute.AttributeValue
		replay, err := catalog.PatchAttribute(t.Context(), token, "attribute-equal-input-snapshot", attribute.ID, attribute.Revision, input)
		if err != nil || !reflect.DeepEqual(attribute, replay) {
			t.Fatal("equal original attribute intent did not reconcile", err)
		}
	})
}
