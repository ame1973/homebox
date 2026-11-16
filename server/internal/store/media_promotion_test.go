package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"homebox/internal/mediaimage"

	"golang.org/x/sys/unix"
)

func mediaPromotionDirs(t *testing.T) (string, string) {
	t.Helper()
	root := privateTempDir(t)
	staging, objects := filepath.Join(root, "staging"), filepath.Join(root, "objects")
	for _, dir := range []string{staging, objects} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return staging, objects
}

func sealedMediaStage(t *testing.T, directory string, data []byte) *mediaStage {
	t.Helper()
	stage, err := newMediaStage(t.Context(), directory, int64(len(data))+1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stage.discard(); err != nil {
			t.Errorf("discard stage: %v", err)
		}
	})
	if _, err := stage.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.seal(); err != nil {
		t.Fatal(err)
	}
	return stage
}

func TestMediaPromotionCreatesReadOnlyObjectAndDeduplicates(t *testing.T) {
	staging, objects := mediaPromotionDirs(t)
	data := []byte("sanitized media bytes")
	digest := sha256.Sum256(data)
	first := sealedMediaStage(t, staging, data)
	result, err := first.promote(objects)
	if err != nil || result.key != "sha256-"+hex.EncodeToString(digest[:]) || result.digest.SHA256 != digest || result.digest.Bytes != int64(len(data)) || result.reused {
		t.Fatalf("invalid new object: %+v %v", result, err)
	}
	objectPath := filepath.Join(objects, result.key)
	stageInfo, err := os.Stat(filepath.Join(staging, first.name))
	if err != nil {
		t.Fatal(err)
	}
	objectInfo, err := os.Lstat(objectPath)
	if err != nil || !objectInfo.Mode().IsRegular() || objectInfo.Mode().Perm() != 0o400 || !os.SameFile(stageInfo, objectInfo) {
		t.Fatalf("promoted object is not the private read-only staged inode: %v %v", objectInfo, err)
	}
	if repeat, err := first.promote(objects); err != nil || !repeat.reused || repeat.key != result.key {
		t.Fatalf("same-stage promotion did not safely reuse the object: %+v %v", repeat, err)
	}
	if err := first.discard(); err != nil {
		t.Fatal(err)
	}
	if actual, err := os.ReadFile(objectPath); err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("discard removed or changed the object: %q %v", actual, err)
	}
	second := sealedMediaStage(t, staging, data)
	again, err := second.promote(objects)
	if err != nil || !again.reused || again.key != result.key || again.digest != result.digest {
		t.Fatalf("same bytes were not safely reused: %+v %v", again, err)
	}
	after, err := os.Stat(objectPath)
	if err != nil || !os.SameFile(objectInfo, after) {
		t.Fatalf("deduplication replaced an existing object: %v", err)
	}
	third := sealedMediaStage(t, staging, []byte("different sanitized bytes"))
	other, err := third.promote(objects)
	if err != nil || other.reused || other.key == result.key {
		t.Fatalf("different bytes were incorrectly deduplicated: %+v %v", other, err)
	}
}

func TestMediaPromotionReusesExistingValidPrivateObjectWithoutModifyingIt(t *testing.T) {
	staging, objects := mediaPromotionDirs(t)
	data := []byte("retained valid object")
	stage := sealedMediaStage(t, staging, data)
	digest := sha256.Sum256(data)
	path := filepath.Join(objects, mediaObjectKey(digest))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := stage.promote(objects)
	if err != nil || !result.reused {
		t.Fatalf("valid existing object was not reused: %+v %v", result, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm() != 0o600 {
		t.Fatalf("reuse changed the existing object: %v %v", after, err)
	}
}

func TestMediaPromotionRejectsUnsealedClosedCanceledAndUnsafeDirectories(t *testing.T) {
	staging, objects := mediaPromotionDirs(t)
	stage, err := newMediaStage(t.Context(), staging, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.discard()
	if _, err := stage.Write([]byte("valid")); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.promote(objects); !errors.Is(err, errMediaStageUnsealed) {
		t.Fatalf("unsealed stage was promoted: %v", err)
	}
	if _, err := stage.seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.promote(staging); !errors.Is(err, errMediaObjectDirectory) {
		t.Fatalf("staging directory accepted as object directory: %v", err)
	}
	missing := filepath.Join(filepath.Dir(objects), "missing")
	if _, err := stage.promote(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing object directory accepted: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("promotion created an object directory: %v", err)
	}
	link := filepath.Join(filepath.Dir(objects), "object-link")
	if err := os.Symlink(objects, link); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.promote(link); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink object directory accepted: %v", err)
	}
	if err := os.Chmod(objects, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.promote(objects); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("permissive object directory accepted: %v", err)
	}
	if err := os.Chmod(objects, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	canceled, err := newMediaStage(ctx, staging, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer canceled.discard()
	if _, err := canceled.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	if _, err := canceled.seal(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := canceled.promote(objects); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stage was promoted: %v", err)
	}
	if err := stage.discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.promote(objects); !errors.Is(err, errMediaStageClosed) {
		t.Fatalf("discarded stage was promoted: %v", err)
	}
	entries, err := os.ReadDir(objects)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refused promotions created an object: %v %v", entries, err)
	}
}

func TestMediaPromotionRejectsCrossDeviceBeforeChangingStage(t *testing.T) {
	staging, _ := mediaPromotionDirs(t)
	objects, err := os.MkdirTemp("/dev/shm", "homebox-media-promotion-")
	if err != nil {
		t.Skipf("a writable secondary filesystem is unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(objects); err != nil {
			t.Errorf("remove test object directory: %v", err)
		}
	})
	var source, destination unix.Stat_t
	if err := unix.Stat(staging, &source); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(objects, &destination); err != nil {
		t.Fatal(err)
	}
	if source.Dev == destination.Dev {
		t.Skip("the test directories use the same filesystem")
	}
	data := []byte("cross-device stage")
	stage := sealedMediaStage(t, staging, data)
	if result, err := stage.promote(objects); !errors.Is(err, unix.EXDEV) || result != (promotedMedia{}) {
		t.Fatalf("cross-device promotion was not rejected: %+v %v", result, err)
	}
	info, err := os.Stat(filepath.Join(staging, stage.name))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cross-device rejection changed the stage: %v %v", info, err)
	}
	entries, err := os.ReadDir(objects)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cross-device rejection created an object: %v %v", entries, err)
	}
	path := filepath.Join(objects, mediaObjectKey(sha256.Sum256(data)))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := stage.promote(objects); !errors.Is(err, unix.EXDEV) || result != (promotedMedia{}) {
		t.Fatalf("cross-device reuse was not rejected: %+v %v", result, err)
	}
	info, err = os.Stat(filepath.Join(staging, stage.name))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cross-device reuse changed the stage: %v %v", info, err)
	}
	if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("cross-device rejection changed the existing object: %q %v", actual, err)
	}
}

type cancelWhenObjectAppears struct {
	context.Context
	path   string
	cancel context.CancelFunc
}

func (ctx *cancelWhenObjectAppears) Err() error {
	if _, err := os.Lstat(ctx.path); err == nil {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestMediaPromotionRetainsObjectAfterCancellationFollowingLink(t *testing.T) {
	staging, objects := mediaPromotionDirs(t)
	data := []byte("canceled after filesystem promotion")
	stage := sealedMediaStage(t, staging, data)
	digest := sha256.Sum256(data)
	path := filepath.Join(objects, mediaObjectKey(digest))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stage.ctx = &cancelWhenObjectAppears{Context: ctx, path: path, cancel: cancel}
	result, err := stage.promote(objects)
	if !errors.Is(err, context.Canceled) || result != (promotedMedia{}) {
		t.Fatalf("canceled promotion returned a result: %+v %v", result, err)
	}
	value, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(value, data) {
		t.Fatalf("canceled promotion removed or changed the object: %q %v", value, err)
	}
	if err := stage.discard(); err != nil {
		t.Fatal(err)
	}
	value, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(value, data) {
		t.Fatalf("stage cleanup removed the possibly shared object: %q %v", value, err)
	}
}

func TestMediaPromotionRejectsChangedStageWithoutPublishing(t *testing.T) {
	for _, alteration := range []string{"bytes", "permissions", "replacement", "symlink", "missing"} {
		t.Run(alteration, func(t *testing.T) {
			staging, objects := mediaPromotionDirs(t)
			stage := sealedMediaStage(t, staging, []byte("original"))
			path := filepath.Join(staging, stage.name)
			switch alteration {
			case "bytes":
				if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "replacement", "symlink":
				if err := os.Rename(path, path+".moved"); err != nil {
					t.Fatal(err)
				}
				if alteration == "replacement" {
					err := os.WriteFile(path, []byte("original"), 0o600)
					if err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(path+".moved", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := stage.promote(objects); !errors.Is(err, errMediaStageChanged) {
				t.Fatalf("changed stage promoted: %v", err)
			}
			entries, err := os.ReadDir(objects)
			if err != nil || len(entries) != 0 {
				t.Fatalf("changed stage created object: %v %v", entries, err)
			}
			if alteration == "replacement" || alteration == "symlink" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMediaPromotionNeverReplacesInvalidExistingObject(t *testing.T) {
	for _, kind := range []string{"corrupt", "permissive", "owner-executable", "symlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			staging, objects := mediaPromotionDirs(t)
			data := []byte("sanitized")
			stage := sealedMediaStage(t, staging, data)
			digest := sha256.Sum256(data)
			path := filepath.Join(objects, "sha256-"+hex.EncodeToString(digest[:]))
			target := filepath.Join(filepath.Dir(objects), "unrelated")
			if err := os.WriteFile(target, []byte("do not change"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "corrupt", "permissive", "owner-executable":
				value, mode := []byte("different"), os.FileMode(0o400)
				if kind == "permissive" {
					value, mode = data, 0o644
				}
				if kind == "owner-executable" {
					value, mode = data, 0o700
				}
				if err := os.WriteFile(path, value, mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stage.promote(objects); !errors.Is(err, errMediaObjectInvalid) {
				t.Fatalf("invalid existing object accepted: %v", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("existing object was replaced: %v", err)
			}
			if value, err := os.ReadFile(target); err != nil || string(value) != "do not change" {
				t.Fatalf("unrelated file changed: %q %v", value, err)
			}
		})
	}
}

func TestMediaPromotionRejectsWritableStageWithAdditionalHardlinks(t *testing.T) {
	staging, objects := mediaPromotionDirs(t)
	stage := sealedMediaStage(t, staging, []byte("shared private file"))
	alias := filepath.Join(filepath.Dir(staging), "unrelated-hardlink")
	if err := os.Link(filepath.Join(staging, stage.name), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.promote(objects); !errors.Is(err, errMediaStageChanged) {
		t.Fatalf("writable stage with another hardlink was promoted: %v", err)
	}
	info, err := os.Stat(alias)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("promotion changed the unrelated hardlink: %v %v", info, err)
	}
	entries, err := os.ReadDir(objects)
	if err != nil || len(entries) != 0 {
		t.Fatalf("hardlink rejection published an object: %v %v", entries, err)
	}
}

func TestMediaObjectVerificationRejectsNoncanonicalKeys(t *testing.T) {
	_, objects := mediaPromotionDirs(t)
	data := []byte("private content")
	digest := sha256.Sum256(data)
	expected := mediaStageDigest{SHA256: digest, Bytes: int64(len(data))}
	outside := filepath.Join(filepath.Dir(objects), "outside")
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objects, "arbitrary"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := openPrivateMediaDirectory(objects)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	for _, key := range []string{"../outside", "arbitrary", "/etc/passwd", "", "sha256-" + strings.Repeat("A", 64), mediaObjectKey(sha256.Sum256([]byte("different")))} {
		if err := verifyMediaObject(t.Context(), dir, key, expected); !errors.Is(err, errMediaObjectInvalid) {
			t.Fatalf("noncanonical key %q accepted: %v", key, err)
		}
	}
	if err := verifyMediaObject(nil, dir, mediaObjectKey(digest), expected); !errors.Is(err, errMediaObjectInvalid) {
		t.Fatalf("nil context accepted: %v", err)
	}
	for _, size := range []int64{-1, 0, maxMediaStageBytes + 1} {
		invalid := expected
		invalid.Bytes = size
		if err := verifyMediaObject(t.Context(), dir, mediaObjectKey(digest), invalid); !errors.Is(err, errMediaObjectInvalid) {
			t.Fatalf("invalid object size %d accepted: %v", size, err)
		}
	}
}

func TestMediaObjectVerificationRequiresPrivateDirectoryAtReadTime(t *testing.T) {
	_, objects := mediaPromotionDirs(t)
	data := []byte("actual object")
	digest := sha256.Sum256(data)
	expected := mediaStageDigest{SHA256: digest, Bytes: int64(len(data))}
	if err := os.WriteFile(filepath.Join(objects, mediaObjectKey(digest)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := openPrivateMediaDirectory(objects)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := verifyMediaObject(t.Context(), dir, mediaObjectKey(digest), expected); err != nil {
		t.Fatalf("valid object was rejected: %v", err)
	}
	if err := os.Chmod(objects, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(objects, 0o700)
	if err := verifyMediaObject(t.Context(), dir, mediaObjectKey(digest), expected); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("object read through permissive directory was accepted: %v", err)
	}
}

func TestMediaObjectVerificationUsesAnchoredDirectoryAfterRename(t *testing.T) {
	_, objects := mediaPromotionDirs(t)
	data := []byte("actual object")
	digest := sha256.Sum256(data)
	expected := mediaStageDigest{SHA256: digest, Bytes: int64(len(data))}
	key := mediaObjectKey(digest)
	if err := os.WriteFile(filepath.Join(objects, key), data, 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := openPrivateMediaDirectory(objects)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	moved := objects + "-moved"
	if err := os.Rename(objects, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(objects, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objects, key), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyMediaObject(t.Context(), dir, key, expected); err != nil {
		t.Fatalf("verification used the replacement directory: %v", err)
	}
	if value, err := os.ReadFile(filepath.Join(objects, key)); err != nil || string(value) != "replacement" {
		t.Fatalf("verification changed replacement bytes: %q %v", value, err)
	}
}

func TestMediaPromotionSerializesAtomicDeduplication(t *testing.T) {
	staging, objects := mediaPromotionDirs(t)
	const count = 16
	stages := make([]*mediaStage, count)
	for index := range stages {
		stages[index] = sealedMediaStage(t, staging, []byte("shared sanitized image"))
	}
	results, failures := make([]promotedMedia, count), make([]error, count)
	var group sync.WaitGroup
	for index := range count {
		group.Go(func() { results[index], failures[index] = stages[index].promote(objects) })
	}
	group.Wait()
	created := 0
	for index, result := range results {
		if failures[index] != nil || result.key != results[0].key || result.digest != results[0].digest {
			t.Fatalf("inconsistent concurrent promotion: %+v %v", result, failures[index])
		}
		if !result.reused {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("expected one created object, got %d", created)
	}
	entries, err := os.ReadDir(objects)
	if err != nil || len(entries) != 1 || entries[0].Name() != results[0].key {
		t.Fatalf("unexpected concurrent object set: %v %v", entries, err)
	}
	for _, stage := range stages {
		if err := stage.discard(); err != nil {
			t.Fatal(err)
		}
	}
	if value, err := os.ReadFile(filepath.Join(objects, results[0].key)); err != nil || string(value) != "shared sanitized image" {
		t.Fatalf("staging cleanup damaged shared object: %q %v", value, err)
	}
}

func TestMediaPromotionSurvivesAbruptProcessExit(t *testing.T) {
	const helper = "HOMEBOX_MEDIA_PROMOTION_EXIT_TEST"
	if os.Getenv(helper) == "1" {
		stage, err := newMediaStage(context.Background(), os.Getenv("HOMEBOX_MEDIA_STAGE_TEST_DIR"), 128)
		if err == nil {
			_, err = stage.Write([]byte("sanitized process payload"))
		}
		if err == nil {
			_, err = stage.seal()
		}
		var result promotedMedia
		if err == nil {
			result, err = stage.promote(os.Getenv("HOMEBOX_MEDIA_OBJECT_TEST_DIR"))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "promotion helper failed:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, stage.name, result.key)
		os.Exit(0)
	}
	staging, objects := mediaPromotionDirs(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMediaPromotionSurvivesAbruptProcessExit$")
	cmd.Env = append(os.Environ(), helper+"=1", "HOMEBOX_MEDIA_STAGE_TEST_DIR="+staging, "HOMEBOX_MEDIA_OBJECT_TEST_DIR="+objects)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("promotion helper exited unsuccessfully: %v %s", err, output)
	}
	parts := strings.Fields(string(output))
	if len(parts) != 2 || !regexp.MustCompile(`^media-[a-f0-9]{32}\.partial$`).MatchString(parts[0]) || !regexp.MustCompile(`^sha256-[a-f0-9]{64}$`).MatchString(parts[1]) {
		t.Fatalf("invalid promoted names: %q", output)
	}
	stageInfo, err := os.Stat(filepath.Join(staging, parts[0]))
	if err != nil {
		t.Fatal(err)
	}
	objectInfo, err := os.Stat(filepath.Join(objects, parts[1]))
	if err != nil || !os.SameFile(stageInfo, objectInfo) || objectInfo.Mode().Perm() != 0o400 {
		t.Fatalf("abrupt exit lost promoted object: %v %v", objectInfo, err)
	}
	if value, err := os.ReadFile(filepath.Join(objects, parts[1])); err != nil || string(value) != "sanitized process payload" {
		t.Fatalf("promoted bytes changed after exit: %q %v", value, err)
	}
}

func TestMediaPromotionAcceptsBothSanitizerOutputs(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 222, G: 23, B: 50, A: 255})
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatal(err)
	}
	staging, objects := mediaPromotionDirs(t)
	original, err := newMediaStage(t.Context(), staging, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer original.discard()
	thumbnail, err := newMediaStage(t.Context(), staging, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer thumbnail.discard()
	fullResult, thumbResult, err := mediaimage.SanitizeWithThumbnail(t.Context(), raw.Bytes(), "image/png", original, thumbnail, mediaimage.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := original.seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := thumbnail.seal(); err != nil {
		t.Fatal(err)
	}
	full, err := original.promote(objects)
	if err != nil || full.digest.SHA256 != fullResult.SHA256 {
		t.Fatalf("invalid original: %+v %v", full, err)
	}
	thumb, err := thumbnail.promote(objects)
	if err != nil || thumb.digest.SHA256 != thumbResult.SHA256 || thumb.key == full.key {
		t.Fatalf("invalid thumbnail: %+v %v", thumb, err)
	}
	pngFile, err := os.Open(filepath.Join(objects, full.key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(pngFile); err != nil {
		t.Fatal(err)
	}
	if err := pngFile.Close(); err != nil {
		t.Fatal(err)
	}
	jpegFile, err := os.Open(filepath.Join(objects, thumb.key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(jpegFile); err != nil {
		t.Fatal(err)
	}
	if err := jpegFile.Close(); err != nil {
		t.Fatal(err)
	}
}

func FuzzMediaPromotion(f *testing.F) {
	f.Add([]byte("sanitized\x00bytes"), false)
	f.Add([]byte("shared"), true)
	f.Fuzz(func(t *testing.T, data []byte, reuse bool) {
		if len(data) == 0 || len(data) > 8192 {
			t.Skip()
		}
		staging, objects := mediaPromotionDirs(t)
		stage := sealedMediaStage(t, staging, data)
		digest := sha256.Sum256(data)
		key := mediaObjectKey(digest)
		path := filepath.Join(objects, key)
		if reuse {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		result, err := stage.promote(objects)
		if err != nil || result.key != key || result.reused != reuse || result.digest.SHA256 != digest || result.digest.Bytes != int64(len(data)) {
			t.Fatalf("wrong promotion: %+v %v", result, err)
		}
		if err := stage.discard(); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, data) {
			t.Fatalf("promotion did not retain bytes: %v", err)
		}
		dir, err := openPrivateMediaDirectory(objects)
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		if err := verifyMediaObject(t.Context(), dir, key, result.digest); err != nil {
			t.Fatalf("valid object refused: %v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		actual[0] ^= 0xff
		if err := os.WriteFile(path, actual, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyMediaObject(t.Context(), dir, key, result.digest); !errors.Is(err, errMediaObjectInvalid) {
			t.Fatalf("tampered object accepted: %v", err)
		}
	})
}
