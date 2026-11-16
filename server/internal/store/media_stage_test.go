package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"homebox/internal/mediaimage"
)

func TestMediaStageNamesRejectTraversalAndAmbiguousBytes(t *testing.T) {
	valid := "media-" + strings.Repeat("a1", 16) + ".partial"
	if !validMediaStageName(valid) {
		t.Fatal("generated stage spelling was rejected")
	}
	for _, name := range []string{"", valid + "\x00", "../" + valid, "media-" + strings.Repeat("A", 32) + ".partial", valid + "/other", "media-" + strings.Repeat("g", 32) + ".partial"} {
		if validMediaStageName(name) {
			t.Fatal("unsafe stage spelling was accepted", name)
		}
	}
}

func TestMediaStagePreservesOnlySealedJournaledFiles(t *testing.T) {
	directory := privateTempDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	stage, err := newMediaStage(ctx, directory, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = stage.preserve(); !errors.Is(err, errMediaStageUnsealed) {
		t.Fatal("unsealed stage was preserved", err)
	}
	data := []byte("sealed private bytes")
	if _, err = stage.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err = stage.seal(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, stage.name)
	cancel()
	if err = stage.preserve(); err != nil {
		t.Fatal("cancellation prevented safe preservation", err)
	}
	if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, data) {
		t.Fatal("preservation removed or changed the stage", err)
	}
	if err = stage.discard(); !errors.Is(err, errMediaStageClosed) {
		t.Fatal("a detached stage was removed without recovery validation", err)
	}
}

func TestMediaStagePreserveRejectsPermissiveFile(t *testing.T) {
	directory := privateTempDir(t)
	stage, err := newMediaStage(t.Context(), directory, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stage.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	if _, err = stage.seal(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, stage.name)
	if err = os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err = stage.preserve(); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("permissive stage preservation reported success", err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "private" {
		t.Fatal("failed preservation removed or changed the stage", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMediaStagePrivateBoundedSealAndDiscard(t *testing.T) {
	directory := privateTempDir(t)
	existing := filepath.Join(directory, "unrelated")
	if err := os.WriteFile(existing, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage, err := newMediaStage(t.Context(), directory, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.discard()
	path := filepath.Join(directory, stage.name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("stage is not a private regular file: %v %v", info, err)
	}
	for _, part := range []string{"private ", "sanitized ", "bytes"} {
		if n, err := stage.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatalf("stage write: %d %v", n, err)
		}
	}
	want := []byte("private sanitized bytes")
	sealed, err := stage.seal()
	if err != nil || sealed.Bytes != int64(len(want)) || sealed.SHA256 != sha256.Sum256(want) {
		t.Fatalf("incorrect sealed bytes or digest: %+v %v", sealed, err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatalf("sealed stage differs from actual bytes: %v", err)
	}
	if _, err := stage.Write([]byte("late")); !errors.Is(err, errMediaStageClosed) {
		t.Fatalf("sealed stage accepted a write: %v", err)
	}
	if again, err := stage.seal(); err != nil || again != sealed {
		t.Fatalf("sealed metadata was not stable: %+v %v", again, err)
	}
	if err := stage.discard(); err != nil {
		t.Fatal(err)
	}
	if err := stage.discard(); err != nil {
		t.Fatalf("discard was not idempotent: %v", err)
	}
	if _, err := stage.seal(); !errors.Is(err, errMediaStageClosed) {
		t.Fatalf("discarded stage was sealed: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discard retained its stage: %v", err)
	}
	if content, err := os.ReadFile(existing); err != nil || string(content) != "do not touch" {
		t.Fatalf("discard changed another file: %q %v", content, err)
	}
}

func TestMediaStageRejectsUnsafeLimitsDirectoriesAndCancellation(t *testing.T) {
	parent := privateTempDir(t)
	for _, limit := range []int64{-1, 0, maxMediaStageBytes + 1} {
		directory := filepath.Join(parent, "should-not-exist")
		if _, err := newMediaStage(t.Context(), directory, limit); !errors.Is(err, errMediaStageLimit) {
			t.Fatalf("limit %d accepted: %v", limit, err)
		}
		if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid limit created a directory: %v", err)
		}
	}
	largest, err := newMediaStage(t.Context(), parent, maxMediaStageBytes)
	if err != nil {
		t.Fatalf("maximum staging limit was rejected: %v", err)
	}
	if err := largest.discard(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	directory := filepath.Join(parent, "cancelled")
	if _, err := newMediaStage(ctx, directory, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request was not rejected: %v", err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled request created a directory: %v", err)
	}
	public := filepath.Join(parent, "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newMediaStage(t.Context(), public, 100); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("permissive directory accepted: %v", err)
	}
}

func TestMediaStageRequiresExistingNonSymlinkDirectory(t *testing.T) {
	parent := privateTempDir(t)
	missing := filepath.Join(parent, "missing")
	unexpected, err := newMediaStage(t.Context(), missing, 16)
	if unexpected != nil {
		t.Cleanup(func() { _ = unexpected.discard() })
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing staging directory accepted: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing staging directory was created: %v", err)
	}
	actual := filepath.Join(parent, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	unexpected, err = newMediaStage(t.Context(), link, 16)
	if unexpected != nil {
		t.Cleanup(func() { _ = unexpected.discard() })
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink staging directory accepted: %v", err)
	}
	entries, err := os.ReadDir(actual)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refused symlink created a stage: %v %v", entries, err)
	}
}

func TestMediaStageRejectsReplacementBeforeSealOrDiscard(t *testing.T) {
	for _, action := range []string{"seal", "discard"} {
		t.Run(action, func(t *testing.T) {
			directory := privateTempDir(t)
			stage, err := newMediaStage(t.Context(), directory, 32)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stage.Write([]byte("original")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, stage.name)
			if err := os.Rename(path, path+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("do not delete"), 0o600); err != nil {
				t.Fatal(err)
			}
			if action == "seal" {
				_, err = stage.seal()
			} else {
				err = stage.discard()
			}
			if !errors.Is(err, errMediaStageChanged) {
				t.Fatalf("replacement was not rejected: %v", err)
			}
			if value, err := os.ReadFile(path); err != nil || string(value) != "do not delete" {
				t.Fatalf("replacement was modified or removed: %q %v", value, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := stage.discard(); err != nil {
				t.Fatalf("discard after replacement removal: %v", err)
			}
		})
	}
}

func TestMediaStageAnchorsDirectoryAcrossRename(t *testing.T) {
	parent := privateTempDir(t)
	directory, moved := filepath.Join(parent, "stage"), filepath.Join(parent, "moved")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	stage, err := newMediaStage(t.Context(), directory, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.discard()
	if _, err := stage.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(directory, stage.name)
	if err := os.WriteFile(replacement, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	sealed, err := stage.seal()
	if err != nil || sealed.SHA256 != sha256.Sum256([]byte("private")) {
		t.Fatalf("renamed directory changed staged file: %+v %v", sealed, err)
	}
	if err := stage.discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, stage.name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original stage was not removed: %v", err)
	}
	if value, err := os.ReadFile(replacement); err != nil || string(value) != "unrelated" {
		t.Fatalf("replacement directory was changed: %q %v", value, err)
	}
}

func TestMediaStageRejectsSymlinkReplacementWithoutTouchingTarget(t *testing.T) {
	directory := privateTempDir(t)
	stage, err := newMediaStage(t.Context(), directory, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	path, target := filepath.Join(directory, stage.name), filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.seal(); !errors.Is(err, errMediaStageChanged) {
		t.Fatalf("symlink replacement was sealed: %v", err)
	}
	if err := stage.discard(); !errors.Is(err, errMediaStageChanged) {
		t.Fatalf("symlink replacement was removed: %v", err)
	}
	if value, err := os.ReadFile(target); err != nil || string(value) != "untouched" {
		t.Fatalf("symlink target changed: %q %v", value, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := stage.discard(); err != nil {
		t.Fatalf("discard after symlink removal: %v", err)
	}
}

func TestMediaStageWithholdsOverflowPartialWritesAndChangedBytes(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		stage, err := newMediaStage(t.Context(), privateTempDir(t), 4)
		if err != nil {
			t.Fatal(err)
		}
		defer stage.discard()
		if _, err := stage.Write([]byte("1234")); err != nil {
			t.Fatal(err)
		}
		if n, err := stage.Write([]byte("5")); n != 0 || !errors.Is(err, errMediaStageTooLarge) {
			t.Fatalf("overflow was not rejected without a prefix: %d %v", n, err)
		}
		if _, err := stage.seal(); !errors.Is(err, errMediaStageTooLarge) {
			t.Fatalf("overflowed stage was sealed: %v", err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		stage, err := newMediaStage(t.Context(), privateTempDir(t), 4)
		if err != nil {
			t.Fatal(err)
		}
		defer stage.discard()
		if _, err := stage.seal(); !errors.Is(err, errMediaStageEmpty) {
			t.Fatalf("empty stage was sealed: %v", err)
		}
	})
	t.Run("changed same-size bytes", func(t *testing.T) {
		stage, err := newMediaStage(t.Context(), privateTempDir(t), 16)
		if err != nil {
			t.Fatal(err)
		}
		defer stage.discard()
		if _, err := stage.Write([]byte("original")); err != nil {
			t.Fatal(err)
		}
		if _, err := stage.file.WriteAt([]byte("modified"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := stage.seal(); !errors.Is(err, errMediaStageChanged) {
			t.Fatalf("changed stage was sealed: %v", err)
		}
	})
	t.Run("failed file write", func(t *testing.T) {
		stage, err := newMediaStage(t.Context(), privateTempDir(t), 16)
		if err != nil {
			t.Fatal(err)
		}
		defer stage.discard()
		if err := stage.file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := stage.Write([]byte("broken")); err == nil {
			t.Fatal("closed stage accepted a write")
		}
		if _, err := stage.seal(); err == nil {
			t.Fatal("failed stage was sealed")
		}
	})
}

func TestMediaStageFailedDiscardCanBeRetriedWithoutReopeningTheStage(t *testing.T) {
	directory := privateTempDir(t)
	stage, err := newMediaStage(t.Context(), directory, 32)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, stage.name)
	if _, err := stage.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := stage.discard(); err == nil {
		t.Fatal("discard unexpectedly removed a directory")
	}
	if _, err := stage.seal(); !errors.Is(err, errMediaStageClosed) {
		t.Fatalf("failed discard left stage sealable: %v", err)
	}
	if _, err := stage.Write([]byte("late")); !errors.Is(err, errMediaStageClosed) {
		t.Fatalf("failed discard left stage writable: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := stage.discard(); err != nil {
		t.Fatalf("discard could not be retried: %v", err)
	}
}

func TestMediaStageRefusesPermissivePermissionsWithoutPreventingCleanup(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		directory := privateTempDir(t)
		stage, err := newMediaStage(t.Context(), directory, 32)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stage.Write([]byte("private")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(directory, 0o700)
		if _, err := stage.Write([]byte("late")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("public directory accepted another write: %v", err)
		}
		if _, err := stage.seal(); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("public directory was sealed: %v", err)
		}
		if err := stage.discard(); err != nil {
			t.Fatalf("public directory prevented cleanup: %v", err)
		}
	})
	t.Run("file", func(t *testing.T) {
		directory := privateTempDir(t)
		stage, err := newMediaStage(t.Context(), directory, 32)
		if err != nil {
			t.Fatal(err)
		}
		defer stage.discard()
		if _, err := stage.Write([]byte("private")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(directory, stage.name), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := stage.seal(); !errors.Is(err, errMediaStageChanged) {
			t.Fatalf("public file was sealed: %v", err)
		}
	})
}

func TestMediaStageDiscardReportsCloseFailureAndStillUnlinks(t *testing.T) {
	directory := privateTempDir(t)
	stage, err := newMediaStage(t.Context(), directory, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	if err := stage.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stage.discard(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("file close failure was hidden: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, stage.name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file close failure prevented cleanup: %v", err)
	}
	if err := stage.discard(); err != nil {
		t.Fatalf("finished cleanup was not idempotent: %v", err)
	}
}

func TestMediaStageCancellationKeepsCleanupAvailable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	directory := privateTempDir(t)
	stage, err := newMediaStage(ctx, directory, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := stage.Write([]byte("late")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write accepted: %v", err)
	}
	if _, err := stage.seal(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stage was sealed: %v", err)
	}
	if err := stage.discard(); err != nil {
		t.Fatalf("cancellation prevented cleanup: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled stage retained files: %v %v", entries, err)
	}
}

func TestMediaStageSerializesConcurrentWrites(t *testing.T) {
	directory := privateTempDir(t)
	stage, err := newMediaStage(t.Context(), directory, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.discard()
	var group sync.WaitGroup
	for index := range 32 {
		group.Go(func() {
			part := bytes.Repeat([]byte{byte(index)}, 32)
			if n, err := stage.Write(part); err != nil || n != len(part) {
				t.Errorf("concurrent stage write: %d %v", n, err)
			}
		})
	}
	group.Wait()
	sealed, err := stage.seal()
	if err != nil || sealed.Bytes != 32*32 {
		t.Fatalf("invalid concurrently sealed stage: %+v %v", sealed, err)
	}
	actual, err := os.ReadFile(filepath.Join(directory, stage.name))
	if err != nil || sha256.Sum256(actual) != sealed.SHA256 {
		t.Fatalf("stage digest does not match actual bytes: %v", err)
	}
}

func TestMediaStagesAreIndependentWhenCreatedConcurrently(t *testing.T) {
	directory := privateTempDir(t)
	const count = 16
	stages := make([]*mediaStage, count)
	var group sync.WaitGroup
	for index := range count {
		group.Go(func() {
			var err error
			stages[index], err = newMediaStage(t.Context(), directory, 64)
			if err != nil {
				t.Errorf("concurrent stage creation: %v", err)
				return
			}
			if _, err = stages[index].Write([]byte{byte(index)}); err != nil {
				t.Errorf("concurrent stage write: %v", err)
			}
		})
	}
	group.Wait()
	seen := make(map[string]bool, count)
	for index, stage := range stages {
		if stage == nil {
			continue
		}
		if seen[stage.name] {
			t.Errorf("stages reused a filename: %q", stage.name)
		}
		seen[stage.name] = true
		sealed, err := stage.seal()
		if err != nil || sealed.Bytes != 1 || sealed.SHA256 != sha256.Sum256([]byte{byte(index)}) {
			t.Errorf("one stage changed another: %+v %v", sealed, err)
		}
		if err := stage.discard(); err != nil {
			t.Errorf("concurrent stage cleanup: %v", err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("concurrent cleanup left files: %v %v", entries, err)
	}
}

func TestMediaStageSurvivesAbruptProcessExit(t *testing.T) {
	const helper = "HOMEBOX_MEDIA_STAGE_EXIT_TEST"
	if os.Getenv(helper) == "1" {
		stage, err := newMediaStage(context.Background(), os.Getenv("HOMEBOX_MEDIA_STAGE_TEST_DIR"), 128)
		if err == nil {
			_, err = stage.Write([]byte("sanitized test payload"))
		}
		if err == nil {
			_, err = stage.seal()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "stage helper failed:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, stage.name)
		os.Exit(0)
	}
	directory := privateTempDir(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMediaStageSurvivesAbruptProcessExit$")
	cmd.Env = append(os.Environ(), helper+"=1", "HOMEBOX_MEDIA_STAGE_TEST_DIR="+directory)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stage helper exited unsuccessfully: %v %s", err, output)
	}
	name := strings.TrimSpace(string(output))
	if !regexp.MustCompile(`^media-[a-f0-9]{32}\.partial$`).MatchString(name) {
		t.Fatalf("invalid staged name returned by helper: %q", name)
	}
	path := filepath.Join(directory, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("abrupt exit lost the private stage: %v %v", info, err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != "sanitized test payload" || sha256.Sum256(actual) != sha256.Sum256([]byte("sanitized test payload")) {
		t.Fatalf("abrupt exit changed staged bytes: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestMediaStageAcceptsBothActualSanitizerOutputs(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 222, G: 23, B: 50, A: 255})
	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	directory := privateTempDir(t)
	original, err := newMediaStage(t.Context(), directory, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer original.discard()
	thumb, err := newMediaStage(t.Context(), directory, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer thumb.discard()
	fullResult, thumbResult, err := mediaimage.SanitizeWithThumbnail(t.Context(), input.Bytes(), "image/png", original, thumb, mediaimage.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	fullStage, err := original.seal()
	if err != nil || fullStage.Bytes != fullResult.Bytes || fullStage.SHA256 != fullResult.SHA256 {
		t.Fatalf("original stage differs from sanitizer: %+v %+v %v", fullStage, fullResult, err)
	}
	thumbStage, err := thumb.seal()
	if err != nil || thumbStage.Bytes != thumbResult.Bytes || thumbStage.SHA256 != thumbResult.SHA256 {
		t.Fatalf("thumbnail stage differs from sanitizer: %+v %+v %v", thumbStage, thumbResult, err)
	}
	file, err := os.Open(filepath.Join(directory, thumb.name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := jpeg.Decode(io.LimitReader(file, 1<<20)); err != nil {
		t.Fatalf("staged thumbnail is not a JPEG: %v", err)
	}
}

func FuzzMediaStage(f *testing.F) {
	f.Add([]byte("private\x00bytes"), uint8(4), uint16(32))
	f.Add([]byte{}, uint8(1), uint16(1))
	f.Add([]byte("too large"), uint8(3), uint16(4))
	f.Fuzz(func(t *testing.T, value []byte, chunk uint8, encodedLimit uint16) {
		if len(value) > 1<<16 {
			t.Skip()
		}
		limit := int64(encodedLimit) + 1
		directory := privateTempDir(t)
		stage, err := newMediaStage(t.Context(), directory, limit)
		if err != nil {
			t.Fatal(err)
		}
		step := int(chunk) + 1
		var writeErr error
		for offset := 0; offset < len(value); offset += step {
			end := min(offset+step, len(value))
			if _, writeErr = stage.Write(value[offset:end]); writeErr != nil {
				break
			}
		}
		sealed, sealErr := stage.seal()
		switch {
		case len(value) == 0:
			if !errors.Is(sealErr, errMediaStageEmpty) {
				t.Fatalf("empty input was sealed: %v", sealErr)
			}
		case int64(len(value)) > limit:
			if !errors.Is(writeErr, errMediaStageTooLarge) || !errors.Is(sealErr, errMediaStageTooLarge) {
				t.Fatalf("overflow returned a prefix: %v %v", writeErr, sealErr)
			}
		default:
			if writeErr != nil || sealErr != nil || sealed.Bytes != int64(len(value)) || sealed.SHA256 != sha256.Sum256(value) {
				t.Fatalf("wrong staged digest or size: %+v %v %v", sealed, writeErr, sealErr)
			}
		}
		if err := stage.discard(); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("discard left staging contents: %v %v", entries, err)
		}
	})
}
