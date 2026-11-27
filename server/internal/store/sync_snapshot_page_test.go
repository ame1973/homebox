package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type snapshotPageFixtureRow struct {
	id, payload string
	revision    int64
}

// This test fixture signs selected rows directly. It is not an authoritative
// snapshot producer or proof that every eligible source has been materialized.
func snapshotPageFixture(t *testing.T) (*Store, *Auth, *Locations, *time.Time, string, syncSnapshotCursor, string, []snapshotPageFixtureRow) {
	t.Helper()
	s, a, now, session, token := completedAuthFixture(t)
	locations, err := s.NewLocations(a)
	if err != nil {
		t.Fatal(err)
	}
	first := createLocation(t, locations, token, "SNAPSHOT-PAGE-A", nil)
	second := createLocation(t, locations, token, "SNAPSHOT-PAGE-B", nil)
	values := []Location{first, second}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	cursor := syncSnapshotCursor{Version: 1, Scope: syncCursorScope{
		AdminID: session.Admin.ID, ServerInstanceID: scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state"),
		DatasetEpoch: scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state"), ProjectionVersion: 1,
	}, SnapshotID: "page-snapshot", HighWatermark: scalar[int64](t, s.db, "SELECT seq FROM sqlite_sequence WHERE name='sync_changes'"),
		CreatedAt: stamp(*now), ExpiresAt: stamp(now.Add(syncSnapshotLifetime))}
	execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 VALUES(?,?,?,?,?,?,?,'ready')`, cursor.SnapshotID, cursor.Scope.ServerInstanceID, cursor.Scope.DatasetEpoch, cursor.Scope.ProjectionVersion,
		cursor.HighWatermark, cursor.CreatedAt, cursor.ExpiresAt)
	pinTestSnapshotSigner(t, s, a)
	rows := make([]snapshotPageFixtureRow, 0, len(values))
	for index, value := range values {
		payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='location' AND entity_id=?", value.ID)
		rows = append(rows, snapshotPageFixtureRow{id: value.ID, payload: payload, revision: value.Revision})
		execSQL(t, s.db, `INSERT INTO sync_snapshot_rows(snapshot_id,ordinal,entity_type,entity_id,entity_revision,payload_json)
 VALUES(?,?,'location',?,?,?)`, cursor.SnapshotID, index, value.ID, value.Revision, payload)
		proof, err := a.syncSnapshotRowProof(t.Context(), cursor, int64(len(values)), int64(index), "location", value.ID, value.Revision, []byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, s.db, "INSERT INTO sync_snapshot_row_proofs VALUES(?,?,?)", cursor.SnapshotID, index, proof[:])
	}
	manifest, err := a.syncSnapshotManifestProof(t.Context(), cursor, int64(len(values)))
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES(?,?,?)", cursor.SnapshotID, len(values), manifest[:])
	signed, err := encodeSyncSnapshotCursor(a, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return s, a, locations, now, token, cursor, signed, rows
}

func TestSyncSnapshotPagePreservesFrozenRowsAndStartsDeltaAtWatermark(t *testing.T) {
	s, a, locations, _, token, cursor, signed, rows := snapshotPageFixture(t)
	later := createLocation(t, locations, token, "SNAPSHOT-AFTER-H", nil)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	var records []syncRecord
	for index := 0; index < len(rows); index++ {
		page, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits)
		if err != nil || page.DatasetEpoch != cursor.Scope.DatasetEpoch || page.ServerInstanceID != cursor.Scope.ServerInstanceID ||
			page.ProjectionVersion != 1 || page.HighWatermark != cursor.HighWatermark || len(page.Records) != 1 ||
			page.Complete != (index == len(rows)-1) || (page.NextCursor == "") != page.Complete || (page.DeltaCursor != "") != page.Complete {
			t.Fatal("sealed page did not retain its scope, position or completion", page, err)
		}
		record := page.Records[0]
		if record.EntityType != "location" || record.EntityID != rows[index].id || record.EntityRevision != rows[index].revision ||
			record.Operation != "upsert" || record.ProjectionVersion != 1 || string(record.Payload) != rows[index].payload {
			t.Fatal("sealed page changed frozen payload or identity", record)
		}
		records = append(records, record)
		if !page.Complete {
			continuation, err := decodeSyncSnapshotCursor(a, page.NextCursor, cursor.Scope, a.now().UTC())
			if err != nil || continuation.NextOrdinal != int64(index+1) || continuation.HighWatermark != cursor.HighWatermark {
				t.Fatal("snapshot continuation advanced incorrectly", err)
			}
			signed = page.NextCursor
			continue
		}
		delta, err := a.readSyncDeltaPage(t.Context(), token, page.DeltaCursor, syncPageLimits{Records: 20, PayloadBytes: 4096, PageBytes: 8192})
		if err != nil || len(delta.Changes) == 0 || delta.HighWatermark <= cursor.HighWatermark {
			t.Fatal("snapshot completion did not begin delta at its frozen watermark", delta, err)
		}
		found := false
		for _, change := range delta.Changes {
			found = found || change.Record.EntityType == "location" && change.Record.EntityID == later.ID
		}
		if !found {
			t.Fatal("post-snapshot source change was absent from delta")
		}
	}
	if len(records) != len(rows) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_rows WHERE snapshot_id=?", cursor.SnapshotID) != len(rows) {
		t.Fatal("page reads changed the frozen snapshot")
	}
	status, err := a.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("private fixture paging incorrectly enabled public synchronization", err)
	}
}

func TestSyncSnapshotPageRejectsUnprovenAndDamagedContentWithoutPrefix(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *Store, syncSnapshotCursor)
		want   error
	}{
		{"missing signer", func(t *testing.T, s *Store, _ syncSnapshotCursor) {
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_signer_no_delete; DELETE FROM sync_snapshot_signer")
		}, errSyncSnapshotUnproven},
		{"bad manifest proof", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_seals_no_update; UPDATE sync_snapshot_seals SET proof=zeroblob(32) WHERE snapshot_id=?", c.SnapshotID)
		}, errSyncSnapshotUnproven},
		{"wrong sealed total", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_seals_no_update; UPDATE sync_snapshot_seals SET total_rows=1 WHERE snapshot_id=?", c.SnapshotID)
		}, errSyncSnapshotUnproven},
		{"missing lookahead proof", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_row_proofs_no_delete; DELETE FROM sync_snapshot_row_proofs WHERE snapshot_id=? AND ordinal=1", c.SnapshotID)
		}, errSyncSnapshotUnproven},
		{"bad lookahead proof", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_row_proofs_no_update; UPDATE sync_snapshot_row_proofs SET proof=zeroblob(32) WHERE snapshot_id=? AND ordinal=1", c.SnapshotID)
		}, errSyncSnapshotUnproven},
		{"oversized lookahead identity", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_rows_no_sealed_update")
			execSQL(t, s.db, "UPDATE sync_snapshot_rows SET entity_id=? WHERE snapshot_id=? AND ordinal=1", strings.Repeat("x", maxSyncIdentityBytes+1), c.SnapshotID)
		}, ErrDomainState},
		{"oversized lookahead kind", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_rows_no_sealed_update")
			execSQL(t, s.db, "UPDATE sync_snapshot_rows SET entity_type=? WHERE snapshot_id=? AND ordinal=1", strings.Repeat("x", 33), c.SnapshotID)
		}, ErrDomainState},
		{"changed frozen payload", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, `DROP TRIGGER sync_snapshot_rows_no_sealed_update;
 UPDATE sync_snapshot_rows SET payload_json=json_set(payload_json,'$.name','modified after seal') WHERE snapshot_id=? AND ordinal=1`, c.SnapshotID)
		}, errSyncSnapshotUnproven},
		{"unreviewed search lookahead", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, `DROP TRIGGER sync_snapshot_rows_no_sealed_update;
 UPDATE sync_snapshot_rows SET entity_type='search_doc',entity_id=?,payload_json=? WHERE snapshot_id=? AND ordinal=1`,
				projectionPair("item", "fixture"), `{"entity_type":"item","entity_id":"fixture","approved_text":"private-unreviewed-fixture"}`, c.SnapshotID)
		}, errSyncSnapshotUnproven},
		{"missing frozen tail", func(t *testing.T, s *Store, c syncSnapshotCursor) {
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_rows_no_sealed_delete; DELETE FROM sync_snapshot_rows WHERE snapshot_id=? AND ordinal=1", c.SnapshotID)
		}, errSyncSnapshotUnproven},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, a, _, _, token, cursor, signed, _ := snapshotPageFixture(t)
			test.damage(t, s, cursor)
			page, err := a.readSyncSnapshotPage(t.Context(), token, signed, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
			if !errors.Is(err, test.want) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
				t.Fatal("damaged snapshot returned a prefix", page, err)
			}
		})
	}
}

func TestSyncSnapshotPageEnforcesAuthenticationScopeAndBounds(t *testing.T) {
	s, a, _, now, token, cursor, signed, rows := snapshotPageFixture(t)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	if page, err := a.readSyncSnapshotPage(t.Context(), "not-a-token", signed, limits); !errors.Is(err, ErrAuthentication) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("unauthenticated snapshot read returned data", page, err)
	}
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed+"x", limits); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("tampered cursor returned data", page, err)
	}
	other := cursor
	other.Scope.AdminID = "other-admin"
	wrong, err := encodeSyncSnapshotCursor(a, other)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := a.readSyncSnapshotPage(t.Context(), token, wrong, limits); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("another principal read the snapshot", page, err)
	}
	for _, invalid := range []syncPageLimits{{}, {Records: 501, PayloadBytes: 1, PageBytes: 1}, {Records: 1, PayloadBytes: 2, PageBytes: 1}, {Records: 1, PayloadBytes: 1, PageBytes: maxSyncPageBytes + 1}} {
		if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, invalid); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
			t.Fatal("invalid page limits returned data", page, err)
		}
	}
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, syncPageLimits{Records: 1, PayloadBytes: 1, PageBytes: 8192}); !errors.Is(err, errSyncPayloadTooLarge) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("an oversized frozen payload returned a prefix", page, err)
	}
	if len(rows[0].payload) == 0 {
		t.Fatal("fixture requires a nonempty frozen payload")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if page, err := a.readSyncSnapshotPage(ctx, token, signed, limits); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("canceled snapshot read returned data", page, err)
	}
	if page, err := a.readSyncSnapshotPage(nil, token, signed, limits); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("nil context returned data", page, err)
	}
	for _, absent := range []*Auth{nil, {}, {store: &Store{}}} {
		if page, err := absent.readSyncSnapshotPage(t.Context(), token, signed, limits); !errors.Is(err, ErrDomainInput) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
			t.Fatal("an unavailable store returned data", page, err)
		}
	}
	*now = now.Add(syncSnapshotLifetime)
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits); !errors.Is(err, ErrSyncSnapshotExpired) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("expired snapshot returned data", page, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_seals WHERE snapshot_id=?", cursor.SnapshotID) != 1 {
		t.Fatal("a failed read changed the sealed fixture")
	}
}

func TestSyncSnapshotPageRejectsMissingSealAndOutOfRangeCursor(t *testing.T) {
	s, a, _, token, cursor, signed := snapshotMetadataFixture(t)
	pinTestSnapshotSigner(t, s, a)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits); !errors.Is(err, errSyncSnapshotUnproven) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("an unsealed snapshot returned data", page, err)
	}
	cursor.NextOrdinal = 1
	wrong, err := encodeSyncSnapshotCursor(a, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := a.readSyncSnapshotPage(t.Context(), token, wrong, limits); !errors.Is(err, ErrInvalidCursor) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("an out-of-range cursor returned data", page, err)
	}
}

func TestSyncSnapshotPageRejectsOversizedSignedLookahead(t *testing.T) {
	s, a, _, _, token, cursor, signed, rows := snapshotPageFixture(t)
	payload := fmt.Sprintf(`{"id":%q,"parent_id":null,"code":"BIG","name":%q,"status":"active","kind":"other","sort_order":0,"description":""}`, rows[1].id, strings.Repeat("x", 4096))
	proof, err := a.syncSnapshotRowProof(t.Context(), cursor, int64(len(rows)), 1, "location", rows[1].id, rows[1].revision, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "DROP TRIGGER sync_snapshot_rows_no_sealed_update; DROP TRIGGER sync_snapshot_row_proofs_no_update")
	execSQL(t, s.db, "UPDATE sync_snapshot_rows SET payload_json=? WHERE snapshot_id=? AND ordinal=1", payload, cursor.SnapshotID)
	execSQL(t, s.db, "UPDATE sync_snapshot_row_proofs SET proof=? WHERE snapshot_id=? AND ordinal=1", proof[:], cursor.SnapshotID)
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, syncPageLimits{Records: 1, PayloadBytes: 1024, PageBytes: 2048}); !errors.Is(err, errSyncPayloadTooLarge) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("an oversized lookahead returned a prefix", page, err)
	}
}

func TestSyncSnapshotPageRejectsARecordLargerThanItsPageBudget(t *testing.T) {
	s, a, _, _, token, cursor, signed, rows := snapshotPageFixture(t)
	firstLimit := len(rows[0].payload)
	limits := syncPageLimits{Records: 1, PayloadBytes: firstLimit, PageBytes: firstLimit}
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits); !errors.Is(err, errSyncPayloadTooLarge) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("an unrepresentable first row returned a page", page, err)
	}

	payload := fmt.Sprintf(`{"id":%q,"parent_id":null,"code":"BIG","name":%q,"status":"active","kind":"other","sort_order":0,"description":""}`, rows[1].id, strings.Repeat("x", 4096))
	proof, err := a.syncSnapshotRowProof(t.Context(), cursor, int64(len(rows)), 1, "location", rows[1].id, rows[1].revision, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "DROP TRIGGER sync_snapshot_rows_no_sealed_update; DROP TRIGGER sync_snapshot_row_proofs_no_update")
	execSQL(t, s.db, "UPDATE sync_snapshot_rows SET payload_json=? WHERE snapshot_id=? AND ordinal=1", payload, cursor.SnapshotID)
	execSQL(t, s.db, "UPDATE sync_snapshot_row_proofs SET proof=? WHERE snapshot_id=? AND ordinal=1", proof[:], cursor.SnapshotID)
	limits = syncPageLimits{Records: 1, PayloadBytes: len(payload), PageBytes: len(payload)}
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits); !errors.Is(err, errSyncPayloadTooLarge) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("an unrepresentable signed lookahead returned a prefix", page, err)
	}
}

func TestSyncSnapshotPageSupportsEmptySnapshotAndEndCursor(t *testing.T) {
	s, a, _, token, cursor, signed := snapshotMetadataFixture(t)
	pinTestSnapshotSigner(t, s, a)
	seal, err := a.syncSnapshotManifestProof(t.Context(), cursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES(?,0,?)", cursor.SnapshotID, seal[:])
	page, err := a.readSyncSnapshotPage(t.Context(), token, signed, syncPageLimits{Records: 1, PayloadBytes: 128, PageBytes: 128})
	if err != nil || !page.Complete || page.Records == nil || len(page.Records) != 0 || page.NextCursor != "" || page.DeltaCursor == "" {
		t.Fatal("empty sealed snapshot did not complete", page, err)
	}
	delta, err := decodeSyncDeltaCursor(a, page.DeltaCursor, cursor.Scope)
	if err != nil || delta.After != cursor.HighWatermark {
		t.Fatal("empty snapshot did not start delta at its watermark", err)
	}

	_, fullAuth, _, _, fullToken, fullCursor, _, fixtureRows := snapshotPageFixture(t)
	fullCursor.NextOrdinal = int64(len(fixtureRows))
	end, err := encodeSyncSnapshotCursor(fullAuth, fullCursor)
	if err != nil {
		t.Fatal(err)
	}
	page, err = fullAuth.readSyncSnapshotPage(t.Context(), fullToken, end, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192})
	if err != nil || !page.Complete || page.Records == nil || len(page.Records) != 0 || page.NextCursor != "" || page.DeltaCursor == "" {
		t.Fatal("end-of-snapshot cursor did not complete", page, err)
	}
}

func TestSyncSnapshotPageRespectsByteBudgetAndRetentionFloor(t *testing.T) {
	s, a, locations, _, token, cursor, signed, rows := snapshotPageFixture(t)
	payloadLimit := max(len(rows[0].payload), len(rows[1].payload))
	pageLimit := max(len(rows[0].payload)+len(rows[0].id), len(rows[1].payload)+len(rows[1].id)) + len("location") + len("upsert")
	limits := syncPageLimits{Records: 10, PayloadBytes: payloadLimit, PageBytes: pageLimit}
	first, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits)
	if err != nil || first.Complete || len(first.Records) != 1 || first.NextCursor == "" {
		t.Fatal("snapshot page did not stop at its byte budget", first, err)
	}
	last, err := a.readSyncSnapshotPage(t.Context(), token, first.NextCursor, limits)
	if err != nil || !last.Complete || len(last.Records) != 1 || last.Records[0].EntityID != rows[1].id {
		t.Fatal("byte-bounded snapshot did not finish without skipping rows", last, err)
	}
	createLocation(t, locations, token, "SNAPSHOT-ADVANCE-FLOOR", nil)
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq<=?", cursor.HighWatermark+1)
	execSQL(t, s.db, "UPDATE sync_state SET min_retained_seq=? WHERE singleton=1", cursor.HighWatermark+1)
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits); !errors.Is(err, ErrSyncResetRequired) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("snapshot below the retained floor returned data", page, err)
	}
}

func TestSyncSnapshotPageRejectsSignedRowsWithInvalidShapeOrOrder(t *testing.T) {
	for _, test := range []struct {
		name, id, payload string
		want              error
	}{
		{"invalid shape", "", `{}`, ErrDomainState},
		{"wrong order", "!", `{"id":"!","parent_id":null,"code":"EARLY","name":"Early","status":"active","kind":"other","sort_order":0,"description":""}`, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, a, _, _, token, cursor, signed, rows := snapshotPageFixture(t)
			id := test.id
			if id == "" {
				id = rows[1].id
			}
			proof, err := a.syncSnapshotRowProof(t.Context(), cursor, int64(len(rows)), 1, "location", id, rows[1].revision, []byte(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, "DROP TRIGGER sync_snapshot_rows_no_sealed_update; DROP TRIGGER sync_snapshot_row_proofs_no_update")
			execSQL(t, s.db, "UPDATE sync_snapshot_rows SET entity_id=?,payload_json=? WHERE snapshot_id=? AND ordinal=1", id, test.payload, cursor.SnapshotID)
			execSQL(t, s.db, "UPDATE sync_snapshot_row_proofs SET proof=? WHERE snapshot_id=? AND ordinal=1", proof[:], cursor.SnapshotID)
			if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}); !errors.Is(err, test.want) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
				t.Fatal("signed invalid lookahead returned a prefix", page, err)
			}
		})
	}
}

func TestSyncSnapshotPageRejectsUnavailableDatabaseAndInitialSignerDamage(t *testing.T) {
	s, a, _, _, token, _, signed, _ := snapshotPageFixture(t)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	page, err := a.readSyncSnapshotPage(ctx, token, signed, limits)
	cancel()
	if err = errors.Join(err, held.Rollback()); !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("a canceled database wait returned data", page, err)
	}
	execSQL(t, s.db, "DROP TRIGGER sync_snapshot_signer_no_update; UPDATE sync_snapshot_signer SET key_proof=zeroblob(32)")
	before := domainState(t, s)
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits); !errors.Is(err, ErrAuthState) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("a damaged initial signer returned data", page, err)
	}
	requireDomainState(t, s, before)
}

func TestSyncSnapshotPageRejectsSignerDamageDuringSessionActivity(t *testing.T) {
	s, a, _, _, token, _, signed, _ := snapshotPageFixture(t)
	marker := bytes.Clone(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"))
	execSQL(t, s.db, "DROP TRIGGER sync_snapshot_signer_no_update")
	execSQL(t, s.db, "CREATE TRIGGER damage_snapshot_page_signer AFTER UPDATE ON sessions BEGIN UPDATE sync_snapshot_signer SET key_proof=zeroblob(32); END")
	before := domainState(t, s)
	if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}); !errors.Is(err, ErrAuthState) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("signer damage during session activity returned data", page, err)
	}
	requireDomainState(t, s, before)
	if !bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_snapshot_signer"), marker) {
		t.Fatal("failed page read committed signer damage")
	}
}

func TestSyncSnapshotPageWithholdsResultOnExpiryOrCommitFailure(t *testing.T) {
	t.Run("expiry after read", func(t *testing.T) {
		s, a, _, now, token, _, signed, _ := snapshotPageFixture(t)
		base, calls := *now, 0
		a.now = func() time.Time {
			calls++
			if calls >= 5 {
				return base.Add(syncSnapshotLifetime)
			}
			return base
		}
		before := domainState(t, s)
		if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}); !errors.Is(err, ErrSyncSnapshotExpired) || !reflect.DeepEqual(page, syncSnapshotPage{}) || calls < 5 {
			t.Fatal("expired observation returned snapshot data", page, err)
		}
		requireDomainState(t, s, before)
	})
	t.Run("commit failure", func(t *testing.T) {
		s, a, _, _, token, _, signed, _ := snapshotPageFixture(t)
		execSQL(t, s.db, "CREATE TABLE snapshot_page_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
		execSQL(t, s.db, "CREATE TRIGGER snapshot_page_commit_failure AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO snapshot_page_commit_guard VALUES('missing'); END")
		before := domainState(t, s)
		if page, err := a.readSyncSnapshotPage(t.Context(), token, signed, syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}); err == nil || !reflect.DeepEqual(page, syncSnapshotPage{}) {
			t.Fatal("failed commit returned snapshot data", page, err)
		}
		requireDomainState(t, s, before)
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM snapshot_page_commit_guard") != 0 {
			t.Fatal("failed commit retained its deferred write")
		}
	})
}

func TestSyncSnapshotPageRejectsSignedOutOfOrderBoundary(t *testing.T) {
	s, a, _, _, token, cursor, signed, rows := snapshotPageFixture(t)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	first, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits)
	if err != nil || first.NextCursor == "" {
		t.Fatal("first sealed page was not available", err)
	}
	payload := `{"id":"!","parent_id":null,"code":"EARLY","name":"Early","status":"active","kind":"other","sort_order":0,"description":""}`
	proof, err := a.syncSnapshotRowProof(t.Context(), cursor, int64(len(rows)), 1, "location", "!", rows[1].revision, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "DROP TRIGGER sync_snapshot_rows_no_sealed_update; DROP TRIGGER sync_snapshot_row_proofs_no_update")
	execSQL(t, s.db, "UPDATE sync_snapshot_rows SET entity_id='!',payload_json=? WHERE snapshot_id=? AND ordinal=1", payload, cursor.SnapshotID)
	execSQL(t, s.db, "UPDATE sync_snapshot_row_proofs SET proof=? WHERE snapshot_id=? AND ordinal=1", proof[:], cursor.SnapshotID)
	if page, err := a.readSyncSnapshotPage(t.Context(), token, first.NextCursor, limits); !errors.Is(err, ErrDomainState) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("a signed out-of-order boundary returned a later page", page, err)
	}
}

func TestSyncSnapshotPageRejectsChangedAnchorAcrossPages(t *testing.T) {
	s, a, _, _, token, cursor, signed, _ := snapshotPageFixture(t)
	limits := syncPageLimits{Records: 1, PayloadBytes: 4096, PageBytes: 8192}
	first, err := a.readSyncSnapshotPage(t.Context(), token, signed, limits)
	if err != nil || first.NextCursor == "" {
		t.Fatal("first sealed page was not available", err)
	}
	execSQL(t, s.db, `DROP TRIGGER sync_snapshot_rows_no_sealed_update;
 UPDATE sync_snapshot_rows SET payload_json=json_set(payload_json,'$.name','changed anchor') WHERE snapshot_id=? AND ordinal=0`, cursor.SnapshotID)
	if page, err := a.readSyncSnapshotPage(t.Context(), token, first.NextCursor, limits); !errors.Is(err, errSyncSnapshotUnproven) || !reflect.DeepEqual(page, syncSnapshotPage{}) {
		t.Fatal("a changed boundary row returned a later page", page, err)
	}
	if bytes.Equal([]byte(first.Records[0].Payload), []byte(scalar[string](t, s.db, "SELECT payload_json FROM sync_snapshot_rows WHERE snapshot_id=? AND ordinal=0", cursor.SnapshotID))) {
		t.Fatal("fixture did not actually change the frozen row")
	}
}
