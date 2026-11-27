package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func snapshotProofFixture() (*Auth, syncSnapshotCursor) {
	secret := make([]byte, 32)
	for index := range secret {
		secret[index] = byte(index)
	}
	created := time.Date(2026, 11, 18, 12, 34, 56, 123456789, time.UTC)
	return &Auth{serverSecret: secret}, syncSnapshotCursor{
		Version: 1, Scope: syncCursorScope{AdminID: "admin-家\x00", ServerInstanceID: testSyncProofInstance, DatasetEpoch: "epoch-😀", ProjectionVersion: 1},
		SnapshotID: "snapshot-家", HighWatermark: 120, CreatedAt: stamp(created), ExpiresAt: stamp(created.Add(syncSnapshotLifetime)),
	}
}

func TestSyncSnapshotProofMatchesIndependentFramedVectors(t *testing.T) {
	auth, cursor := snapshotProofFixture()
	payload := []byte(`{"id":"location-1","name":"Box"}`)
	original := bytes.Clone(payload)
	manifest, err := auth.syncSnapshotManifestProof(t.Context(), cursor, 2)
	if err != nil || hex.EncodeToString(manifest[:]) != "8bf542b5c539f4eb9480c8544798e91fffc6f5058c48fe1d7472eacf6a341c7c" {
		t.Fatal("snapshot manifest did not match the independently framed vector", err)
	}
	row, err := auth.syncSnapshotRowProof(t.Context(), cursor, 2, 1, "location", "location-1", 7, payload)
	if err != nil || hex.EncodeToString(row[:]) != "8ef5435ed61ee8a4b722ab0d86630677ff07b28e5af6c0847e14be85178e63e7" || !bytes.Equal(payload, original) || row == manifest {
		t.Fatal("snapshot row did not match the independent domain-separated vector", err)
	}
	cursor.NextOrdinal = 1
	manifestAgain, err := auth.syncSnapshotManifestProof(t.Context(), cursor, 2)
	if err != nil || manifestAgain != manifest {
		t.Fatal("pagination position changed the manifest proof", err)
	}
	rowAgain, err := auth.syncSnapshotRowProof(t.Context(), cursor, 2, 1, "location", "location-1", 7, payload)
	if err != nil || rowAgain != row {
		t.Fatal("pagination position changed the frozen row proof", err)
	}
}

func TestSyncSnapshotProofBindsEveryStableScopeAndExactRow(t *testing.T) {
	auth, cursor := snapshotProofFixture()
	payload := []byte(`{"id":"location-1","name":"Box"}`)
	manifest, err := auth.syncSnapshotManifestProof(t.Context(), cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	row, err := auth.syncSnapshotRowProof(t.Context(), cursor, 2, 1, "location", "location-1", 7, payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*syncSnapshotCursor)
	}{
		{"principal", func(c *syncSnapshotCursor) { c.Scope.AdminID = "other-admin" }},
		{"instance", func(c *syncSnapshotCursor) { c.Scope.ServerInstanceID = "fedcba9876543210fedcba9876543210" }},
		{"epoch", func(c *syncSnapshotCursor) { c.Scope.DatasetEpoch = "other-epoch" }},
		{"snapshot", func(c *syncSnapshotCursor) { c.SnapshotID = "other-snapshot" }},
		{"watermark", func(c *syncSnapshotCursor) { c.HighWatermark++ }},
		{"interval", func(c *syncSnapshotCursor) {
			created, _ := time.Parse(authTimestamp, c.CreatedAt)
			c.CreatedAt, c.ExpiresAt = stamp(created.Add(time.Second)), stamp(created.Add(time.Second+syncSnapshotLifetime))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := cursor
			test.change(&changed)
			head, headErr := auth.syncSnapshotManifestProof(t.Context(), changed, 2)
			value, rowErr := auth.syncSnapshotRowProof(t.Context(), changed, 2, 1, "location", "location-1", 7, payload)
			if headErr != nil || rowErr != nil || head == manifest || value == row {
				t.Fatal("changed stable scope reused a proof", headErr, rowErr)
			}
		})
	}
	for _, test := range []struct {
		name, kind, id           string
		total, ordinal, revision int64
		payload                  []byte
	}{
		{"count", "location", "location-1", 3, 1, 7, payload},
		{"ordinal", "location", "location-1", 2, 0, 7, payload},
		{"kind", "item", "location-1", 2, 1, 7, payload},
		{"identity", "location", "location-2", 2, 1, 7, payload},
		{"revision", "location", "location-1", 2, 1, 8, payload},
		{"exact bytes", "location", "location-1", 2, 1, 7, []byte(`{ "id":"location-1","name":"Box"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := auth.syncSnapshotRowProof(t.Context(), cursor, test.total, test.ordinal, test.kind, test.id, test.revision, test.payload)
			if err != nil || value == row {
				t.Fatal("changed exact row reused a proof", err)
			}
		})
	}
	if changed, err := auth.syncSnapshotManifestProof(t.Context(), cursor, 3); err != nil || changed == manifest {
		t.Fatal("different row count reused the manifest proof", err)
	}
	other := &Auth{serverSecret: bytes.Repeat([]byte{2}, 32)}
	if changed, err := other.syncSnapshotManifestProof(t.Context(), cursor, 2); err != nil || changed == manifest {
		t.Fatal("different private key reused the manifest proof", err)
	}
	if bytes.Equal(row[:], auth.authMAC("", syncSnapshotCursorPurpose, payload)) {
		t.Fatal("snapshot row proof shared the cursor MAC domain")
	}
}

func TestSyncSnapshotProofBoundsInputsAndHonorsCancellation(t *testing.T) {
	auth, cursor := snapshotProofFixture()
	for _, change := range []func(*syncSnapshotCursor){
		func(c *syncSnapshotCursor) { c.Version = 2 },
		func(c *syncSnapshotCursor) { c.Scope.AdminID = "" },
		func(c *syncSnapshotCursor) { c.Scope.ServerInstanceID = "not-an-instance" },
		func(c *syncSnapshotCursor) { c.Scope.DatasetEpoch = string([]byte{0xff}) },
		func(c *syncSnapshotCursor) { c.Scope.ProjectionVersion = 2 },
		func(c *syncSnapshotCursor) { c.SnapshotID = "" },
		func(c *syncSnapshotCursor) { c.HighWatermark = maxSafeInteger + 1 },
		func(c *syncSnapshotCursor) { c.ExpiresAt = c.CreatedAt },
		func(c *syncSnapshotCursor) { c.NextOrdinal = -1 },
		func(c *syncSnapshotCursor) { c.NextOrdinal = 3 },
	} {
		changed := cursor
		change(&changed)
		if value, err := auth.syncSnapshotManifestProof(t.Context(), changed, 2); !errors.Is(err, ErrDomainInput) || value != [32]byte{} {
			t.Fatal("invalid snapshot proof scope was accepted", err)
		}
	}
	for _, total := range []int64{-1, maxSafeInteger + 1} {
		if value, err := auth.syncSnapshotManifestProof(t.Context(), cursor, total); !errors.Is(err, ErrDomainInput) || value != [32]byte{} {
			t.Fatal("invalid snapshot size was accepted", err)
		}
	}
	if value, err := auth.syncSnapshotManifestProof(t.Context(), cursor, 0); err != nil || value == [32]byte{} {
		t.Fatal("empty snapshot could not be sealed", err)
	}
	for _, test := range []struct {
		kind, id                 string
		total, ordinal, revision int64
		payload                  []byte
	}{
		{"location", "id", 0, 0, 1, []byte("{}")}, {"location", "id", 2, -1, 1, []byte("{}")},
		{"location", "id", 2, 2, 1, []byte("{}")}, {"location", "id", 2, 0, 0, []byte("{}")},
		{"location", "id", 2, 0, maxSafeInteger + 1, []byte("{}")}, {"unknown", "id", 2, 0, 1, []byte("{}")},
		{"stock", `["item", "location"]`, 2, 0, 1, []byte("{}")}, {"location", "id", 2, 0, 1, nil},
		{"location", "id", 2, 0, 1, make([]byte, maxSyncPageBytes+1)},
	} {
		if value, err := auth.syncSnapshotRowProof(t.Context(), cursor, test.total, test.ordinal, test.kind, test.id, test.revision, test.payload); !errors.Is(err, ErrDomainInput) || value != [32]byte{} {
			t.Fatal("invalid snapshot row was accepted", err)
		}
	}
	maximum := cursor
	maximum.HighWatermark, maximum.NextOrdinal = maxSafeInteger, maxSafeInteger
	if value, err := auth.syncSnapshotRowProof(t.Context(), maximum, maxSafeInteger, maxSafeInteger-1, "search_doc", projectionPair("item", strings.Repeat("\x00", 128)), maxSafeInteger, []byte("not-json")); err != nil || value == [32]byte{} {
		t.Fatal("valid maximum inputs or opaque source bytes were rejected", err)
	}
	if value, err := auth.syncSnapshotRowProof(t.Context(), cursor, 1, 0, "location", "id", 1, make([]byte, maxSyncPageBytes)); err != nil || value == [32]byte{} {
		t.Fatal("maximum payload was rejected", err)
	}
	var absent *Auth
	if value, err := absent.syncSnapshotManifestProof(t.Context(), cursor, 2); !errors.Is(err, ErrAuthState) || value != [32]byte{} {
		t.Fatal("missing signer produced a manifest proof", err)
	}
	if value, err := auth.syncSnapshotRowProof(nil, cursor, 2, 0, "location", "id", 1, []byte("{}")); !errors.Is(err, ErrDomainInput) || value != [32]byte{} {
		t.Fatal("nil context produced a row proof", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	counted := &cancelOnSearchProofCheck{Context: ctx, cancel: cancel}
	defer cancel()
	if value, err := auth.syncSnapshotRowProof(counted, cursor, 2, 0, "location", "id", 1, bytes.Repeat([]byte("x"), 3*(64<<10))); !errors.Is(err, context.Canceled) || value != [32]byte{} || counted.checks < 3 {
		t.Fatal("mid-payload cancellation returned a proof", err)
	}
}

func TestSyncSnapshotProofFramesAmbiguousFieldsAndRejectsCanceledCalls(t *testing.T) {
	auth, cursor := snapshotProofFixture()
	first := cursor
	first.Scope.DatasetEpoch, first.SnapshotID = "epoch", "snapshot-1"
	second := first
	second.Scope.DatasetEpoch, second.SnapshotID = "epochsnap", "shot-1"
	left, err := auth.syncSnapshotManifestProof(t.Context(), first, 2)
	if err != nil {
		t.Fatal(err)
	}
	right, err := auth.syncSnapshotManifestProof(t.Context(), second, 2)
	if err != nil || left == right {
		t.Fatal("ambiguous concatenated scope fields reused a proof", err)
	}
	left, err = auth.syncSnapshotRowProof(t.Context(), cursor, 2, 0, "location", "_event-x", 1, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	right, err = auth.syncSnapshotRowProof(t.Context(), cursor, 2, 0, "location_event", "-x", 1, []byte("{}"))
	if err != nil || left == right {
		t.Fatal("ambiguous concatenated row fields reused a proof", err)
	}
	otherKey := &Auth{serverSecret: bytes.Repeat([]byte{2}, 32)}
	right, err = otherKey.syncSnapshotRowProof(t.Context(), cursor, 2, 0, "location", "_event-x", 1, []byte("{}"))
	if err != nil || left == right {
		t.Fatal("different private keys reused a row proof", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := auth.syncSnapshotManifestProof(ctx, cursor, 2); !errors.Is(err, context.Canceled) || value != [32]byte{} {
		t.Fatal("canceled context produced a manifest proof", err)
	}
	if value, err := auth.syncSnapshotRowProof(ctx, cursor, 2, 0, "location", "id", 1, []byte("{}")); !errors.Is(err, context.Canceled) || value != [32]byte{} {
		t.Fatal("canceled context produced a row proof", err)
	}
	lateCtx, lateCancel := context.WithCancel(t.Context())
	defer lateCancel()
	late := &cancelOnSearchProofCheck{Context: lateCtx, cancel: lateCancel, checks: 1}
	if value, err := auth.syncSnapshotManifestProof(late, cursor, 2); !errors.Is(err, context.Canceled) || value != [32]byte{} {
		t.Fatal("late cancellation produced a manifest proof", err)
	}
	finalCtx, finalCancel := context.WithCancel(t.Context())
	defer finalCancel()
	final := &cancelOnSearchProofCheck{Context: finalCtx, cancel: finalCancel}
	if value, err := auth.syncSnapshotRowProof(final, cursor, 2, 0, "location", "id", 1, []byte("{}")); !errors.Is(err, context.Canceled) || value != [32]byte{} {
		t.Fatal("final cancellation produced a row proof", err)
	}
	shortKey := &Auth{serverSecret: []byte("short")}
	if value, err := shortKey.syncSnapshotRowProof(t.Context(), cursor, 2, 0, "location", "id", 1, []byte("{}")); !errors.Is(err, ErrAuthState) || value != [32]byte{} {
		t.Fatal("invalid private key produced a row proof", err)
	}
}

func FuzzSyncSnapshotRowProof(f *testing.F) {
	f.Add("location", "location-1", int64(2), int64(1), int64(7), []byte(`{"id":"location-1"}`))
	f.Add("search_doc", projectionPair("item", "source\x00"), int64(1), int64(0), int64(1), []byte("not-json"))
	f.Add("unknown", "", int64(0), int64(-1), int64(0), []byte{})
	f.Fuzz(func(t *testing.T, kind, id string, total, ordinal, revision int64, payload []byte) {
		if len(kind) > 128 || len(id) > 4096 || len(payload) > 64<<10 {
			t.Skip()
		}
		auth, cursor := snapshotProofFixture()
		original := bytes.Clone(payload)
		first, firstErr := auth.syncSnapshotRowProof(t.Context(), cursor, total, ordinal, kind, id, revision, payload)
		second, secondErr := auth.syncSnapshotRowProof(t.Context(), cursor, total, ordinal, kind, id, revision, payload)
		if !bytes.Equal(original, payload) || first != second || (firstErr == nil) != (secondErr == nil) {
			t.Fatal("identical frozen input changed or produced inconsistent proofs")
		}
		if firstErr != nil {
			if !errors.Is(firstErr, ErrDomainInput) || first != [32]byte{} {
				t.Fatal("invalid input produced a proof", firstErr)
			}
			return
		}
		other, err := auth.syncSnapshotRowProof(t.Context(), cursor, total, ordinal, kind, id, revision, append(bytes.Clone(payload), 0))
		if err != nil || first == other {
			t.Fatal("different exact payload bytes produced the same proof", err)
		}
	})
}
