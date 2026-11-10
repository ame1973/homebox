package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMediaLinkPatchPublishesIndependentRowAndActualOwnerScopes(t *testing.T) {
	for _, ownerType := range []string{"item", "unit", "location"} {
		t.Run(ownerType, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, ownerType)
			s := fixture.store
			description, position := "Reviewed é\ncaption", int64(8)
			input := MediaLinkPatch{Description: &description, SortOrder: &position}
			before := domainState(t, s)
			var unit Unit
			if ownerType == "unit" {
				var err error
				unit, err = loadUnit(t.Context(), s.db, fixture.ownerID)
				if err != nil {
					t.Fatal(err)
				}
			}
			var link MediaLink
			changed := requireProjectionReplay(t, s, func() {
				var err error
				link, err = library.PatchLink(t.Context(), fixture.token, "media-patch-original", "owned-a", 1, input)
				if err != nil {
					t.Fatal(err)
				}
			})
			if link.ID != "owned-a" || link.MediaID != "shared-a" || link.OwnerType != ownerType || link.OwnerID != fixture.ownerID || link.Revision != 2 ||
				!link.IsCover || link.SortOrder != 8 || link.Description != description {
				t.Fatal("changed link lost independent identity, revision or partial values", link)
			}
			for _, key := range []projectionKey{{"media_link", link.ID}, {"media", "shared-a"}, {ownerType, fixture.ownerID}} {
				if !changed[key] {
					t.Fatal("link edit omitted a real dependent aggregate", key)
				}
			}
			if ownerType == "unit" && !changed[projectionKey{"item", unit.ItemID}] {
				t.Fatal("Unit-owned link edit did not advance its Item")
			}
			for key := range changed {
				if key.kind == "search_doc" || key.kind == "inventory_event" || key.kind == "media" && key.id == "shared-b" || key.kind == "media_link" && key.id != link.ID {
					t.Fatal("link edit republished an unrelated scope", key)
				}
			}
			after := domainState(t, s)
			for _, table := range []string{"media", "inventory_events", "stock_balances", "ocr_results", "search_documents", "unit_search_documents"} {
				if before[table] != after[table] {
					t.Fatal("link edit changed unrelated source", table)
				}
			}
			if scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-a'") != 8 ||
				scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-b'") != 13 {
				t.Fatal("link edit changed the wrong Media membership")
			}
			replay, err := library.PatchLink(t.Context(), fixture.token, "media-patch-original", "owned-a", 1, input)
			if err != nil || !reflect.DeepEqual(replay, link) {
				t.Fatal("original-key retry did not return its original row", err)
			}
			if _, err = library.PatchLink(t.Context(), fixture.token, "media-patch-stale", "owned-a", 1, input); !errors.Is(err, ErrRevisionConflict) {
				t.Fatal("fresh key silently rebased the independent link proof", err)
			}
		})
	}
}

func TestMediaLinkPatchRetainsLegacyNegativePositionAndNoopReceipt(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	before := domainState(t, fixture.store)
	description := "\x00Exact é\n"
	input := MediaLinkPatch{Description: &description}
	link, err := library.PatchLink(t.Context(), fixture.token, "media-patch-equal-intent", "owned-a", 1, input)
	if err != nil || link.Revision != 1 || link.SortOrder != -3 || link.Description != description {
		t.Fatal("equal decision rewrote retained metadata or advanced the row", link, err)
	}
	after := domainState(t, fixture.store)
	for table, value := range before {
		if table != "mutation_events" && table != "api_idempotency" && table != "sessions" && table != "sqlite_sequence" && after[table] != value {
			t.Fatal("equal link intent changed source or projections", table)
		}
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='patch_media_links_id'") != 1 {
		t.Fatal("equal link intent was not recorded as an immutable receipt")
	}
	if replay, err := library.PatchLink(t.Context(), fixture.token, "media-patch-equal-intent", "owned-a", 1, input); err != nil || !reflect.DeepEqual(link, replay) {
		t.Fatal("equal original intent did not reconcile", err)
	}
	changedPresence := MediaLinkPatch{Description: &description, IsCover: testPointer(true)}
	if _, err = library.PatchLink(t.Context(), fixture.token, "media-patch-equal-intent", "owned-a", 1, changedPresence); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("different optional-field presence reused an original no-op claim", err)
	}
	if _, err = library.PatchLink(t.Context(), fixture.token, "media-negative-position", "owned-a", 1, MediaLinkPatch{SortOrder: testPointer(int64(-3))}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("retained negative position was incorrectly accepted as new input", err)
	}
}

func TestMediaLinkPatchEqualIntentPreservesAbsentLegacyOwnerProjection(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.ownerID)
	input := MediaLinkPatch{Description: testPointer("\x00Exact é\n")}
	link, err := library.PatchLink(t.Context(), fixture.token, "media-noop-legacy-owner", "owned-a", 1, input)
	if err != nil || link.Revision != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.ownerID) != 0 {
		t.Fatal("equal link intent invented an absent owner projection", link, err)
	}
}

func TestMediaLinkPatchSnapshotsCallerPointersBeforeFingerprintAndClock(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	cover, position, description := false, int64(4), "Original reviewed caption"
	input := MediaLinkPatch{IsCover: &cover, SortOrder: &position, Description: &description}
	clock := fixture.auth.now
	fixture.auth.now = func() time.Time {
		cover, position, description = true, 9, "Changed after fingerprint"
		return clock()
	}
	link, err := library.PatchLink(t.Context(), fixture.token, "media-patch-input-snapshot", "owned-a", 1, input)
	if err != nil || link.IsCover || link.SortOrder != 4 || link.Description != "Original reviewed caption" {
		t.Fatal("caller pointer changed reviewed link input", link, err)
	}
	fixture.auth.now = clock
	cover, position, description = false, 4, "Original reviewed caption"
	replay, err := library.PatchLink(t.Context(), fixture.token, "media-patch-input-snapshot", "owned-a", 1, input)
	if err != nil || !reflect.DeepEqual(link, replay) {
		t.Fatal("reviewed input and original-key fingerprint diverged", err)
	}
}

func TestMediaLinkPatchUsesOnlyItsOriginalProofAndReplaysAfterOwnerDeletion(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	catalog, err := NewCatalog(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	name := "Changed owner before link review"
	if _, err = catalog.Patch(t.Context(), fixture.token, "media-patch-concurrent-owner", fixture.ownerID, fixture.revision, ItemPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	sibling, err := loadItem(t.Context(), fixture.store.db, fixture.siblingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = library.Attach(t.Context(), fixture.token, "media-patch-concurrent-membership", "shared-a", 7, MediaLinkCreate{ItemID: &fixture.siblingID, OwnerRevision: sibling.Revision}); err != nil {
		t.Fatal(err)
	}
	caption := "Selected independent row only"
	input := MediaLinkPatch{Description: &caption}
	link, err := library.PatchLink(t.Context(), fixture.token, "media-patch-independent-proof", "owned-a", 1, input)
	if err != nil || link.Revision != 2 || link.Description != caption {
		t.Fatal("link writer invented a Media or owner proof", link, err)
	}
	if scalar[int64](t, fixture.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-a'") != 9 ||
		scalar[int64](t, fixture.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='item' AND entity_id=?", fixture.ownerID) != fixture.revision+2 {
		t.Fatal("link writer did not advance the actual current Media and owner scopes")
	}
	if _, err = fixture.delete(t.Context(), fixture.token, "media-delete-after-patch", fixture.revision+2); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.auth.Logout(t.Context(), fixture.token, "media-patch-logout-after-delete"); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, fixture.auth)
	if replay, err := library.PatchLink(t.Context(), fresh, "media-patch-independent-proof", "owned-a", 1, input); err != nil || !reflect.DeepEqual(replay, link) {
		t.Fatal("fresh-login reconciliation required a deleted current owner or link", err)
	}
	if _, err = library.PatchLink(t.Context(), fresh, "media-fresh-after-delete", "owned-a", 2, input); !errors.Is(err, ErrNotFound) {
		t.Fatal("fresh link mutation recreated deleted source", err)
	}
}

func TestMediaLinkPatchRejectsInvalidInputAndMissingActualAuthority(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	before := domainState(t, fixture.store)
	for _, input := range []MediaLinkPatch{
		{}, {SortOrder: testPointer(int64(-1))}, {SortOrder: testPointer(maxSafeInteger + 1)},
		{Description: testPointer(strings.Repeat("界", 10001))}, {Description: testPointer(string([]byte{0xff}))},
	} {
		if _, err := library.PatchLink(t.Context(), fixture.token, "media-invalid-patch", "owned-a", 1, input); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid link patch was accepted", err)
		}
	}
	if _, err := library.PatchLink(t.Context(), fixture.token, "media-missing-patch", "missing", 1, MediaLinkPatch{IsCover: testPointer(false)}); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing link became an empty source", err)
	}
	if _, err := library.PatchLink(t.Context(), fixture.token, "media-missing-patch-proof", "owned-a", 0, MediaLinkPatch{IsCover: testPointer(false)}); !errors.Is(err, ErrPreconditionRequired) {
		t.Fatal("missing independent link proof was accepted", err)
	}
	requireDomainState(t, fixture.store, before)
	for _, scope := range []string{"media", "media_link"} {
		t.Run(scope, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, "item")
			id := "shared-a"
			if scope == "media_link" {
				id = "owned-a"
			}
			execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", scope, id)
			execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", scope, id)
			before := domainState(t, fixture.store)
			if _, err := library.PatchLink(t.Context(), fixture.token, "media-missing-patch-authority", "owned-a", 1, MediaLinkPatch{Description: testPointer("changed")}); !errors.Is(err, ErrDomainState) {
				t.Fatal("missing real Media/link counter was reconstructed", err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestMediaLinkPatchRollsBackRevisionOverflow(t *testing.T) {
	for _, scope := range []string{"media_link", "media", "item", "catalog", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, "item")
			proof := int64(1)
			switch scope {
			case "media_link":
				setCatalogProjectionRevision(t, fixture.store, scope, "owned-a", maxSafeInteger)
				proof = maxSafeInteger
			case "media":
				setCatalogProjectionRevision(t, fixture.store, scope, "shared-a", maxSafeInteger)
			case "item":
				setCatalogProjectionRevision(t, fixture.store, scope, fixture.ownerID, maxSafeInteger)
			case "catalog":
				execSQL(t, fixture.store.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, fixture.store.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			}
			before := domainState(t, fixture.store)
			value, err := library.PatchLink(t.Context(), fixture.token, "media-link-overflow", "owned-a", proof, MediaLinkPatch{Description: testPointer("changed")})
			if !errors.Is(err, ErrRevisionOverflow) || value != (MediaLink{}) {
				t.Fatal("overflow returned a partial link result", value, err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestMediaLinkPatchEqualIntentRemainsAReceiptAtMaximumRevisions(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	setCatalogProjectionRevision(t, fixture.store, "media_link", "owned-a", maxSafeInteger)
	setCatalogProjectionRevision(t, fixture.store, "media", "shared-a", maxSafeInteger)
	setCatalogProjectionRevision(t, fixture.store, "item", fixture.ownerID, maxSafeInteger)
	execSQL(t, fixture.store.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
	execSQL(t, fixture.store.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
	input := MediaLinkPatch{Description: testPointer("\x00Exact é\n")}
	before := domainState(t, fixture.store)
	link, err := library.PatchLink(t.Context(), fixture.token, "media-noop-max-revisions", "owned-a", maxSafeInteger, input)
	if err != nil || link.Revision != maxSafeInteger {
		t.Fatal("equal intent incorrectly required available next projection revisions", link, err)
	}
	after := domainState(t, fixture.store)
	for table, value := range before {
		if table != "mutation_events" && table != "api_idempotency" && table != "sessions" && table != "sqlite_sequence" && after[table] != value {
			t.Fatal("equal maximum-revision intent changed source or projections", table)
		}
	}
}

func TestMediaLinkPatchConcurrentRequestsRespectItsOriginalIndependentProof(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-key=%t", sameKey), func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, "item")
			input := MediaLinkPatch{Description: testPointer("Concurrent reviewed caption")}
			type result struct {
				value MediaLink
				err   error
			}
			results := make(chan result, 2)
			var group sync.WaitGroup
			for index := range 2 {
				group.Add(1)
				go func(index int) {
					defer group.Done()
					key := fmt.Sprintf("media-patch-concurrent-%d", index)
					if sameKey {
						key = "media-patch-concurrent-same"
					}
					value, err := library.PatchLink(t.Context(), fixture.token, key, "owned-a", 1, input)
					results <- result{value, err}
				}(index)
			}
			group.Wait()
			close(results)
			var successful []MediaLink
			conflicts := 0
			for result := range results {
				if result.err == nil {
					successful = append(successful, result.value)
				} else if errors.Is(result.err, ErrRevisionConflict) && result.value == (MediaLink{}) {
					conflicts++
				} else {
					t.Fatal("unexpected concurrent link result", result.err)
				}
			}
			if sameKey && (len(successful) != 2 || conflicts != 0 || !reflect.DeepEqual(successful[0], successful[1])) || !sameKey && (len(successful) != 1 || conflicts != 1) {
				t.Fatal("concurrent link proof duplicated or failed to reconcile one edit")
			}
			if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='patch_media_links_id'") != 1 {
				t.Fatal("concurrent link commands duplicated their immutable audit")
			}
		})
	}
}

func TestMediaLinkPatchRollsBackLateTamperingForChangedAndEqualIntents(t *testing.T) {
	for _, failure := range []struct {
		name, setup string
		want        error
		changedOnly bool
	}{
		{"ignored link update", `CREATE TRIGGER fail_link_patch BEFORE UPDATE ON media_links BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState, true},
		{"ignored owner timestamp", `CREATE TRIGGER fail_link_patch BEFORE UPDATE OF updated_at ON items BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState, true},
		{"late link source", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE media_links SET description='tampered' WHERE id='owned-a'; END`, ErrDomainState, false},
		{"late link current", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media_link' AND entity_id='owned-a'; END`, ErrDomainState, false},
		{"late media sibling", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE media_links SET description='tampered' WHERE id='sibling'; END`, ErrDomainState, false},
		{"late owner sibling", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE media_links SET description='tampered' WHERE id='owned-c'; END`, ErrDomainState, false},
		{"late selected Media source", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE media SET storage_key='tampered' WHERE id='shared-a'; END`, ErrDomainState, false},
		{"late selected Media current", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media' AND entity_id='shared-a'; END`, ErrDomainState, false},
		{"late related Media source", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE media SET storage_key='tampered' WHERE id='shared-b'; END`, ErrDomainState, false},
		{"late related Media current", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media' AND entity_id='shared-b'; END`, ErrDomainState, false},
		{"late owner source", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE items SET name='tampered' WHERE id=(SELECT item_id FROM media_links WHERE id='owned-a'); END`, ErrDomainState, false},
		{"late owner current", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='item' AND entity_id=(SELECT item_id FROM media_links WHERE id='owned-a'); END`, ErrDomainState, false},
		{"late extra owner link", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN INSERT INTO media_links(id,media_id,item_id) SELECT 'late-extra','shared-b',item_id FROM media_links WHERE id='owned-a'; END`, ErrDomainState, false},
		{"late frozen row", `CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN DELETE FROM sync_changes WHERE entity_type='media_link' AND entity_id='owned-a' AND entity_revision=2; END`, ErrDomainState, true},
		{"late authorization", `CREATE TRIGGER fail_link_patch AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, ErrAuthentication, false},
		{"deferred commit", `CREATE TABLE media_patch_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT; CREATE TRIGGER fail_link_patch AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN INSERT INTO media_patch_guard VALUES('missing'); END`, nil, false},
	} {
		for _, equal := range []bool{false, true} {
			if equal && failure.changedOnly {
				continue
			}
			t.Run(fmt.Sprintf("%s/equal=%t", failure.name, equal), func(t *testing.T) {
				fixture, library, _ := mediaLibraryFixture(t, "item")
				caption := "New reviewed caption"
				if equal {
					caption = "\x00Exact é\n"
				}
				input := MediaLinkPatch{Description: &caption}
				execSQL(t, fixture.store.db, failure.setup)
				before := domainState(t, fixture.store)
				value, err := library.PatchLink(t.Context(), fixture.token, "media-failed-link-patch", "owned-a", 1, input)
				if err == nil || value != (MediaLink{}) || failure.want != nil && !errors.Is(err, failure.want) {
					t.Fatal("ignored or changed source escaped link transaction", value, err)
				}
				requireDomainState(t, fixture.store, before)
				if failure.name == "deferred commit" && scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_patch_guard") != 0 {
					t.Fatal("failed deferred COMMIT retained a guard row")
				}
				execSQL(t, fixture.store.db, "DROP TRIGGER fail_link_patch")
				if _, err = library.PatchLink(t.Context(), fixture.token, "media-failed-link-patch", "owned-a", 1, input); err != nil {
					t.Fatal("rolled-back failure retained an unusable original key", err)
				}
			})
		}
	}
}

func TestMediaLinkPatchEqualIntentRechecksUnitAndLocationAuthority(t *testing.T) {
	for _, tc := range []struct{ ownerType, kind, id string }{
		{"unit", "unit", "unit"}, {"unit", "location", "root"}, {"location", "location", ""},
	} {
		t.Run(tc.ownerType+"/"+tc.kind+"/"+tc.id, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, tc.ownerType)
			id := tc.id
			if id == "" {
				id = fixture.ownerID
			}
			execSQL(t, fixture.store.db, `CREATE TRIGGER fail_link_noop_owner AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='`+tc.kind+`' AND entity_id='`+id+`'; END`)
			before := domainState(t, fixture.store)
			input := MediaLinkPatch{Description: testPointer("\x00Exact é\n")}
			if value, err := library.PatchLink(t.Context(), fixture.token, "media-noop-owner-projection", "owned-a", 1, input); !errors.Is(err, ErrDomainState) || value != (MediaLink{}) {
				t.Fatal("equal intent exposed a changed Unit or ancestor projection", value, err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestMediaLinkPatchValidatesMembershipBeyondOnePage(t *testing.T) {
	for _, scope := range []string{"media", "owner"} {
		for _, equal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/equal=%t", scope, equal), func(t *testing.T) {
				fixture, library, _ := mediaLibraryFixture(t, "location")
				if scope == "media" {
					seedMediaLinkBatch(t, fixture.store, "item_id", "shared-a", fixture.siblingID, "media-patch-sibling-", 205)
				} else {
					seedMediaLinkBatch(t, fixture.store, "location_id", "shared-b", fixture.ownerID, "media-patch-sibling-", 205)
				}
				execSQL(t, fixture.store.db, `CREATE TRIGGER fail_link_patch_large AFTER INSERT ON mutation_events WHEN NEW.operation='patch_media_links_id' BEGIN UPDATE media_links SET description='late change' WHERE id='media-patch-sibling-204'; END`)
				before := domainState(t, fixture.store)
				caption := "New caption"
				if equal {
					caption = "\x00Exact é\n"
				}
				input := MediaLinkPatch{Description: &caption}
				if _, err := library.PatchLink(t.Context(), fixture.token, "media-large-link-patch", "owned-a", 1, input); !errors.Is(err, ErrDomainState) {
					t.Fatal("late membership change beyond one page escaped validation", err)
				}
				requireDomainState(t, fixture.store, before)
				execSQL(t, fixture.store.db, "DROP TRIGGER fail_link_patch_large")
				if _, err := library.PatchLink(t.Context(), fixture.token, "media-large-link-patch", "owned-a", 1, input); err != nil {
					t.Fatal("failed large membership check retained an unusable key", err)
				}
			})
		}
	}
}

func TestMediaLinkPatchConflictsOnlyWhenNewlySelectingAnotherCover(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	text := "Retain existing legacy covers"
	updated, err := library.PatchLink(t.Context(), fixture.token, "media-patch-existing-cover", "owned-a", 1, MediaLinkPatch{Description: &text})
	if err != nil || !updated.IsCover {
		t.Fatal("legacy cover metadata could not be edited without silently repairing other covers", err)
	}
	cleared, err := library.PatchLink(t.Context(), fixture.token, "media-clear-cover", "owned-b", 1, MediaLinkPatch{IsCover: testPointer(false)})
	if err != nil || cleared.IsCover || cleared.Revision != 2 {
		t.Fatal("explicit cover clearing failed", err)
	}
	before := domainState(t, fixture.store)
	if _, err = library.PatchLink(t.Context(), fixture.token, "media-conflicting-cover", "owned-b", 2, MediaLinkPatch{IsCover: testPointer(true)}); !errors.Is(err, ErrMediaCoverConflict) {
		t.Fatal("new cover silently displaced another independent cover", err)
	}
	requireDomainState(t, fixture.store, before)
	if _, err = library.PatchLink(t.Context(), fixture.token, "media-clear-cover-a", "owned-a", 2, MediaLinkPatch{IsCover: testPointer(false)}); err != nil {
		t.Fatal("explicitly clearing the first retained cover failed", err)
	}
	if _, err = library.PatchLink(t.Context(), fixture.token, "media-clear-cover-c", "owned-c", 1, MediaLinkPatch{IsCover: testPointer(false)}); err != nil {
		t.Fatal("explicitly clearing the second retained cover failed", err)
	}
	selected, err := library.PatchLink(t.Context(), fixture.token, "media-select-new-cover", "owned-b", 2, MediaLinkPatch{IsCover: testPointer(true)})
	if err != nil || !selected.IsCover || selected.Revision != 3 {
		t.Fatal("cleared owner could not explicitly select a new cover", err)
	}
}
