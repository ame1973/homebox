//go:build linux

package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type recoveryFixture struct {
	store   *Store
	auth    *Auth
	library *MediaLibrary
	token   string
	storage *mediaStorage
	stages  []*mediaStage
	pending []pendingMediaObject
}

func newRecoveryFixture(t *testing.T) recoveryFixture {
	t.Helper()
	s, auth, _, _, token := completedAuthFixture(t)
	storage := testMediaStorage(t, s)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(storage.path, "staging")
	stages := []*mediaStage{
		sealedMediaStage(t, staging, []byte("sanitized original")),
		sealedMediaStage(t, staging, []byte("sanitized thumbnail")),
	}
	pending, err := library.journalMediaObjects(t.Context(), token, stages, storage.objects)
	if err != nil {
		t.Fatal(err)
	}
	return recoveryFixture{store: s, auth: auth, library: library, token: token, storage: storage, stages: stages, pending: pending}
}

func (f recoveryFixture) stagePath(index int) string {
	return filepath.Join(f.storage.path, "staging", f.pending[index].StageName)
}

func (f recoveryFixture) objectPath(index int) string {
	return filepath.Join(f.storage.path, "objects", "sha256-"+f.pending[index].SHA256)
}

func (f recoveryFixture) promote(t *testing.T, index int) promotedMedia {
	t.Helper()
	result, err := f.stages[index].promoteTo(f.storage.objects)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f recoveryFixture) requireOtherPendingIntact(t *testing.T) {
	t.Helper()
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[1].ID); count != 1 {
		t.Fatal("recovery changed a different pending object", count)
	}
	if _, err := os.Lstat(f.stagePath(1)); err != nil {
		t.Fatal("recovery removed a different stage", err)
	}
}

func seedRecoveryReference(t *testing.T, f recoveryFixture, kind string) {
	t.Helper()
	value := f.pending[0]
	ownDigest := value.SHA256
	storageKey := "sha256-" + ownDigest
	verification := "unverified"
	if kind == "thumbnail" {
		ownDigest = strings.Repeat("b", 64)
		storageKey = "sha256-" + ownDigest
		verification = "verified"
	}
	if kind == "unrelated" {
		ownDigest = strings.Repeat("d", 64)
		storageKey = "sha256-" + ownDigest
	}
	if kind == "ambiguous legacy" {
		ownDigest = strings.Repeat("c", 64)
		storageKey = "legacy/unknown"
	}
	execSQL(t, f.store.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at,verification)
 VALUES('recovery-reference',?,?, 'image/png',1,1,?,'2026',?)`, storageKey, ownDigest, value.Bytes, verification)
	if kind == "thumbnail" {
		execSQL(t, f.store.db, `INSERT INTO media_thumbnails(media_id,sha256,width,height,bytes) VALUES('recovery-reference',?,1,1,?)`, value.SHA256, value.Bytes)
	}
	seedMediaProjection(t, f.store, "recovery-reference")
}

func TestMediaRecoveryRemovesOnlyProvenUnreferencedFiles(t *testing.T) {
	for _, scenario := range []string{"unpromoted", "promoted", "reused unrelated object", "already absent"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newRecoveryFixture(t)
			object := f.objectPath(0)
			switch scenario {
			case "promoted":
				if f.promote(t, 0).reused {
					t.Fatal("new object unexpectedly reused")
				}
			case "reused unrelated object":
				if err := os.WriteFile(object, []byte("sanitized original"), 0o600); err != nil {
					t.Fatal(err)
				}
				if !f.promote(t, 0).reused {
					t.Fatal("test did not reuse the unrelated object")
				}
			case "already absent":
				if err := os.Remove(f.stagePath(0)); err != nil {
					t.Fatal(err)
				}
			}
			before := domainState(t, f.store)
			result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID)
			if err != nil || result.Referenced || result.ObjectRemoved != (scenario == "promoted") || result.StageRemoved != (scenario != "already absent") {
				t.Fatal("unexpected recovery result", scenario, result, err)
			}
			if _, err = os.Lstat(f.stagePath(0)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("recovered stage remains", err)
			}
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 0 {
				t.Fatal("recovered pending row remains", count)
			}
			if scenario == "reused unrelated object" {
				if data, err := os.ReadFile(object); err != nil || string(data) != "sanitized original" {
					t.Fatal("recovery removed or changed an unrelated object", err)
				}
			} else if _, err = os.Lstat(object); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unreferenced owned object remains", err)
			}
			requireDomainState(t, f.store, before)
			f.requireOtherPendingIntact(t)
		})
	}
}

func TestMediaRecoveryResumesPartialUnlinks(t *testing.T) {
	for _, scenario := range []string{"object removed", "both removed", "referenced stage removed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newRecoveryFixture(t)
			f.promote(t, 0)
			if scenario == "referenced stage removed" {
				seedRecoveryReference(t, f, "original")
			} else if err := os.Remove(f.objectPath(0)); err != nil {
				t.Fatal(err)
			}
			if scenario != "object removed" {
				if err := os.Remove(f.stagePath(0)); err != nil {
					t.Fatal(err)
				}
			}
			if err := errors.Join(syncRecoveryDirectory(f.storage.staging), syncRecoveryDirectory(f.storage.objects)); err != nil {
				t.Fatal(err)
			}
			result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID)
			if err != nil || result.StageRemoved != (scenario == "object removed") || result.ObjectRemoved || result.Referenced != (scenario == "referenced stage removed") {
				t.Fatal("partial unlink was not reconciled", scenario, result, err)
			}
			if scenario == "referenced stage removed" {
				if data, err := os.ReadFile(f.objectPath(0)); err != nil || string(data) != "sanitized original" {
					t.Fatal("recovery changed a referenced object", err)
				}
			}
			f.requireOtherPendingIntact(t)
		})
	}
}

func TestMediaRecoveryKeepsReferencedOriginalAndThumbnailObjects(t *testing.T) {
	for _, kind := range []string{"original", "thumbnail"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newRecoveryFixture(t)
			f.promote(t, 0)
			seedRecoveryReference(t, f, kind)
			before := domainState(t, f.store)
			result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID)
			if err != nil || !result.Referenced || !result.StageRemoved || result.ObjectRemoved {
				t.Fatal("referenced file was not preserved", kind, result, err)
			}
			if data, err := os.ReadFile(f.objectPath(0)); err != nil || string(data) != "sanitized original" {
				t.Fatal("recovery changed a referenced object", err)
			}
			if _, err = os.Lstat(f.stagePath(0)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("referenced object's redundant stage remains", err)
			}
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 0 {
				t.Fatal("completed referenced stage remains pending", count)
			}
			requireDomainState(t, f.store, before)
			f.requireOtherPendingIntact(t)
		})
	}
}

func TestMediaRecoveryReconcilesFailedRealUploadBeforeExplicitRetry(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	storage := testMediaStorage(t, s)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	raw := privateUploadPNG(t, "failed real publication")
	execSQL(t, s.db, `CREATE TRIGGER reject_recovery_upload BEFORE INSERT ON media BEGIN SELECT RAISE(ABORT,'test failure'); END`)
	if value, err := storage.upload(t.Context(), library, token, "recovery-failed-upload", "image/png", raw); err == nil || value != (Media{}) {
		t.Fatal("test upload unexpectedly published", value, err)
	}
	execSQL(t, s.db, "DROP TRIGGER reject_recovery_upload")
	before := domainState(t, s)
	ids, next, err := storage.pendingMediaRecoveryPage(t.Context(), "", 10)
	if err != nil || len(ids) != 2 || next != "" {
		t.Fatal("failed upload did not retain both pending objects", ids, next, err)
	}
	for _, id := range ids {
		result, err := storage.recoverPendingMedia(t.Context(), id)
		if err != nil || !result.StageRemoved || !result.ObjectRemoved || result.Referenced {
			t.Fatal("failed upload did not recover its real object", result, err)
		}
	}
	requireDomainState(t, s, before)
	for _, directory := range []string{"staging", "objects"} {
		entries, err := os.ReadDir(filepath.Join(storage.path, directory))
		if err != nil || len(entries) != 1 || entries[0].Name() != mediaOwnerName {
			t.Fatal("recovery left unexpected private files", directory, entries, err)
		}
	}
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("recovery left a pending source", count)
	}
	value, err := storage.upload(t.Context(), library, token, "recovery-failed-upload", "image/png", raw)
	if err != nil || value.ID == "" || value.Verification != "verified" {
		t.Fatal("explicit original-key retry could not publish after recovery", value, err)
	}
}

func TestMediaRecoveryPreservesDeduplicatedSourceAndOriginalReplay(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	storage := testMediaStorage(t, s)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	raw := privateUploadPNG(t, "private recovery proof")
	first, err := storage.upload(t.Context(), library, token, "recovery-original-key", "image/png", raw)
	if err != nil {
		t.Fatal(err)
	}
	source, err := loadMedia(t.Context(), s.db, first.ID)
	if err != nil || source.thumbnail == nil {
		t.Fatal("real upload did not produce its source", err)
	}
	originalPath := filepath.Join(storage.path, "objects", source.storageKey)
	thumbnailPath := filepath.Join(storage.path, "objects", "sha256-"+source.thumbnail.SHA256)
	original, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	thumbnail, err := os.ReadFile(thumbnailPath)
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(storage.path, "staging")
	stages := []*mediaStage{sealedMediaStage(t, staging, original), sealedMediaStage(t, staging, thumbnail)}
	pending, err := library.journalMediaObjects(t.Context(), token, stages, storage.objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if result, err := stage.promoteTo(storage.objects); err != nil || !result.reused {
			t.Fatal("matching upload did not reuse actual source bytes", result, err)
		}
	}
	result, err := storage.recoverPendingMedia(t.Context(), pending[0].ID)
	if err != nil || !result.Referenced || !result.StageRemoved || result.ObjectRemoved {
		t.Fatal("recovery did not retain the real deduplicated source", result, err)
	}
	if after, err := os.ReadFile(originalPath); err != nil || string(after) != string(original) {
		t.Fatal("recovery changed the real original", err)
	}
	if after, err := os.ReadFile(thumbnailPath); err != nil || string(after) != string(thumbnail) {
		t.Fatal("recovery changed the real thumbnail", err)
	}
	replay, err := storage.upload(t.Context(), library, token, "recovery-original-key", "image/png", raw)
	if err != nil || replay != first {
		t.Fatal("recovery broke the original-key acknowledgement", replay, err)
	}
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", pending[1].ID); count != 1 {
		t.Fatal("recovery changed a separate pending thumbnail", count)
	}
}

func TestMediaRecoveryWithholdsUnprovenObjectsAndLegacyReferences(t *testing.T) {
	for _, scenario := range []string{"missing stage", "missing referenced object", "ambiguous legacy"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newRecoveryFixture(t)
			switch scenario {
			case "missing stage":
				f.promote(t, 0)
				if err := os.Remove(f.stagePath(0)); err != nil {
					t.Fatal(err)
				}
			case "missing referenced object":
				seedRecoveryReference(t, f, "original")
			case "ambiguous legacy":
				f.promote(t, 0)
				seedRecoveryReference(t, f, "ambiguous legacy")
			}
			if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); !errors.Is(err, errMediaRecoveryUnproven) || result != (mediaRecoveryResult{}) {
				t.Fatal("unproven recovery changed state", scenario, result, err)
			}
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 1 {
				t.Fatal("unproven recovery removed its journal", count)
			}
			if scenario != "missing stage" {
				if _, err := os.Lstat(f.stagePath(0)); err != nil {
					t.Fatal("unproven recovery removed its stage", err)
				}
			}
			if scenario != "missing referenced object" {
				if data, err := os.ReadFile(f.objectPath(0)); err != nil || string(data) != "sanitized original" {
					t.Fatal("unproven recovery removed an object", err)
				}
			}
			f.requireOtherPendingIntact(t)
		})
	}
}

func TestMediaRecoveryRollsBackLateUnrelatedSourceChanges(t *testing.T) {
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	seedRecoveryReference(t, f, "unrelated")
	before, err := loadMedia(t.Context(), f.store.db, "recovery-reference")
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, f.store.db, `CREATE TRIGGER recovery_changes_source AFTER DELETE ON media_pending_objects
 BEGIN UPDATE media SET mime_type='image/jpeg' WHERE id='recovery-reference'; END`)
	if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); err == nil || result != (mediaRecoveryResult{}) {
		t.Fatal("a late unrelated source change committed", result, err)
	}
	after, err := loadMedia(t.Context(), f.store.db, "recovery-reference")
	if err != nil || after.MIMEType != before.MIMEType || after.storageKey != before.storageKey || after.Revision != before.Revision {
		t.Fatal("failed recovery changed unrelated source", after, err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 1 {
		t.Fatal("failed recovery lost its pending row", count)
	}
	execSQL(t, f.store.db, "DROP TRIGGER recovery_changes_source")
	if _, err = f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); err != nil {
		t.Fatal("interrupted cleanup could not be reconciled", err)
	}
	f.requireOtherPendingIntact(t)
}

func TestMediaRecoveryRollsBackLateProjectionChanges(t *testing.T) {
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	seedRecoveryReference(t, f, "unrelated")
	before := scalar[string](t, f.store.db, "SELECT payload_json FROM current_projections WHERE entity_type='media' AND entity_id='recovery-reference'")
	execSQL(t, f.store.db, `CREATE TRIGGER recovery_changes_projection AFTER DELETE ON media_pending_objects
 BEGIN UPDATE current_projections SET payload_json=json_set(payload_json,'$.mime_type','image/jpeg') WHERE entity_type='media' AND entity_id='recovery-reference'; END`)
	if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); err == nil || result != (mediaRecoveryResult{}) {
		t.Fatal("a late projection change committed", result, err)
	}
	if actual := scalar[string](t, f.store.db, "SELECT payload_json FROM current_projections WHERE entity_type='media' AND entity_id='recovery-reference'"); actual != before {
		t.Fatal("failed recovery changed existing projection bytes")
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 1 {
		t.Fatal("failed recovery lost its pending row", count)
	}
}

func TestMediaRecoveryRollsBackLateCounterOnlyChanges(t *testing.T) {
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	execSQL(t, f.store.db, "INSERT INTO projection_revisions VALUES('media','counter-only',7)")
	execSQL(t, f.store.db, `CREATE TRIGGER recovery_changes_counter AFTER DELETE ON media_pending_objects
 BEGIN UPDATE projection_revisions SET revision=8 WHERE entity_type='media' AND entity_id='counter-only'; END`)
	if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); err == nil || result != (mediaRecoveryResult{}) {
		t.Fatal("a late counter-only change committed", result, err)
	}
	if revision := scalar[int](t, f.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='counter-only'"); revision != 7 {
		t.Fatal("failed recovery changed a retained counter", revision)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 1 {
		t.Fatal("failed recovery lost its pending row", count)
	}
}

func TestMediaRecoveryRollsBackLateJournalAndReferenceWrites(t *testing.T) {
	for _, scenario := range []string{"ignored delete", "reinsert current", "delete sibling", "insert reference", "change epoch", "change instance", "change watermark"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newRecoveryFixture(t)
			f.promote(t, 0)
			var trigger string
			switch scenario {
			case "ignored delete":
				trigger = `CREATE TRIGGER recovery_failure BEFORE DELETE ON media_pending_objects BEGIN SELECT RAISE(IGNORE); END`
			case "reinsert current":
				trigger = `CREATE TRIGGER recovery_failure AFTER DELETE ON media_pending_objects BEGIN
 INSERT INTO media_pending_objects VALUES(OLD.id,OLD.dataset_epoch,OLD.sha256,OLD.bytes,OLD.created_at,OLD.stage_name,OLD.stage_device,OLD.stage_inode,OLD.staging_device,OLD.staging_inode,OLD.object_device,OLD.object_inode); END`
			case "delete sibling":
				trigger = fmt.Sprintf(`CREATE TRIGGER recovery_failure AFTER DELETE ON media_pending_objects WHEN OLD.id='%s' BEGIN
 DELETE FROM media_pending_objects WHERE id='%s'; END`, f.pending[0].ID, f.pending[1].ID)
			case "insert reference":
				trigger = `CREATE TRIGGER recovery_failure AFTER DELETE ON media_pending_objects BEGIN
 INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at,verification)
 VALUES('late-reference','sha256-'||OLD.sha256,OLD.sha256,'image/png',1,1,OLD.bytes,'2026','unverified');
 INSERT INTO projection_revisions VALUES('media','late-reference',1); END`
			case "change epoch":
				trigger = `CREATE TRIGGER recovery_failure AFTER DELETE ON media_pending_objects BEGIN UPDATE sync_state SET dataset_epoch=lower(hex(randomblob(16))) WHERE singleton=1; END`
			case "change instance":
				trigger = `CREATE TRIGGER recovery_failure AFTER DELETE ON media_pending_objects BEGIN UPDATE sync_state SET server_instance_id=lower(hex(randomblob(16))) WHERE singleton=1; END`
			case "change watermark":
				trigger = `CREATE TRIGGER recovery_failure AFTER DELETE ON media_pending_objects BEGIN UPDATE sync_state SET min_retained_seq=min_retained_seq+1 WHERE singleton=1; END`
			}
			execSQL(t, f.store.db, trigger)
			if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); err == nil || result != (mediaRecoveryResult{}) {
				t.Fatal("a late journal or source write committed", scenario, result, err)
			}
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
				t.Fatal("failed recovery changed pending authority", count)
			}
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media WHERE id='late-reference'"); count != 0 {
				t.Fatal("failed recovery published a late reference", count)
			}
			for _, path := range []string{f.stagePath(0), f.objectPath(0)} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("test did not reach the durable unlink before rollback", path, err)
				}
			}
			execSQL(t, f.store.db, "DROP TRIGGER recovery_failure")
			result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID)
			if err != nil || result.StageRemoved || result.ObjectRemoved || result.Referenced {
				t.Fatal("retry could not reconcile previously removed files", result, err)
			}
			f.requireOtherPendingIntact(t)
		})
	}
}

func TestMediaRecoveryRetainsUnsafeFilesAndAuthority(t *testing.T) {
	for _, scenario := range []string{
		"stage symlink", "stage changed bytes", "stage extra hardlink", "stage public", "object symlink", "object corrupt", "linked bytes corrupt", "linked extra hardlink",
		"changed epoch", "wrong directory identity", "unsafe stage name", "invalid sibling journal", "missing thumbnail table", "missing media counter", "orphan thumbnail", "invalid projection",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newRecoveryFixture(t)
			stage, object := f.stagePath(0), f.objectPath(0)
			var restore func() error
			switch scenario {
			case "stage symlink":
				if err := os.Rename(stage, stage+"-held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.storage.path, "outside"), stage); err != nil {
					t.Fatal(err)
				}
				restore = func() error { return errors.Join(os.Remove(stage), os.Rename(stage+"-held", stage)) }
			case "stage changed bytes":
				if err := os.WriteFile(stage, []byte(strings.Repeat("x", int(f.pending[0].Bytes))), 0o600); err != nil {
					t.Fatal(err)
				}
			case "stage extra hardlink":
				if err := os.Link(stage, stage+"-extra"); err != nil {
					t.Fatal(err)
				}
				restore = func() error { return os.Remove(stage + "-extra") }
			case "stage public":
				if err := os.Chmod(stage, 0o644); err != nil {
					t.Fatal(err)
				}
				restore = func() error { return os.Chmod(stage, 0o600) }
			case "object symlink":
				f.promote(t, 0)
				if err := os.Rename(object, object+"-held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.storage.path, "outside"), object); err != nil {
					t.Fatal(err)
				}
				restore = func() error { return errors.Join(os.Remove(object), os.Rename(object+"-held", object)) }
			case "object corrupt":
				if err := os.WriteFile(object, []byte("unrelated corrupt object"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "linked bytes corrupt":
				f.promote(t, 0)
				if err := os.Chmod(stage, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stage, []byte(strings.Repeat("x", int(f.pending[0].Bytes))), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(stage, 0o400); err != nil {
					t.Fatal(err)
				}
			case "linked extra hardlink":
				f.promote(t, 0)
				if err := os.Link(object, object+"-extra"); err != nil {
					t.Fatal(err)
				}
				restore = func() error { return os.Remove(object + "-extra") }
			case "changed epoch":
				execSQL(t, f.store.db, "UPDATE sync_state SET dataset_epoch=lower(hex(randomblob(16))) WHERE singleton=1")
			case "wrong directory identity", "unsafe stage name", "invalid sibling journal":
				execSQL(t, f.store.db, "DROP TRIGGER media_pending_objects_no_update")
				switch scenario {
				case "wrong directory identity":
					execSQL(t, f.store.db, "UPDATE media_pending_objects SET staging_inode=staging_inode+1000000 WHERE id=?", f.pending[0].ID)
				case "unsafe stage name":
					execSQL(t, f.store.db, "UPDATE media_pending_objects SET stage_name='../outside' WHERE id=?", f.pending[0].ID)
				case "invalid sibling journal":
					execSQL(t, f.store.db, "UPDATE media_pending_objects SET stage_name='../outside' WHERE id=?", f.pending[1].ID)
				}
			case "missing thumbnail table":
				execSQL(t, f.store.db, "DROP TABLE media_thumbnails")
			case "missing media counter":
				digest := strings.Repeat("a", 64)
				execSQL(t, f.store.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at,verification)
 VALUES('bad-source',?,?,'image/png',1,1,1,'2026','unverified')`, "sha256-"+digest, digest)
			case "orphan thumbnail":
				execSQL(t, f.store.db, "PRAGMA foreign_keys=OFF")
				execSQL(t, f.store.db, "DROP TRIGGER media_thumbnails_verified_insert")
				execSQL(t, f.store.db, "INSERT INTO media_thumbnails VALUES('missing',?,1,1,1)", strings.Repeat("a", 64))
				execSQL(t, f.store.db, "PRAGMA foreign_keys=ON")
			case "invalid projection":
				seedRecoveryReference(t, f, "unrelated")
				execSQL(t, f.store.db, "PRAGMA ignore_check_constraints=ON")
				execSQL(t, f.store.db, "UPDATE current_projections SET payload_json='null' WHERE entity_type='media' AND entity_id='recovery-reference'")
				execSQL(t, f.store.db, "PRAGMA ignore_check_constraints=OFF")
			}
			if restore != nil {
				defer func() {
					if err := restore(); err != nil {
						t.Error("restore test-owned path", err)
					}
				}()
			}
			if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); err == nil || result != (mediaRecoveryResult{}) {
				t.Fatal("unsafe recovery was accepted", scenario, result, err)
			}
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 1 {
				t.Fatal("unsafe recovery removed its pending row", count)
			}
			if _, err := os.Lstat(stage); err != nil {
				t.Fatal("unsafe recovery removed its stage", err)
			}
			if scenario == "object symlink" || scenario == "object corrupt" || scenario == "linked bytes corrupt" || scenario == "linked extra hardlink" {
				if _, err := os.Lstat(object); err != nil {
					t.Fatal("unsafe recovery removed an unproven object", err)
				}
			}
			f.requireOtherPendingIntact(t)
		})
	}
}

func TestMediaRecoveryRetriesAfterFilesystemUnlinkFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can bypass private-directory write permissions")
	}
	for _, directory := range []string{"objects", "staging"} {
		t.Run(directory, func(t *testing.T) {
			t.Parallel()
			f := newRecoveryFixture(t)
			f.promote(t, 0)
			path := filepath.Join(f.storage.path, directory)
			if err := os.Chmod(path, 0o500); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chmod(path, 0o700); err != nil {
					t.Error(err)
				}
			}()
			if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); err == nil || result != (mediaRecoveryResult{}) {
				t.Fatal("unwritable directory returned a successful cleanup", result, err)
			}
			if _, err := os.Lstat(f.stagePath(0)); err != nil {
				t.Fatal("failed cleanup lost its remaining stage", err)
			}
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 1 {
				t.Fatal("failed cleanup lost its journal", count)
			}
			_, objectErr := os.Lstat(f.objectPath(0))
			if directory == "objects" && objectErr != nil || directory == "staging" && !errors.Is(objectErr, os.ErrNotExist) {
				t.Fatal("unlink failure left an unexpected object state", directory, objectErr)
			}
			if err := os.Chmod(path, 0o700); err != nil {
				t.Fatal(err)
			}
			result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID)
			if err != nil || !result.StageRemoved || result.ObjectRemoved != (directory == "objects") || result.Referenced {
				t.Fatal("filesystem cleanup could not resume", result, err)
			}
			f.requireOtherPendingIntact(t)
		})
	}
}

func TestMediaRecoveryReconcilesDeferredCommitFailureAfterUnlinks(t *testing.T) {
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	execSQL(t, f.store.db, "CREATE TABLE recovery_commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	execSQL(t, f.store.db, `CREATE TRIGGER recovery_commit_failure AFTER DELETE ON media_pending_objects
 BEGIN INSERT INTO recovery_commit_guard VALUES('missing'); END`)
	if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); err == nil || result != (mediaRecoveryResult{}) {
		t.Fatal("a failed deferred COMMIT returned success", result, err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM recovery_commit_guard"); count != 0 {
		t.Fatal("failed COMMIT retained a late write", count)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", f.pending[0].ID); count != 1 {
		t.Fatal("failed COMMIT lost the original pending row", count)
	}
	for _, path := range []string{f.stagePath(0), f.objectPath(0)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed COMMIT did not follow durable file cleanup", path, err)
		}
	}
	execSQL(t, f.store.db, "DROP TRIGGER recovery_commit_failure")
	result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID)
	if err != nil || result.StageRemoved || result.ObjectRemoved || result.Referenced {
		t.Fatal("original pending row could not reconcile the uncertain COMMIT", result, err)
	}
	f.requireOtherPendingIntact(t)
}

func TestMediaRecoveryWaitsForActiveGuardedUpload(t *testing.T) {
	f := newRecoveryFixture(t)
	clock := f.auth.now
	calls := 0
	entered, resume := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resume) }) }
	defer release()
	f.auth.now = func() time.Time {
		calls++
		if calls == 6 {
			close(entered)
			<-resume
		}
		return clock()
	}
	type uploadOutcome struct {
		value Media
		err   error
	}
	uploaded := make(chan uploadOutcome, 1)
	uploadDone := false
	defer func() {
		release()
		if !uploadDone {
			select {
			case <-uploaded:
			case <-time.After(5 * time.Second):
				t.Error("upload did not stop during cleanup")
			}
		}
	}()
	raw := privateUploadPNG(t, "concurrent upload")
	go func() {
		value, err := f.storage.upload(t.Context(), f.library, f.token, "recovery-upload-key", "image/png", raw)
		uploaded <- uploadOutcome{value, err}
	}()
	select {
	case <-entered:
	case result := <-uploaded:
		uploadDone = true
		t.Fatal("upload ended before reaching the protected transaction", result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not reach the protected transaction")
	}
	type recoveryOutcome struct {
		value mediaRecoveryResult
		err   error
	}
	recovered := make(chan recoveryOutcome, 1)
	recoveryDone := false
	defer func() {
		release()
		if !recoveryDone {
			select {
			case <-recovered:
			case <-time.After(5 * time.Second):
				t.Error("recovery did not stop during cleanup")
			}
		}
	}()
	go func() {
		value, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID)
		recovered <- recoveryOutcome{value, err}
	}()
	select {
	case result := <-recovered:
		recoveryDone = true
		t.Fatal("recovery ran while a guarded upload was active", result.err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case result := <-uploaded:
		uploadDone = true
		if result.err != nil || result.value.ID == "" {
			t.Fatal("guarded upload failed before recovery", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guarded upload did not finish")
	}
	f.auth.now = clock
	select {
	case result := <-recovered:
		recoveryDone = true
		if result.err != nil || !result.value.StageRemoved || result.value.ObjectRemoved || result.value.Referenced {
			t.Fatal("recovery did not resume after the guarded upload", result.value, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not resume")
	}
	f.requireOtherPendingIntact(t)
}

func TestMediaRecoveryResumesAfterInterruptedRecoveryProcess(t *testing.T) {
	const databaseEnv = "HOMEBOX_MEDIA_RECOVERY_INTERRUPTED_TEST_DB"
	if path := os.Getenv(databaseEnv); path != "" {
		ctx := context.Background()
		idBytes, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			t.Fatal(err)
		}
		s, err := Open(ctx, Config{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		storage, err := s.openMediaStorage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if result, err := storage.recoverPendingMedia(ctx, string(idBytes)); err == nil || result != (mediaRecoveryResult{}) {
			t.Fatal("unwritable staging unexpectedly completed recovery", result, err)
		}
		// Exit without closing either store so the next process must reconcile.
		os.Exit(0)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can bypass private-directory write permissions")
	}
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	path := f.store.path
	stagePath, objectPath := f.stagePath(0), f.objectPath(0)
	staging := filepath.Join(f.storage.path, "staging")
	if err := os.Chmod(staging, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(staging, 0o700); err != nil {
			t.Error(err)
		}
	}()
	if err := errors.Join(f.storage.Close(), f.store.Close()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMediaRecoveryResumesAfterInterruptedRecoveryProcess$")
	cmd.Env = append(os.Environ(), databaseEnv+"="+path)
	cmd.Stdin = strings.NewReader(f.pending[0].ID)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("interrupted recovery helper failed: %v %s", err, output)
	}
	if _, err := os.Lstat(objectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted recovery did not remove the unreferenced object", err)
	}
	if _, err := os.Lstat(stagePath); err != nil {
		t.Fatal("interrupted recovery lost the retryable stage", err)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	storage, err := reopened.openMediaStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	result, err := storage.recoverPendingMedia(t.Context(), f.pending[0].ID)
	if err != nil || !result.StageRemoved || result.ObjectRemoved || result.Referenced {
		t.Fatal("another process could not finish interrupted recovery", result, err)
	}
	if count := scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 1 {
		t.Fatal("interrupted recovery changed another pending object", count)
	}
}

func TestMediaRecoveryHandlesAbruptProducerExit(t *testing.T) {
	const databaseEnv = "HOMEBOX_MEDIA_RECOVERY_EXIT_TEST_DB"
	const clockEnv = "HOMEBOX_MEDIA_RECOVERY_EXIT_TEST_CLOCK"
	if path := os.Getenv(databaseEnv); path != "" {
		ctx := context.Background()
		tokenBytes, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			t.Fatal(err)
		}
		s, err := Open(ctx, Config{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		auth, err := s.NewAuth(AuthConfig{})
		if err != nil {
			t.Fatal(err)
		}
		now, err := time.Parse(time.RFC3339Nano, os.Getenv(clockEnv))
		if err != nil {
			t.Fatal(err)
		}
		auth.now = func() time.Time { return now }
		library, err := NewMediaLibrary(auth)
		if err != nil {
			t.Fatal(err)
		}
		storage, err := s.openMediaStorage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		stages := make([]*mediaStage, 0, 2)
		for _, value := range []string{"abrupt original", "abrupt thumbnail"} {
			stage, err := newMediaStage(ctx, filepath.Join(storage.path, "staging"), 1024)
			if err == nil {
				_, err = stage.Write([]byte(value))
			}
			if err == nil {
				_, err = stage.seal()
			}
			if err != nil {
				t.Fatal(err)
			}
			stages = append(stages, stage)
		}
		pending, err := library.journalMediaObjects(ctx, string(tokenBytes), stages, storage.objects)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = stages[0].promoteTo(storage.objects); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, pending[0].ID)
		// Simulate process loss without running any deferred cleanup.
		os.Exit(0)
	}

	s, _, now, _, token := completedAuthFixture(t)
	storage := testMediaStorage(t, s)
	path := s.path
	if err := errors.Join(storage.Close(), s.Close()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMediaRecoveryHandlesAbruptProducerExit$")
	cmd.Env = append(os.Environ(), databaseEnv+"="+path, clockEnv+"="+now.Format(time.RFC3339Nano))
	cmd.Stdin = strings.NewReader(token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("abrupt producer failed: %v %s", err, output)
	}
	id := strings.TrimSpace(string(output))
	if len(id) != 36 || strings.ContainsAny(id, "\r\n") || !validDomainID(id) {
		t.Fatal("abrupt producer returned an invalid pending identity")
	}
	reopened, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	owner, err := reopened.openMediaStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	pending, err := loadMediaPendingRow(t.Context(), reopened.db, id)
	if err != nil {
		t.Fatal(err)
	}
	stagePath := filepath.Join(owner.path, "staging", pending.StageName)
	objectPath := filepath.Join(owner.path, "objects", "sha256-"+pending.SHA256)
	stageInfo, stageErr := os.Stat(stagePath)
	objectInfo, objectErr := os.Stat(objectPath)
	if stageErr != nil || objectErr != nil || !os.SameFile(stageInfo, objectInfo) {
		t.Fatal("abrupt producer did not leave the actual linked stage and object", stageErr, objectErr)
	}
	result, err := owner.recoverPendingMedia(t.Context(), id)
	if err != nil || !result.StageRemoved || !result.ObjectRemoved || result.Referenced {
		t.Fatal("recovery after producer exit failed", result, err)
	}
	for _, file := range []string{stagePath, objectPath} {
		if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("recovered file remains", err)
		}
	}
	if count := scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 1 {
		t.Fatal("recovery after producer exit changed another pending object", count)
	}
}

func TestMediaRecoveryPageValidatesLookaheadAndExplicitContinuations(t *testing.T) {
	f := newRecoveryFixture(t)
	ids := []string{f.pending[0].ID, f.pending[1].ID}
	sort.Strings(ids)
	first, next, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 1)
	if err != nil || len(first) != 1 || first[0] != ids[0] || next != ids[0] {
		t.Fatal("first pending recovery page was incorrect", first, next, err)
	}
	last, next, err := f.storage.pendingMediaRecoveryPage(t.Context(), next, 1)
	if err != nil || len(last) != 1 || last[0] != ids[1] || next != "" {
		t.Fatal("terminal pending recovery page was incorrect", last, next, err)
	}
	for _, limit := range []int{0, -1, 101} {
		if page, cursor, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", limit); !errors.Is(err, ErrDomainInput) || page != nil || cursor != "" {
			t.Fatal("invalid recovery page limit was accepted", limit, page, cursor, err)
		}
	}
	if page, cursor, err := f.storage.pendingMediaRecoveryPage(t.Context(), string([]byte{0xff}), 1); !errors.Is(err, ErrDomainInput) || page != nil || cursor != "" {
		t.Fatal("invalid recovery continuation was accepted", page, cursor, err)
	}
	execSQL(t, f.store.db, "DROP TRIGGER media_pending_objects_no_update")
	execSQL(t, f.store.db, "UPDATE media_pending_objects SET stage_name='../outside' WHERE id=?", ids[1])
	if page, cursor, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 1); err == nil || page != nil || cursor != "" {
		t.Fatal("invalid lookahead returned a partial recovery page", page, cursor, err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("page validation changed pending rows", count)
	}
}

func TestMediaRecoveryPageContinuesAfterRemovalAndKeepsExactIDs(t *testing.T) {
	f := newRecoveryFixture(t)
	execSQL(t, f.store.db, "DROP TRIGGER media_pending_objects_no_update")
	for index, id := range []string{"a\x00b", "a\x00c"} {
		execSQL(t, f.store.db, "UPDATE media_pending_objects SET id=? WHERE id=?", id, f.pending[index].ID)
	}
	first, next, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 1)
	if err != nil || len(first) != 1 || first[0] != "a\x00b" || next != "a\x00b" {
		t.Fatal("exact first pending identity was changed", first, next, err)
	}
	if _, err = f.storage.recoverPendingMedia(t.Context(), first[0]); err != nil {
		t.Fatal("could not recover the selected page identity", err)
	}
	last, next, err := f.storage.pendingMediaRecoveryPage(t.Context(), next, 1)
	if err != nil || len(last) != 1 || last[0] != "a\x00c" || next != "" {
		t.Fatal("continuation changed identity or depended on the removed row", last, next, err)
	}
}

func TestMediaRecoveryPageRejectsClosedAndCanceledStorage(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if page, next, err := f.storage.pendingMediaRecoveryPage(ctx, "", 1); !errors.Is(err, context.Canceled) || page != nil || next != "" {
		t.Fatal("canceled recovery page returned data", page, next, err)
	}
	if err := f.storage.Close(); err != nil {
		t.Fatal(err)
	}
	if page, next, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 1); !errors.Is(err, errMediaStorageClosed) || page != nil || next != "" {
		t.Fatal("closed storage returned a recovery page", page, next, err)
	}
	if result, err := f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); !errors.Is(err, errMediaStorageClosed) || result != (mediaRecoveryResult{}) {
		t.Fatal("closed storage accepted recovery", result, err)
	}
}

func TestMediaRecoveryRejectsInvalidContextAndPendingIdentity(t *testing.T) {
	for _, storage := range []*mediaStorage{nil, {}, {store: &Store{}}} {
		if result, err := storage.recoverPendingMedia(t.Context(), "valid-id"); !errors.Is(err, ErrDomainInput) || result != (mediaRecoveryResult{}) {
			t.Fatal("invalid storage was accepted", result, err)
		}
		if page, next, err := storage.pendingMediaRecoveryPage(t.Context(), "", 1); !errors.Is(err, ErrDomainInput) || page != nil || next != "" {
			t.Fatal("invalid storage returned a recovery page", page, next, err)
		}
	}
	f := newRecoveryFixture(t)
	if result, err := f.storage.recoverPendingMedia(nil, f.pending[0].ID); !errors.Is(err, ErrDomainInput) || result != (mediaRecoveryResult{}) {
		t.Fatal("nil context was accepted", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := f.storage.recoverPendingMedia(ctx, f.pending[0].ID); !errors.Is(err, context.Canceled) || result != (mediaRecoveryResult{}) {
		t.Fatal("canceled recovery changed state", result, err)
	}
	if result, err := f.storage.recoverPendingMedia(t.Context(), "missing"); !errors.Is(err, ErrNotFound) || result != (mediaRecoveryResult{}) {
		t.Fatal("missing pending identity was accepted", result, err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("rejected recovery changed pending rows", count)
	}
	for index := range f.pending {
		if _, err := os.Lstat(f.stagePath(index)); err != nil {
			t.Fatal("rejected recovery removed a stage", err)
		}
	}
}
