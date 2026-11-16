package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"homebox/internal/mediaimage"
)

func privateUploadPNG(t *testing.T, marker string) []byte {
	t.Helper()
	imageData := image.NewNRGBA(image.Rect(0, 0, 5, 3))
	for y := range 3 {
		for x := range 5 {
			imageData.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 30), G: uint8(y * 70), B: 40, A: uint8(100 + x*20)})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageData); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	text := []byte("Comment\x00" + marker)
	chunk := make([]byte, len(text)+12)
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(text)))
	copy(chunk[4:8], "tEXt")
	copy(chunk[8:], text)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	out := append(bytes.Clone(data[:len(data)-12]), chunk...)
	return append(out, data[len(data)-12:]...)
}

func privateUploadJPEG(t *testing.T) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := range 3 {
		for x := range 4 {
			pixels.SetRGBA(x, y, color.RGBA{R: uint8(x * 50), G: uint8(y * 70), B: 50, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, pixels, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	marker := []byte("private-gps-marker")
	exif := make([]byte, 44+len(marker))
	copy(exif, "II")
	binary.LittleEndian.PutUint16(exif[2:4], 42)
	binary.LittleEndian.PutUint32(exif[4:8], 8)
	binary.LittleEndian.PutUint16(exif[8:10], 2)
	binary.LittleEndian.PutUint16(exif[10:12], 0x0112)
	binary.LittleEndian.PutUint16(exif[12:14], 3)
	binary.LittleEndian.PutUint32(exif[14:18], 1)
	binary.LittleEndian.PutUint16(exif[18:20], 6)
	binary.LittleEndian.PutUint16(exif[22:24], 0x8825)
	binary.LittleEndian.PutUint16(exif[24:26], 4)
	binary.LittleEndian.PutUint32(exif[26:30], 1)
	binary.LittleEndian.PutUint32(exif[30:34], 38)
	copy(exif[44:], marker)
	segment := append([]byte("Exif\x00\x00"), exif...)
	jpegBytes := encoded.Bytes()
	out := append(bytes.Clone(jpegBytes[:2]), 0xff, 0xe1, byte((len(segment)+2)>>8), byte(len(segment)+2))
	out = append(out, segment...)
	return append(out, jpegBytes[2:]...)
}

func privateUploadFixture(t *testing.T) (*Store, *Auth, *MediaLibrary, string, string, string) {
	t.Helper()
	s, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	staging, objects := mediaPromotionDirs(t)
	return s, auth, library, token, staging, objects
}

func requireEmptyUploadStages(t *testing.T, staging string) {
	t.Helper()
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) != 0 {
		t.Fatal("upload left a private stage after ordinary cleanup", entries, err)
	}
}

func TestStagedMediaUploadPublishesActualSanitizedSourceAndReplays(t *testing.T) {
	s, auth, library, token, staging, objects := privateUploadFixture(t)
	raw := privateUploadPNG(t, "secret GPS marker A")
	var first Media
	changed := requireProjectionReplay(t, s, func() {
		var err error
		first, err = library.uploadStagedMedia(t.Context(), token, "original-upload-key", "image/png", raw, staging, objects)
		if err != nil {
			t.Fatal("real staged upload transaction failed", err)
		}
	})
	if len(changed) != 1 || !changed[projectionKey{"media", first.ID}] || first.Revision != 1 || first.Verification != "verified" ||
		first.MIMEType != "image/png" || first.Width != 5 || first.Height != 3 || first.ThumbURL != nil || first.ThumbSHA256 != nil {
		t.Fatal("upload published an incorrect public source or unrelated projection", first, changed)
	}
	source, err := loadMedia(t.Context(), s.db, first.ID)
	if err != nil || source.thumbnail == nil || source.storageKey != "sha256-"+source.SHA256 || source.thumbnail.Width != 5 || source.thumbnail.Height != 3 {
		t.Fatal("upload did not retain both actual private source records", source, err)
	}
	full, err := os.ReadFile(filepath.Join(objects, source.storageKey))
	if err != nil || int64(len(full)) != source.Bytes || sha256.Sum256(full) != mustDecodeMediaSHA(t, source.SHA256) || bytes.Contains(full, []byte("secret GPS marker")) {
		t.Fatal("original object did not match its sanitized source", err)
	}
	if _, err = png.Decode(bytes.NewReader(full)); err != nil {
		t.Fatal("sanitized original is not an actual PNG", err)
	}
	thumb, err := os.ReadFile(filepath.Join(objects, "sha256-"+source.thumbnail.SHA256))
	if err != nil || int64(len(thumb)) != source.thumbnail.Bytes || sha256.Sum256(thumb) != mustDecodeMediaSHA(t, source.thumbnail.SHA256) || bytes.Contains(thumb, []byte("secret GPS marker")) {
		t.Fatal("thumbnail object did not match its sanitized source", err)
	}
	if _, err = jpeg.Decode(bytes.NewReader(thumb)); err != nil {
		t.Fatal("thumbnail is not an actual JPEG", err)
	}
	requireEmptyUploadStages(t, staging)
	for _, query := range []string{
		"SELECT response_json FROM api_idempotency WHERE operation='post_media'",
		"SELECT result_json FROM mutation_events WHERE operation='post_media'",
		"SELECT payload_json FROM current_projections WHERE entity_type='media'",
	} {
		if strings.Contains(scalar[string](t, s.db, query), "secret GPS marker") {
			t.Fatal("raw private metadata entered a durable database record")
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links") != 0 {
		t.Fatal("upload omitted the key pin or silently attached an owner")
	}
	_, freshToken := login(t, auth)
	replay, err := library.uploadStagedMedia(t.Context(), freshToken, "original-upload-key", "image/png", raw, "/missing-staging", "/missing-objects")
	if err != nil || replay != first || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_media'") != 1 {
		t.Fatal("same original request did not replay before file preparation", replay, err)
	}
	changedRaw := privateUploadPNG(t, "secret GPS marker B")
	if got, err := library.uploadStagedMedia(t.Context(), freshToken, "original-upload-key", "image/png", changedRaw, staging, objects); !errors.Is(err, ErrIdempotencyConflict) || got != (Media{}) {
		t.Fatal("different private source bytes reused the original key", got, err)
	}
	changed = requireProjectionReplay(t, s, func() {
		var err error
		replay, err = library.uploadStagedMedia(t.Context(), freshToken, "fresh-dedup-key", "image/png", changedRaw, staging, objects)
		if err != nil {
			t.Fatal("different raw metadata did not deduplicate the same sanitized image", err)
		}
	})
	if len(changed) != 0 || replay != first || scalar[int](t, s.db, "SELECT COUNT(*) FROM media") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_media'") != 2 {
		t.Fatal("deduplication silently republished or created a second Media", replay, changed)
	}
	requireEmptyUploadStages(t, staging)
}

func TestStagedMediaUploadSanitizesOrientedJPEGAndThumbnail(t *testing.T) {
	s, _, library, token, staging, objects := privateUploadFixture(t)
	raw := privateUploadJPEG(t)
	result, err := library.uploadStagedMedia(t.Context(), token, "jpeg-orientation-key", "image/jpeg", raw, staging, objects)
	if err != nil || result.MIMEType != "image/jpeg" || result.Width != 3 || result.Height != 4 {
		t.Fatal("JPEG orientation was not reflected in the actual Media source", result, err)
	}
	source, err := loadMedia(t.Context(), s.db, result.ID)
	if err != nil || source.thumbnail == nil || source.thumbnail.Width != 3 || source.thumbnail.Height != 4 {
		t.Fatal("thumbnail did not retain oriented output dimensions", source, err)
	}
	for _, key := range []string{source.storageKey, "sha256-" + source.thumbnail.SHA256} {
		data, err := os.ReadFile(filepath.Join(objects, key))
		if err != nil || bytes.Contains(data, []byte("Exif\x00\x00")) || bytes.Contains(data, []byte("private-gps-marker")) {
			t.Fatal("uploaded JPEG output retained private EXIF bytes", key, err)
		}
		decoded, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil || decoded.Bounds().Dx() != 3 || decoded.Bounds().Dy() != 4 {
			t.Fatal("actual JPEG output lost oriented dimensions", key, err)
		}
	}
	requireEmptyUploadStages(t, staging)
}

func mustDecodeMediaSHA(t *testing.T, digest string) [sha256.Size]byte {
	t.Helper()
	bytes, err := hex.DecodeString(digest)
	if err != nil || len(bytes) != sha256.Size {
		t.Fatal("invalid source digest", err)
	}
	var result [sha256.Size]byte
	copy(result[:], bytes)
	return result
}

func TestStagedMediaUploadDeduplicatesCurrentRevisionWithoutRebasingOriginalReply(t *testing.T) {
	s, auth, library, token, staging, objects := privateUploadFixture(t)
	raw := privateUploadPNG(t, "private")
	first, err := library.uploadStagedMedia(t.Context(), token, "initial-upload-key", "image/png", raw, staging, objects)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(auth)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Actual photo owner"})
	if _, err = library.Attach(t.Context(), token, "attach-uploaded-photo", first.ID, first.Revision, MediaLinkCreate{ItemID: &item.ID, OwnerRevision: item.Revision}); err != nil {
		t.Fatal(err)
	}
	changed := requireProjectionReplay(t, s, func() {
		current, err := library.uploadStagedMedia(t.Context(), token, "new-dedup-key", "image/png", raw, staging, objects)
		if err != nil || current.ID != first.ID || current.Revision != first.Revision+1 {
			t.Fatal("deduplication did not return the actual current Media revision", current, err)
		}
	})
	if len(changed) != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links") != 1 {
		t.Fatal("deduplicating an attached image changed its membership or projections")
	}
	replay, err := library.uploadStagedMedia(t.Context(), token, "initial-upload-key", "image/png", raw, "/missing", "/missing")
	if err != nil || replay != first {
		t.Fatal("original receipt silently rebased to a later membership revision", replay, err)
	}
}

func TestStagedMediaDeduplicationRechecksActualSourceAndMembership(t *testing.T) {
	for _, test := range []struct{ name, trigger string }{
		{"thumbnail", `CREATE TRIGGER late_dedup_change AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE media_thumbnails SET width=1; END`},
		{"media", `CREATE TRIGGER late_dedup_change AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE media SET width=width+1; END`},
		{"link", `CREATE TRIGGER late_dedup_change AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE media_links SET description='changed'; END`},
		{"current projection", `CREATE TRIGGER late_dedup_change AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE current_projections SET payload_json='{"poison":true}' WHERE entity_type='media'; END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, auth, library, token, staging, objects := privateUploadFixture(t)
			raw := privateUploadPNG(t, "private")
			first, err := library.uploadStagedMedia(t.Context(), token, "dedup-initial-key", "image/png", raw, staging, objects)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := NewCatalog(auth)
			if err != nil {
				t.Fatal(err)
			}
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Deduplicated image owner"})
			if _, err = library.Attach(t.Context(), token, "dedup-attach-key", first.ID, first.Revision, MediaLinkCreate{ItemID: &item.ID, OwnerRevision: item.Revision}); err != nil {
				t.Fatal(err)
			}
			before := domainState(t, s)
			execSQL(t, s.db, test.trigger)
			if got, err := library.uploadStagedMedia(t.Context(), token, "dedup-late-change", "image/png", raw, staging, objects); !errors.Is(err, ErrDomainState) || got != (Media{}) {
				t.Fatal("deduplication acknowledged changed actual authority", got, err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT width FROM media_thumbnails WHERE media_id=?", first.ID) != 5 {
				t.Fatal("failed deduplication left changed thumbnail source")
			}
			execSQL(t, s.db, "DROP TRIGGER late_dedup_change")
			if retried, err := library.uploadStagedMedia(t.Context(), token, "dedup-late-change", "image/png", raw, staging, objects); err != nil || retried.ID != first.ID || retried.Revision != 2 {
				t.Fatal("explicit deduplication retry failed after rollback", retried, err)
			}
		})
	}
}

func TestStagedMediaUploadOriginalReceiptSurvivesLaterSourceDeletion(t *testing.T) {
	s, auth, library, token, staging, objects := privateUploadFixture(t)
	raw := privateUploadPNG(t, "private")
	first, err := library.uploadStagedMedia(t.Context(), token, "deleted-source-upload", "image/png", raw, staging, objects)
	if err != nil {
		t.Fatal(err)
	}
	// This test-only mutation deletes the source and freezes a real tombstone;
	// it is not the unimplemented global Media deletion API.
	_, err = runDomainMutation(t.Context(), auth, token, "test-delete-media", "test_delete_media", struct{}{}, func(m *mutationTx) (ActionAck, error) {
		if err := requireDomainRow(m.tx.ExecContext(m.ctx, "DELETE FROM media WHERE id=?", first.ID)); err != nil {
			return ActionAck{}, err
		}
		revision, err := m.touch("media", first.ID, true)
		return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, err
	})
	if err != nil || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails WHERE media_id=?", first.ID) != 0 {
		t.Fatal("test source deletion did not complete with its thumbnail", err)
	}
	if _, err = library.Get(t.Context(), token, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted source still appeared as current Media", err)
	}
	_, freshToken := login(t, auth)
	replayed, err := library.uploadStagedMedia(t.Context(), freshToken, "deleted-source-upload", "image/png", raw, "/missing", "/missing")
	if err != nil || replayed != first || scalar[int](t, s.db, "SELECT COUNT(*) FROM media") != 0 {
		t.Fatal("original receipt failed after deletion or recreated the source", replayed, err)
	}
}

func TestStagedMediaUploadRollsBackEveryDurableDatabaseBoundary(t *testing.T) {
	for _, test := range []struct{ name, trigger string }{
		{"ignored source", `CREATE TRIGGER upload_failure BEFORE INSERT ON media BEGIN SELECT RAISE(IGNORE); END`},
		{"ignored thumbnail", `CREATE TRIGGER upload_failure BEFORE INSERT ON media_thumbnails BEGIN SELECT RAISE(IGNORE); END`},
		{"ignored projection", `CREATE TRIGGER upload_failure BEFORE INSERT ON current_projections WHEN NEW.entity_type='media' BEGIN SELECT RAISE(IGNORE); END`},
		{"ignored outbox", `CREATE TRIGGER upload_failure BEFORE INSERT ON sync_changes WHEN NEW.entity_type='media' BEGIN SELECT RAISE(IGNORE); END`},
		{"ignored audit", `CREATE TRIGGER upload_failure BEFORE INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN SELECT RAISE(IGNORE); END`},
		{"revoked session", `CREATE TRIGGER upload_failure AFTER INSERT ON media BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END`},
		{"changed epoch", `CREATE TRIGGER upload_failure AFTER INSERT ON media BEGIN UPDATE sync_state SET dataset_epoch=lower(hex(randomblob(16))) WHERE singleton=1; END`},
		{"late changed source", `CREATE TRIGGER upload_failure AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE media SET width=width+1 WHERE id=json_extract(NEW.result_json,'$.id'); END`},
		{"late removed thumbnail", `CREATE TRIGGER upload_failure AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN DELETE FROM media_thumbnails WHERE media_id=json_extract(NEW.result_json,'$.id'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, _, library, token, staging, objects := privateUploadFixture(t)
			raw := privateUploadPNG(t, "private-marker")
			execSQL(t, s.db, test.trigger)
			before := domainState(t, s)
			if result, err := library.uploadStagedMedia(t.Context(), token, "rollback-upload-key", "image/png", raw, staging, objects); err == nil || result != (Media{}) {
				t.Fatal("failed publication returned a Media acknowledgement", result, err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 0 {
				t.Fatal("failed publication retained a thumbnail or key pin")
			}
			requireEmptyUploadStages(t, staging)
			// Promoted objects can be shared; failure must not delete them.
			entries, err := os.ReadDir(objects)
			if err != nil || len(entries) == 0 {
				t.Fatal("failed publication discarded potentially shared objects", entries, err)
			}
			execSQL(t, s.db, "DROP TRIGGER upload_failure")
			if retry, err := library.uploadStagedMedia(t.Context(), token, "rollback-upload-key", "image/png", raw, staging, objects); err != nil || retry.Revision != 1 {
				t.Fatal("explicit retry could not reuse safe orphaned objects", retry, err)
			}
		})
	}
}

func TestStagedMediaUploadRejectsUnsafeImagesAndUnauthorizedSessions(t *testing.T) {
	s, _, library, token, staging, objects := privateUploadFixture(t)
	raw := privateUploadPNG(t, "private")
	before := domainState(t, s)
	for _, test := range []struct {
		token, key, mime string
		raw              []byte
		want             error
	}{
		{"invalid-token", "wrong-session-key", "image/png", raw, ErrAuthentication},
		{token, "short", "image/png", raw, ErrDomainInput},
		{token, "wrong-mime-key", "image/jpeg", raw, mediaimage.ErrInvalid},
		{token, "unsupported-webp-key", "image/webp", []byte("RIFF....WEBP"), mediaimage.ErrUnsupported},
		{token, "broken-png-key", "image/png", raw[:len(raw)-1], mediaimage.ErrInvalid},
	} {
		if got, err := library.uploadStagedMedia(t.Context(), test.token, test.key, test.mime, test.raw, staging, objects); !errors.Is(err, test.want) || got != (Media{}) {
			t.Fatal("invalid upload crossed an authorization or image boundary", got, err)
		}
		requireEmptyUploadStages(t, staging)
	}
	requireDomainState(t, s, before)
	if entries, err := os.ReadDir(objects); err != nil || len(entries) != 0 {
		t.Fatal("invalid input promoted any object", entries, err)
	}
}

func TestStagedMediaUploadRejectsLegacyStorageKeyCollisions(t *testing.T) {
	for _, verification := range []string{"unverified", "verified"} {
		t.Run(verification, func(t *testing.T) {
			t.Parallel()
			s, _, library, token, staging, objects := privateUploadFixture(t)
			raw := privateUploadPNG(t, "private")
			var original, thumbnail bytes.Buffer
			result, _, err := mediaimage.SanitizeWithThumbnail(t.Context(), raw, "image/png", &original, &thumbnail, mediaimage.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			key := mediaObjectKey(result.SHA256)
			execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at,verification) VALUES('legacy',?,?,?,5,3,?,'old',?)`, key, hex.EncodeToString(result.SHA256[:]), "image/png", result.Bytes, verification)
			seedMediaProjection(t, s, "legacy")
			before := domainState(t, s)
			if got, err := library.uploadStagedMedia(t.Context(), token, "collision-upload-key", "image/png", raw, staging, objects); !errors.Is(err, ErrDomainState) || got != (Media{}) {
				t.Fatal("unverified or unproven legacy storage was silently adopted", got, err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails") != 0 {
				t.Fatal("legacy collision gained an invented thumbnail")
			}
			requireEmptyUploadStages(t, staging)
		})
	}
}

func TestStagedMediaUploadConcurrentKeysDeduplicate(t *testing.T) {
	for _, test := range []struct {
		name   string
		keys   []string
		events int
	}{
		{"distinct keys", []string{"concurrent-upload-one", "concurrent-upload-two"}, 2},
		{"same key", []string{"concurrent-upload-one", "concurrent-upload-one"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, _, library, token, staging, objects := privateUploadFixture(t)
			raw := privateUploadPNG(t, "private")
			type result struct {
				value Media
				err   error
			}
			start := make(chan struct{})
			results := make(chan result, 2)
			var pending sync.WaitGroup
			for _, key := range test.keys {
				pending.Add(1)
				go func() {
					defer pending.Done()
					<-start
					value, err := library.uploadStagedMedia(t.Context(), token, key, "image/png", raw, staging, objects)
					results <- result{value, err}
				}()
			}
			close(start)
			pending.Wait()
			first, second := <-results, <-results
			if first.err != nil || second.err != nil || first.value != second.value || first.value.Revision != 1 {
				t.Fatal("concurrent sanitized images did not resolve to one Media", first, second)
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM media") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails") != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_media'") != test.events ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='media'") != 1 {
				t.Fatal("concurrent deduplication produced duplicate source or missed an acknowledgement")
			}
			requireEmptyUploadStages(t, staging)
		})
	}
}

func TestStagedMediaUploadDetectsActualFileTamperingBeforeCommit(t *testing.T) {
	s, auth, library, token, staging, objects := privateUploadFixture(t)
	raw := privateUploadPNG(t, "private")
	var original, thumbnail bytes.Buffer
	imageResult, _, err := mediaimage.SanitizeWithThumbnail(t.Context(), raw, "image/png", &original, &thumbnail, mediaimage.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(objects, mediaObjectKey(imageResult.SHA256))
	clock := auth.now
	tampered := false
	calls := 0
	var tamperErr error
	auth.now = func() time.Time {
		calls++
		// The fourth clock read follows the audit and session-activity writes.
		if calls == 4 {
			content, readErr := os.ReadFile(path)
			if readErr != nil || len(content) == 0 {
				tamperErr = errors.Join(readErr, errors.New("expected promoted object"))
			} else {
				tampered = true
				content[0] ^= 1
				tamperErr = errors.Join(os.Chmod(path, 0o600), os.WriteFile(path, content, 0o600))
			}
		}
		return clock()
	}
	before := domainState(t, s)
	got, err := library.uploadStagedMedia(t.Context(), token, "tampered-upload-key", "image/png", raw, staging, objects)
	if !tampered || tamperErr != nil || err == nil || got != (Media{}) {
		t.Fatal("tampered promoted bytes were published", tampered, tamperErr, got, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails") != 0 {
		t.Fatal("corrupt object left a Media thumbnail source")
	}
	requireEmptyUploadStages(t, staging)
}

func TestStagedMediaUploadRetainsOriginalReceiptAfterCleanupFailure(t *testing.T) {
	s, auth, library, token, staging, objects := privateUploadFixture(t)
	raw := privateUploadPNG(t, "private")
	clock := auth.now
	var replacement string
	var swapErr error
	auth.now = func() time.Time {
		entries, readErr := os.ReadDir(objects)
		if replacement == "" && swapErr == nil && readErr == nil && len(entries) >= 2 {
			stages, stageErr := os.ReadDir(staging)
			if stageErr != nil || len(stages) != 2 {
				swapErr = errors.Join(stageErr, errors.New("expected two stages"))
			} else {
				replacement = filepath.Join(staging, stages[0].Name())
				swapErr = os.Rename(replacement, replacement+".preserved")
				if swapErr == nil {
					swapErr = os.WriteFile(replacement, []byte("unrelated replacement"), 0o600)
				}
			}
		}
		return clock()
	}
	result, err := library.uploadStagedMedia(t.Context(), token, "cleanup-uncertain-key", "image/png", raw, staging, objects)
	if replacement == "" || swapErr != nil || !errors.Is(err, errMediaStageChanged) || result != (Media{}) {
		t.Fatal("cleanup substitution did not leave an uncertain committed result", replacement, swapErr, result, err)
	}
	if content, err := os.ReadFile(replacement); err != nil || string(content) != "unrelated replacement" {
		t.Fatal("cleanup removed or changed the substituted file", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_media'") != 1 {
		t.Fatal("the successful transaction was incorrectly treated as rolled back")
	}
	replay, err := library.uploadStagedMedia(t.Context(), token, "cleanup-uncertain-key", "image/png", raw, "/missing", "/missing")
	if err != nil || replay.ID == "" || replay.Revision != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM media") != 1 {
		t.Fatal("explicit original-key retry could not reconcile the committed upload", replay, err)
	}
}

func TestStagedMediaUploadCancellationDoesNotPublishPartialState(t *testing.T) {
	s, _, library, token, staging, objects := privateUploadFixture(t)
	before := domainState(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := library.uploadStagedMedia(ctx, token, "cancelled-upload-key", "image/png", privateUploadPNG(t, "private"), staging, objects); !errors.Is(err, context.Canceled) || result != (Media{}) {
		t.Fatal("canceled upload returned a source", result, err)
	}
	requireDomainState(t, s, before)
	requireEmptyUploadStages(t, staging)
}

func TestPublishStagedMediaRejectsInconsistentSanitizerResults(t *testing.T) {
	dir, err := os.Open(privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	digest, smallDigest := sha256.Sum256([]byte("original")), sha256.Sum256([]byte("preview"))
	imageResult := mediaimage.Result{MIMEType: "image/png", Width: 10, Height: 8, Bytes: 8, SHA256: digest}
	preview := mediaimage.Result{MIMEType: "image/jpeg", Width: 10, Height: 8, Bytes: 7, SHA256: smallDigest}
	original := promotedMedia{key: mediaObjectKey(digest), digest: mediaStageDigest{SHA256: digest, Bytes: 8}}
	thumbnail := promotedMedia{key: mediaObjectKey(smallDigest), digest: mediaStageDigest{SHA256: smallDigest, Bytes: 7}}
	for _, mutate := range []func(*mediaimage.Result, *mediaimage.Result, *promotedMedia, *promotedMedia, **os.File){
		func(image, _ *mediaimage.Result, _, _ *promotedMedia, _ **os.File) { image.MIMEType = "image/webp" },
		func(image, _ *mediaimage.Result, _, _ *promotedMedia, _ **os.File) {
			image.Width = mediaimage.MaxDimension + 1
		},
		func(image, _ *mediaimage.Result, _, _ *promotedMedia, _ **os.File) {
			image.Width, image.Height = 8001, 8000
		},
		func(_, preview *mediaimage.Result, _, _ *promotedMedia, _ **os.File) { preview.MIMEType = "image/png" },
		func(_, preview *mediaimage.Result, _, _ *promotedMedia, _ **os.File) { preview.Width = 11 },
		func(_, preview *mediaimage.Result, _, _ *promotedMedia, _ **os.File) { preview.Bytes = 0 },
		func(_, _ *mediaimage.Result, original, _ *promotedMedia, _ **os.File) { original.key = "../unsafe" },
		func(_, _ *mediaimage.Result, _, thumbnail *promotedMedia, _ **os.File) { thumbnail.digest.Bytes++ },
		func(_, _ *mediaimage.Result, _, _ *promotedMedia, objects **os.File) { *objects = nil },
	} {
		image, small, source, thumb, objects := imageResult, preview, original, thumbnail, dir
		mutate(&image, &small, &source, &thumb, &objects)
		m := mutationTx{ctx: t.Context()}
		if result, err := m.publishStagedMedia(image, small, source, thumb, objects); !errors.Is(err, ErrDomainState) || result != (Media{}) {
			t.Fatal("inconsistent sanitizer/file results crossed the source boundary", result, err)
		}
	}
}

func TestStagedMediaUploadFrozenResultRejectsInvalidRepresentations(t *testing.T) {
	valid := Media{ID: "media-id", Revision: 2, SHA256: strings.Repeat("a", 64), MIMEType: "image/png", Verification: "verified", Width: 8, Height: 6, Bytes: 100}
	if err := valid.validateDomainResult(); err != nil {
		t.Fatal("valid deduplicated upload receipt was rejected", err)
	}
	for _, mutate := range []func(*Media){
		func(v *Media) { v.ID = "" },
		func(v *Media) { v.Revision = 0 },
		func(v *Media) { v.SHA256 = strings.Repeat("A", 64) },
		func(v *Media) { v.Verification = "unverified" },
		func(v *Media) { v.MIMEType = "image/heic" },
		func(v *Media) { v.Width, v.Height = 8001, 8000 },
		func(v *Media) { v.Bytes = maxMediaFileBytes + 1 },
		func(v *Media) { v.ThumbURL = testPointer("/api/v1/media/media-id/thumbnail") },
		func(v *Media) { v.ThumbSHA256 = testPointer(strings.Repeat("b", 64)) },
	} {
		result := valid
		mutate(&result)
		if !errors.Is(result.validateDomainResult(), ErrDomainState) {
			t.Fatal("invalid frozen upload result was accepted", result)
		}
	}
}

func TestStagedMediaUploadFrozenResultHasNoPrivateFileFields(t *testing.T) {
	_, _, library, token, staging, objects := privateUploadFixture(t)
	result, err := library.uploadStagedMedia(t.Context(), token, "safe-upload-output", "image/png", privateUploadPNG(t, "secret"), staging, objects)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(encoded, &fields) != nil || len(fields) != 10 || fields["storage_key"] != nil || fields["thumbnail_bytes"] != nil ||
		string(fields["thumb_url"]) != "null" || string(fields["thumb_sha256"]) != "null" || strings.Contains(string(encoded), "secret") {
		t.Fatal("upload result leaked a private source or invented a stream", string(encoded), err)
	}
	if !reflect.DeepEqual(fields["verification"], json.RawMessage(`"verified"`)) {
		t.Fatal("new sanitized upload lacked verified status")
	}
}
