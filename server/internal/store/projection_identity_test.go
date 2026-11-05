package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectionPairUsesUnambiguousCanonicalUnicodeJSON(t *testing.T) {
	s := openTestStore(t)
	for _, fixture := range []struct{ first, second, encoded string }{
		{"item", "tag", `["item","tag"]`},
		{"a:b", "c", `["a:b","c"]`},
		{"a", "b:c", `["a","b:c"]`},
		{"房間😀", "<tag>&/", `["房間😀","<tag>&/"]`},
		{"quote\"slash\\", "line\n\t\r\b\f\x00\x1f", `["quote\"slash\\","line\n\t\r\b\f\u0000\u001f"]`},
		{"  ", "\x7f", "[\"  \",\"\x7f\"]"},
	} {
		got := projectionPair(fixture.first, fixture.second)
		if got != fixture.encoded || !json.Valid([]byte(got)) {
			t.Fatalf("canonical pair changed identity: %q != %q", got, fixture.encoded)
		}
		if scalar[string](t, s.db, "SELECT json_array(?,?)", fixture.first, fixture.second) != got {
			t.Fatal("SQLite migration and Go mutation would split a composite identity")
		}
		parts, valid := projectionPairParts(got)
		if !valid || parts[0] != fixture.first || parts[1] != fixture.second {
			t.Fatal("valid pair did not round-trip", got)
		}
	}
	if projectionPair("a:b", "c") == projectionPair("a", "b:c") {
		t.Fatal("delimiter collision joined independent link identities")
	}
	if projectionPair("", "tag") != "" || projectionPair("\xff", "tag") != "" {
		t.Fatal("canonicalization repaired an invalid component into a different identity")
	}
}

func TestProjectionIdentityBoundsApplyToComponentsNotTheEncodedPair(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 128), strings.Repeat("😀", 128), strings.Repeat("\x01", 128)} {
		pair := projectionPair(text, text)
		if !validProjectionIdentity("item_tag", pair) || !validProjectionIdentity("search_doc", projectionPair("item", text)) {
			t.Fatal("valid maximum-length component was rejected by a single-row ID limit")
		}
	}
	for _, kind := range []string{"location", "location_event", "media_link", "category", "tag", "item", "barcode", "alias", "attribute", "ai_review"} {
		if !validProjectionIdentity(kind, strings.Repeat("😀", 128)) || validProjectionIdentity(kind, strings.Repeat("x", 129)) {
			t.Fatal("single-row identity bounds changed", kind)
		}
	}
	if !validProjectionIdentity("search_doc", projectionPair("unit", "unit-id")) || validProjectionIdentity("search_doc", projectionPair("category", "category-id")) || validProjectionIdentity("unknown", "id") {
		t.Fatal("projection kind or searchable entity escaped its allowlist")
	}
}

func TestProjectionMutationUsesFullCompositeIdentityAndIndependentTombstone(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	itemID, tagID := strings.Repeat("😀", 128), strings.Repeat("<", 128)
	identity := projectionPair(itemID, tagID)
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Item','individual','2026','2026')", itemID)
	execSQL(t, s.db, "INSERT INTO tags(id,name) VALUES(?,'Tag')", tagID)
	execSQL(t, s.db, "INSERT INTO item_tags(item_id,tag_id) VALUES(?,?)", itemID, tagID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item_tag',?,1)", identity)
	execSQL(t, s.db, "INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('item_tag',?,1,json_object('item_id',?,'tag_id',?),'2026')", identity, itemID, tagID)
	ack, err := runDomainMutation(t.Context(), a, token, "composite-identity-delete", "test_delete_link", identity, func(m *mutationTx) (ActionAck, error) {
		if _, err := m.tx.ExecContext(m.ctx, "DELETE FROM item_tags WHERE item_id=? AND tag_id=?", itemID, tagID); err != nil {
			return ActionAck{}, err
		}
		revision, err := m.touch("item_tag", identity, true)
		return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, err
	})
	if err != nil || ack.Revision != 2 {
		t.Fatal("composite identity was rejected by a single-ID limit", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM item_tags") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='item_tag'") != 0 || scalar[string](t, s.db, "SELECT entity_id FROM sync_changes") != identity || scalar[string](t, s.db, "SELECT operation FROM sync_changes") != "delete" {
		t.Fatal("link deletion did not retain one exact independent tombstone")
	}
}

func TestProjectionPairRejectsAlternateSpellingsInvalidUTFAndWrongShapes(t *testing.T) {
	for _, identity := range []string{
		`null`, `{}`, `[]`, `["one"]`, `["one","two","three"]`, `[1,"two"]`,
		`["","two"]`, `[ "one", "two" ]`, `["a","\ud800"]`,
		`["a","\u000a"]`, `["a","b"] trailing`, "[\"\xff\",\"tag\"]",
		projectionPair(strings.Repeat("x", 129), "tag"), strings.Repeat("x", 1600),
	} {
		if _, valid := projectionPairParts(identity); valid || validProjectionIdentity("item_tag", identity) {
			t.Fatalf("noncanonical projection identity was accepted: %q", identity)
		}
	}
	escaped, err := json.Marshal([]string{"<", "tag"})
	if err != nil {
		t.Fatal(err)
	}
	if _, valid := projectionPairParts(string(escaped)); valid {
		t.Fatal("HTML-escaped alternate spelling created a second logical identity")
	}
}
