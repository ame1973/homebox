package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func syncCursorFixture() (*Auth, syncCursorScope, syncSnapshotCursor, syncDeltaCursor, time.Time) {
	a := &Auth{serverSecret: []byte(strings.Repeat("k", 32))}
	scope := syncCursorScope{AdminID: "admin-家\x00", ServerInstanceID: "0123456789abcdef0123456789abcdef", DatasetEpoch: "epoch-😀\x00", ProjectionVersion: 1}
	now := time.Date(2026, 11, 18, 12, 34, 56, 123456789, time.UTC)
	snapshot := syncSnapshotCursor{Version: 1, Scope: scope, SnapshotID: "snapshot-家", NextOrdinal: 23, HighWatermark: 120,
		CreatedAt: stamp(now), ExpiresAt: stamp(now.Add(syncSnapshotLifetime))}
	delta := syncDeltaCursor{Version: 1, Scope: scope, After: 120}
	return a, scope, snapshot, delta, now
}

func signedSyncTestPayload(a *Auth, purpose, payload string) string {
	body := []byte(payload)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(a.authMAC("", purpose, body))
}

func TestSyncCursorRoundTripKeepsIndependentSnapshotAndDeltaScopes(t *testing.T) {
	a, scope, snapshot, delta, now := syncCursorFixture()
	bootstrap, err := encodeSyncSnapshotCursor(a, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := encodeSyncDeltaCursor(a, delta)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := decodeSyncSnapshotCursor(a, bootstrap, scope, now); err != nil || !reflect.DeepEqual(decoded, snapshot) {
		t.Fatal("snapshot cursor did not retain its frozen scope", decoded, err)
	}
	if decoded, err := decodeSyncDeltaCursor(a, changes, scope); err != nil || !reflect.DeepEqual(decoded, delta) {
		t.Fatal("delta cursor did not retain its sequence", decoded, err)
	}
	emptySnapshot := snapshot
	emptySnapshot.NextOrdinal, emptySnapshot.HighWatermark = 0, 0
	emptyDelta := delta
	emptyDelta.After = 0
	initialSnapshot, err := encodeSyncSnapshotCursor(a, emptySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	initialDelta, err := encodeSyncDeltaCursor(a, emptyDelta)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeSyncSnapshotCursor(a, initialSnapshot, scope, now); err != nil || got != emptySnapshot {
		t.Fatal("zero snapshot coordinates changed", got, err)
	}
	if got, err := decodeSyncDeltaCursor(a, initialDelta, scope); err != nil || got != emptyDelta {
		t.Fatal("zero delta sequence changed", got, err)
	}
	if _, err := decodeSyncDeltaCursor(a, bootstrap, scope); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("snapshot cursor entered the delta namespace", err)
	}
	if _, err := decodeSyncSnapshotCursor(a, changes, scope, now); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("delta cursor entered the snapshot namespace", err)
	}
	if _, err := decodeSyncSnapshotCursor(a, bootstrap, scope, now.Add(syncSnapshotLifetime-time.Nanosecond)); err != nil {
		t.Fatal("snapshot expired before its exact deadline", err)
	}
	if _, err := decodeSyncSnapshotCursor(a, bootstrap, scope, now.Add(syncSnapshotLifetime)); !errors.Is(err, ErrSyncSnapshotExpired) {
		t.Fatal("snapshot was accepted at its exact expiry", err)
	}
	if _, err := decodeSyncSnapshotCursor(a, bootstrap, scope, now.Add(-time.Nanosecond)); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("snapshot was accepted before issuance", err)
	}
	// A delta cursor has no wall-clock expiry: the later database read must
	// compare its position against the actual retention floor and watermark.
	if decoded, err := decodeSyncDeltaCursor(a, changes, scope); err != nil || decoded.After != 120 {
		t.Fatal("a retained delta position was treated as a snapshot deadline", err)
	}
}

func TestSyncCursorsRejectChangedPrincipalAndRequireResetForChangedDataset(t *testing.T) {
	a, scope, snapshot, delta, now := syncCursorFixture()
	bootstrap, err := encodeSyncSnapshotCursor(a, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := encodeSyncDeltaCursor(a, delta)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*syncCursorScope)
		want   error
	}{
		{"administrator", func(v *syncCursorScope) { v.AdminID = "other-admin" }, ErrInvalidCursor},
		{"instance", func(v *syncCursorScope) { v.ServerInstanceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }, ErrSyncResetRequired},
		{"epoch", func(v *syncCursorScope) { v.DatasetEpoch = "restored" }, ErrSyncResetRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			other := scope
			test.change(&other)
			if _, err := decodeSyncSnapshotCursor(a, bootstrap, other, now); !errors.Is(err, test.want) {
				t.Fatal("snapshot accepted a changed scope", err)
			}
			if _, err := decodeSyncDeltaCursor(a, changes, other); !errors.Is(err, test.want) {
				t.Fatal("delta accepted a changed scope", err)
			}
		})
	}
	// A different projection version is a reset even when the payload was
	// authenticated by the same server, not an invitation to reinterpret data.
	snapshot.Scope.ProjectionVersion = 2
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	delta.Scope.ProjectionVersion = 2
	deltaJSON, err := json.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSyncSnapshotCursor(a, signedSyncTestPayload(a, syncSnapshotCursorPurpose, string(snapshotJSON)), scope, now); !errors.Is(err, ErrSyncResetRequired) {
		t.Fatal("snapshot accepted an incompatible projection", err)
	}
	if _, err := decodeSyncDeltaCursor(a, signedSyncTestPayload(a, syncDeltaCursorPurpose, string(deltaJSON)), scope); !errors.Is(err, ErrSyncResetRequired) {
		t.Fatal("delta accepted an incompatible projection", err)
	}
	wrongKey := &Auth{serverSecret: []byte(strings.Repeat("x", 32))}
	if _, err := decodeSyncDeltaCursor(wrongKey, changes, scope); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("another key accepted the cursor", err)
	}
}

func TestSyncCursorRejectsTamperingAndNoncanonicalAuthenticatedBodies(t *testing.T) {
	a, scope, snapshot, delta, now := syncCursorFixture()
	bootstrap, err := encodeSyncSnapshotCursor(a, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(bootstrap, ".")
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		"", ".", "abc", "." + parts[1], parts[0] + ".", bootstrap + ".extra",
		parts[0] + "=." + parts[1], parts[0] + "." + parts[1] + "=", "!invalid." + parts[1],
		parts[0] + "." + strings.Repeat("!", 43), parts[0] + "." + strings.Repeat("A", 43),
		strings.Repeat("x", maxSyncCursorBytes+1),
		signedSyncTestPayload(a, syncSnapshotCursorPurpose, string(body)+"{}"),
		signedSyncTestPayload(a, syncSnapshotCursorPurpose, " "+string(body)),
		signedSyncTestPayload(a, syncSnapshotCursorPurpose, strings.Replace(string(body), `"v":1`, `"v":1,"v":1`, 1)),
		signedSyncTestPayload(a, syncSnapshotCursorPurpose, strings.Replace(string(body), `"v":1`, `"V":1`, 1)),
		signedSyncTestPayload(a, syncSnapshotCursorPurpose, strings.Replace(string(body), `"v":1`, `"v":1,"private":"x"`, 1)),
		signedSyncTestPayload(a, syncSnapshotCursorPurpose, `{"v":1}`),
	} {
		if _, err := decodeSyncSnapshotCursor(a, token, scope, now); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("malformed snapshot cursor was accepted", err)
		}
	}
	changes, err := encodeSyncDeltaCursor(a, delta)
	if err != nil {
		t.Fatal(err)
	}
	deltaBody, err := json.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		strings.Replace(string(deltaBody), `"after":120`, `"after":null`, 1),
		strings.Replace(string(deltaBody), `"after":120`, `"after":-1`, 1),
		strings.Replace(string(deltaBody), `"after":120`, `"after":9007199254740992`, 1),
		strings.Replace(string(deltaBody), `"after":120`, `"after":120,"after":120`, 1),
		strings.Replace(string(deltaBody), `"after":120`, `"after":120,"credential":"private"`, 1),
	} {
		value, err := decodeSyncDeltaCursor(a, signedSyncTestPayload(a, syncDeltaCursorPurpose, body), scope)
		if !errors.Is(err, ErrInvalidCursor) || value != (syncDeltaCursor{}) {
			t.Fatal("malformed signed delta exposed a position", value, err)
		}
	}
	for _, purpose := range []string{"locations-tree-cursor-v1", "csrf-token-v1", "auth-request-v2", syncSnapshotCursorPurpose} {
		body, err := json.Marshal(delta)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = decodeSyncDeltaCursor(a, signedSyncTestPayload(a, purpose, string(body)), scope); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("a different MAC purpose entered the delta namespace", purpose, err)
		}
	}
	if _, err := decodeSyncDeltaCursor(&Auth{}, changes, scope); !errors.Is(err, ErrDomainState) {
		t.Fatal("a missing verification key did not fail closed", err)
	}
	if _, err := encodeSyncSnapshotCursor(nil, snapshot); !errors.Is(err, ErrDomainState) {
		t.Fatal("a missing signing key did not fail closed", err)
	}
}

func TestSyncCursorBoundsAndExactMaximumIdentifiers(t *testing.T) {
	a, scope, snapshot, delta, now := syncCursorFixture()
	scope.AdminID, scope.DatasetEpoch = strings.Repeat("\x00", 128), strings.Repeat("\x00", 128)
	snapshot.Scope, snapshot.SnapshotID = scope, strings.Repeat("\x00", 128)
	snapshot.NextOrdinal, snapshot.HighWatermark = maxSafeInteger, maxSafeInteger
	delta.Scope, delta.After = scope, maxSafeInteger
	bootstrap, err := encodeSyncSnapshotCursor(a, snapshot)
	if err != nil || len(bootstrap) > maxSyncCursorBytes {
		t.Fatal("bounded snapshot token cannot represent valid IDs", len(bootstrap), err)
	}
	if decoded, err := decodeSyncSnapshotCursor(a, bootstrap, scope, now); err != nil || decoded != snapshot {
		t.Fatal("snapshot changed exact maximum identities", err)
	}
	changes, err := encodeSyncDeltaCursor(a, delta)
	if err != nil || len(changes) > maxSyncCursorBytes {
		t.Fatal("bounded delta token cannot represent valid IDs", len(changes), err)
	}
	if decoded, err := decodeSyncDeltaCursor(a, changes, scope); err != nil || decoded != delta {
		t.Fatal("delta changed exact maximum identities", err)
	}
	for _, mutate := range []func(*syncSnapshotCursor){
		func(v *syncSnapshotCursor) { v.Version = 0 },
		func(v *syncSnapshotCursor) { v.Scope.AdminID = "" },
		func(v *syncSnapshotCursor) { v.Scope.DatasetEpoch = "\xff" },
		func(v *syncSnapshotCursor) { v.Scope.ServerInstanceID = "ABC" },
		func(v *syncSnapshotCursor) { v.Scope.ProjectionVersion = 2 },
		func(v *syncSnapshotCursor) { v.SnapshotID = strings.Repeat("x", 129) },
		func(v *syncSnapshotCursor) { v.NextOrdinal = -1 },
		func(v *syncSnapshotCursor) { v.HighWatermark = maxSafeInteger + 1 },
		func(v *syncSnapshotCursor) { v.CreatedAt = "2026-11-18T12:34:56Z" },
		func(v *syncSnapshotCursor) { v.ExpiresAt = stamp(now.Add(syncSnapshotLifetime + time.Nanosecond)) },
	} {
		invalid := snapshot
		mutate(&invalid)
		if _, err := encodeSyncSnapshotCursor(a, invalid); !errors.Is(err, ErrDomainState) {
			t.Fatal("invalid snapshot metadata was signed", err)
		}
	}
	for _, after := range []int64{-1, maxSafeInteger + 1} {
		invalid := delta
		invalid.After = after
		if _, err := encodeSyncDeltaCursor(a, invalid); !errors.Is(err, ErrDomainState) {
			t.Fatal("invalid delta position was signed", err)
		}
	}
	badScope := scope
	badScope.DatasetEpoch = ""
	if _, err := decodeSyncDeltaCursor(a, changes, badScope); !errors.Is(err, ErrDomainState) {
		t.Fatal("invalid authoritative context was blamed on the cursor", err)
	}
}

func TestSyncDeltaPositionUsesInclusiveFloorAndDurableWatermark(t *testing.T) {
	for _, test := range []struct {
		name                    string
		after, floor, watermark int64
		want                    error
	}{
		{"empty", 0, 0, 0, nil},
		{"exact floor", 7, 7, 12, nil},
		{"within retained history", 8, 7, 12, nil},
		{"exact allocated watermark", 12, 7, 12, nil},
		{"fully pruned", 12, 12, 12, nil},
		{"below floor", 6, 7, 12, ErrSyncResetRequired},
		{"future position", 13, 7, 12, ErrSyncResetRequired},
		{"negative position", -1, 0, 0, ErrInvalidCursor},
		{"unsafe position", maxSafeInteger + 1, 0, 0, ErrInvalidCursor},
		{"negative floor", 0, -1, 0, ErrDomainState},
		{"floor above watermark", 0, 2, 1, ErrDomainState},
		{"unsafe watermark", 0, 0, maxSafeInteger + 1, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := checkSyncDeltaPosition(test.after, test.floor, test.watermark); !errors.Is(err, test.want) {
				t.Fatalf("position decision: got %v, want %v", err, test.want)
			}
		})
	}
}

func TestSyncDeltaPositionUsesRealPrunedWatermark(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	createLocation(t, locations, token, "CURSOR", nil)
	execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=10 WHERE name='sync_changes'")
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.HighWatermark != 10 || checkSyncDeltaPosition(10, status.MinRetainedSeq, status.HighWatermark) != nil {
		t.Fatal("allocated sequence gap could not be used as a position", status, err)
	}
	execSQL(t, s.db, "DELETE FROM sync_changes; UPDATE sync_state SET min_retained_seq=10")
	status, err = auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.HighWatermark != 10 || status.MinRetainedSeq != 10 || status.LastChangeAt != nil {
		t.Fatal("fully pruned log lost its real retention bounds", status, err)
	}
	if err := checkSyncDeltaPosition(10, status.MinRetainedSeq, status.HighWatermark); err != nil {
		t.Fatal("position at the last pruned sequence was rejected", err)
	}
	if err := checkSyncDeltaPosition(9, status.MinRetainedSeq, status.HighWatermark); !errors.Is(err, ErrSyncResetRequired) {
		t.Fatal("position below the real floor did not require reset", err)
	}
}

func TestSyncCursorDoesNotAuthenticateAndCanSurviveFreshSession(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	session, err := auth.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	scope := syncCursorScope{AdminID: session.Admin.ID, ServerInstanceID: scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state"),
		DatasetEpoch: scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state"), ProjectionVersion: 1}
	cursor, err := encodeSyncDeltaCursor(auth, syncDeltaCursor{Version: 1, Scope: scope, After: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(t.Context(), cursor); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a sync continuation authenticated as a session", err)
	}
	if _, err := auth.Logout(t.Context(), token, "sync-cursor-logout"); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, auth)
	next, err := auth.Authenticate(t.Context(), fresh)
	if err != nil || next.Admin.ID != scope.AdminID {
		t.Fatal("fresh session fixture failed", err)
	}
	if _, err := decodeSyncDeltaCursor(auth, cursor, scope); err != nil {
		t.Fatal("fresh same-principal session lost its position", err)
	}
}

func FuzzSyncCursorDecoders(f *testing.F) {
	a, scope, snapshot, delta, now := syncCursorFixture()
	bootstrap, err := encodeSyncSnapshotCursor(a, snapshot)
	if err != nil {
		f.Fatal(err)
	}
	changes, err := encodeSyncDeltaCursor(a, delta)
	if err != nil {
		f.Fatal(err)
	}
	snapshotBody, err := json.Marshal(snapshot)
	if err != nil {
		f.Fatal(err)
	}
	deltaBody, err := json.Marshal(delta)
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{bootstrap, changes, string(snapshotBody), string(deltaBody), "", ".", strings.Repeat("!", 100)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		if len(token) > maxSyncCursorBytes+1 {
			return
		}
		// Sign mutated bodies too, so fuzzing reaches the canonical JSON and
		// metadata validators rather than stopping at the MAC on every mutation.
		for _, candidate := range []string{token, signedSyncTestPayload(a, syncSnapshotCursorPurpose, token)} {
			if value, err := decodeSyncSnapshotCursor(a, candidate, scope, now); err == nil {
				again, err := encodeSyncSnapshotCursor(a, value)
				if err != nil || again != candidate {
					t.Fatal("accepted a noncanonical snapshot cursor", err)
				}
			}
		}
		for _, candidate := range []string{token, signedSyncTestPayload(a, syncDeltaCursorPurpose, token)} {
			if value, err := decodeSyncDeltaCursor(a, candidate, scope); err == nil {
				again, err := encodeSyncDeltaCursor(a, value)
				if err != nil || again != candidate {
					t.Fatal("accepted a noncanonical delta cursor", err)
				}
			}
		}
	})
}
