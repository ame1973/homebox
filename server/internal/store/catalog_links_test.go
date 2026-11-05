package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogLinkResultsEnforceUnicodeBoundsAndSafeIndependentRevisions(t *testing.T) {
	id := strings.Repeat("界", 128)
	invalidUTF8 := string([]byte{0xff})
	barcode := Barcode{BarcodeData{id, id, "EAN_13", strings.Repeat("😀", 512), "retained legacy normalization"}, maxSafeInteger}
	alias := Alias{AliasData{id, id, strings.Repeat("界", 300)}, maxSafeInteger}
	attribute := Attribute{AttributeData{id, id, strings.Repeat("界", 100), strings.Repeat("界", 10000)}, maxSafeInteger}
	for _, value := range []domainResult{barcode, alias, attribute} {
		if err := value.validateDomainResult(); err != nil {
			t.Fatal("valid retained representation at Unicode/safe-counter bounds was rejected", err)
		}
	}
	for _, owner := range []bool{false, true} {
		b, a, v := barcode, alias, attribute
		invalidID := strings.Repeat("界", 129)
		if owner {
			b.ItemID, a.ItemID, v.ItemID = invalidID, invalidID, invalidID
		} else {
			b.ID, a.ID, v.ID = invalidID, invalidID, invalidID
		}
		for _, value := range []domainResult{b, a, v} {
			if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
				t.Fatal("independent row/owner ID exceeded its original code-point bound", err)
			}
		}
	}
	for _, symbology := range []string{"EAN_13", "EAN_8", "UPC_A", "UPC_E", "CODE_128", "QR_CODE", "OTHER"} {
		value := barcode
		value.Symbology = symbology
		if err := value.validateDomainResult(); err != nil {
			t.Fatal("persisted representation was silently reinterpreted by a new-write policy", symbology, err)
		}
	}
	for _, field := range []string{"id", "item_id", "symbology", "raw_value", "normalized_value"} {
		for _, invalid := range []string{"", invalidUTF8, strings.Repeat("界", 513)} {
			value := barcode
			switch field {
			case "id":
				value.ID = invalid
			case "item_id":
				value.ItemID = invalid
			case "symbology":
				value.Symbology = invalid
			case "raw_value":
				value.RawValue = invalid
			case "normalized_value":
				value.NormalizedValue = invalid
			}
			if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid barcode result escaped the source boundary", field, err)
			}
		}
	}
	for _, invalid := range []string{"", invalidUTF8, strings.Repeat("界", 301)} {
		for _, field := range []string{"id", "item_id", "alias"} {
			value := alias
			switch field {
			case "id":
				value.ID = invalid
			case "item_id":
				value.ItemID = invalid
			case "alias":
				value.Alias = invalid
			}
			if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid alias result escaped the source boundary", field, err)
			}
		}
	}
	for _, field := range []string{"id", "item_id", "attribute_key", "attribute_value"} {
		for _, invalid := range []string{invalidUTF8, strings.Repeat("界", 10001)} {
			value := attribute
			switch field {
			case "id":
				value.ID = invalid
			case "item_id":
				value.ItemID = invalid
			case "attribute_key":
				value.AttributeKey = invalid
			case "attribute_value":
				value.AttributeValue = invalid
			}
			if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid attribute result escaped the source boundary", field, err)
			}
		}
	}
	for _, revision := range []int64{-1, 0, maxSafeInteger + 1} {
		b, a, v := barcode, alias, attribute
		b.Revision, a.Revision, v.Revision = revision, revision, revision
		for _, value := range []domainResult{b, a, v} {
			if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid independent counter was accepted", revision, err)
			}
		}
	}
	for _, value := range []Attribute{
		{AttributeData{"", id, "Key", "Value"}, 1},
		{AttributeData{id, "", "Key", "Value"}, 1},
		{AttributeData{id, id, "", "Value"}, 1},
		{AttributeData{id, id, strings.Repeat("界", 101), "Value"}, 1},
	} {
		if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
			t.Fatal("invalid attribute identity/key was accepted", err)
		}
	}
	attribute.AttributeValue = ""
	if err := attribute.validateDomainResult(); err != nil {
		t.Fatal("supplied-empty attribute value was rejected", err)
	}
	for _, value := range []Barcode{
		{BarcodeData{id, id, "OTHER", "\x00", "\x00"}, 1},
		{BarcodeData{id, id, "CODE_128", "\x00GS1\x1d00123", "\x00GS1\x1d00123"}, 1},
	} {
		if err := value.validateDomainResult(); err != nil {
			t.Fatal("native control bytes were trimmed or rejected", err)
		}
	}
}

func TestCatalogLinkLoadersRequireRetainedCountersAndWithholdPartialResults(t *testing.T) {
	s := openTestStore(t)
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('owner','Owner','individual','2026','2026')")
	execSQL(t, s.db, "INSERT INTO item_barcodes VALUES('row','owner','EAN_13','retained raw','retained normalization','2026')")
	execSQL(t, s.db, "INSERT INTO item_aliases VALUES('row','owner','  Exact é  ','2026')")
	execSQL(t, s.db, "INSERT INTO item_attributes VALUES('row','owner','Key','','2026')")
	for _, check := range []struct {
		kind string
		load func(context.Context, queryer, string) (domainResult, error)
		scan func(rowScanner) (domainResult, error)
		want map[string]any
	}{
		{"barcode", func(ctx context.Context, q queryer, id string) (domainResult, error) { return loadBarcode(ctx, q, id) }, func(row rowScanner) (domainResult, error) { return scanBarcode(row) }, map[string]any{"id": "row", "item_id": "owner", "symbology": "EAN_13", "raw_value": "retained raw", "normalized_value": "retained normalization", "revision": float64(7)}},
		{"alias", func(ctx context.Context, q queryer, id string) (domainResult, error) { return loadAlias(ctx, q, id) }, func(row rowScanner) (domainResult, error) { return scanAlias(row) }, map[string]any{"id": "row", "item_id": "owner", "alias": "  Exact é  ", "revision": float64(7)}},
		{"attribute", func(ctx context.Context, q queryer, id string) (domainResult, error) {
			return loadAttribute(ctx, q, id)
		}, func(row rowScanner) (domainResult, error) { return scanAttribute(row) }, map[string]any{"id": "row", "item_id": "owner", "attribute_key": "Key", "attribute_value": "", "revision": float64(7)}},
	} {
		t.Run(check.kind, func(t *testing.T) {
			before := domainState(t, s)
			if value, err := check.load(t.Context(), s.db, "row"); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("loader fabricated revision one or exposed a partial source row", err)
			}
			requireDomainState(t, s, before)
			execSQL(t, s.db, "INSERT INTO projection_revisions VALUES(?,'row',7)", check.kind)
			value, err := check.load(t.Context(), s.db, "row")
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var resource map[string]any
			if json.Unmarshal(encoded, &resource) != nil || !reflect.DeepEqual(resource, check.want) {
				t.Fatal("loader changed exact field presence, Unicode, retained text or independent revision")
			}
			if value, err := check.load(t.Context(), s.db, "missing"); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("missing source did not return an empty not-found result", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if value, err := check.load(ctx, s.db, "row"); !errors.Is(err, context.Canceled) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("failed SQL lookup exposed a partial source", err)
			}
			if value, err := check.scan(s.db.QueryRowContext(t.Context(), "SELECT 1")); err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("failed SQL scan exposed a partial source", err)
			}
			execSQL(t, s.db, "UPDATE projection_revisions SET revision=? WHERE entity_type=? AND entity_id='row'", maxSafeInteger+1, check.kind)
			if value, err := check.load(t.Context(), s.db, "row"); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("unsafe retained counter escaped a loader", err)
			}
		})
	}
}

func TestCatalogLinkBackfillValidationRejectsFailedTransactionQueries(t *testing.T) {
	s := openTestStore(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateCatalogLinkBackfill(ctx, tx); !errors.Is(err, context.Canceled) {
		t.Fatal("migration validation swallowed a failed transaction query", err)
	}
}
