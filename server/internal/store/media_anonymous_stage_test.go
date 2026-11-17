//go:build linux

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func anonymousTestStage(t *testing.T, dir string, limit int64) *mediaStage {
	t.Helper()
	stage, err := newAnonymousMediaStage(t.Context(), dir, limit)
	if errors.Is(err, errMediaAnonymousUnsupported) {
		t.Skipf("anonymous temporary files are unavailable on this test filesystem: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stage.discard(); err != nil {
			t.Errorf("discard anonymous stage: %v", err)
		}
	})
	return stage
}

func TestAnonymousMediaStageSealsWithoutPublishingAName(t *testing.T) {
	dir := privateTempDir(t)
	stage := anonymousTestStage(t, dir, 32)
	contents := []byte("sanitized media bytes")
	if _, err := stage.Write(contents); err != nil {
		t.Fatal(err)
	}
	digest, err := stage.seal()
	if err != nil || digest.Bytes != int64(len(contents)) {
		t.Fatal("anonymous stage did not seal", digest, err)
	}
	if stage.file == nil {
		t.Fatal("sealing lost the unnamed inode")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(stage.file.Fd()), &stat); err != nil || stat.Nlink != 0 || stat.Mode&0o7777 != 0o600 {
		t.Fatal("sealed stage was not an anonymous private regular file", stat, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("anonymous stage became visible before a journal", entries, err)
	}
	if actual, err := hashMediaFile(t.Context(), stage.file, digest.Bytes); err != nil || actual != digest.SHA256 {
		t.Fatal("anonymous sealed bytes changed", err)
	}
	if _, err := stage.Write([]byte("more")); !errors.Is(err, errMediaStageClosed) {
		t.Fatal("sealed anonymous stage accepted another write", err)
	}
	if err := stage.discard(); err != nil {
		t.Fatal(err)
	}
	if entries, err = os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("discard left a named file", entries, err)
	}
}

func TestAnonymousMediaStageNeverRemovesANameCollision(t *testing.T) {
	dir := privateTempDir(t)
	stage := anonymousTestStage(t, dir, 32)
	if _, err := stage.Write([]byte("staged")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, stage.name)
	if err := os.WriteFile(path, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.seal(); !errors.Is(err, errMediaStageChanged) {
		t.Fatal("stage accepted an occupied intended name", err)
	}
	if err := stage.discard(); !errors.Is(err, errMediaStageChanged) {
		t.Fatal("discard did not report the conflicting name", err)
	}
	value, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(value, []byte("unrelated")) {
		t.Fatal("discard changed an unrelated file", string(value), err)
	}
}

func TestAnonymousMediaStagePreserveClosesOnlyTheUnnamedInode(t *testing.T) {
	dir := privateTempDir(t)
	stage := anonymousTestStage(t, dir, 32)
	if err := stage.preserve(); !errors.Is(err, errMediaStageUnsealed) {
		t.Fatal("unsealed stage was preserved", err)
	}
	if _, err := stage.Write([]byte("staged")); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.seal(); err != nil {
		t.Fatal(err)
	}
	if err := stage.preserve(); err != nil {
		t.Fatal(err)
	}
	if stage.file != nil || stage.dir != nil {
		t.Fatal("preserving an anonymous stage retained open descriptors")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("preserving an anonymous stage left a file", entries, err)
	}
}

func TestAnonymousMediaStageNeverRemovesAnUnexpectedHardLink(t *testing.T) {
	dir := privateTempDir(t)
	stage := anonymousTestStage(t, dir, 32)
	if _, err := stage.Write([]byte("linked outside the stage")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "unexpected-link")
	procFD := fmt.Sprintf("/proc/self/fd/%d", stage.file.Fd())
	if err := unix.Linkat(unix.AT_FDCWD, procFD, int(stage.dir.Fd()), "unexpected-link", unix.AT_SYMLINK_FOLLOW); err != nil {
		t.Skipf("this test filesystem cannot link an anonymous inode through procfs: %v", err)
	}
	if _, err := stage.seal(); !errors.Is(err, errMediaStageChanged) {
		t.Fatal("stage accepted an unexpected hard link", err)
	}
	if err := stage.discard(); !errors.Is(err, errMediaStageChanged) {
		t.Fatal("discard did not report the unexpected hard link", err)
	}
	value, err := os.ReadFile(path)
	if err != nil || string(value) != "linked outside the stage" {
		t.Fatal("discard changed the unexpected hard link", string(value), err)
	}
}

func TestAnonymousMediaStageRejectsInvalidAndCanceledInput(t *testing.T) {
	dir := privateTempDir(t)
	for _, limit := range []int64{0, -1, maxMediaStageBytes + 1} {
		if _, err := newAnonymousMediaStage(t.Context(), dir, limit); !errors.Is(err, errMediaStageLimit) {
			t.Fatal("invalid limit was accepted", limit, err)
		}
	}
	if _, err := newAnonymousMediaStage(nil, dir, 32); !errors.Is(err, errMediaStageLimit) {
		t.Fatal("nil context was accepted", err)
	}
	link := filepath.Join(privateTempDir(t), "staging-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := newAnonymousMediaStage(t.Context(), link, 32); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("symlinked staging directory was accepted", err)
	}
	public := filepath.Join(privateTempDir(t), "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newAnonymousMediaStage(t.Context(), public, 32); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("public staging directory was accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := newAnonymousMediaStage(ctx, dir, 32); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled constructor was accepted", err)
	}
	stage := anonymousTestStage(t, dir, 4)
	if _, err := stage.Write([]byte("12345")); !errors.Is(err, errMediaStageTooLarge) {
		t.Fatal("oversized write was accepted", err)
	}
	if _, err := stage.seal(); !errors.Is(err, errMediaStageTooLarge) {
		t.Fatal("poisoned stage sealed", err)
	}
}

func TestAnonymousMediaStageProcessExitLeavesNoFile(t *testing.T) {
	if dir := os.Getenv("HOMEBOX_ANONYMOUS_MEDIA_STAGE_TEST_DIR"); dir != "" {
		stage, err := newAnonymousMediaStage(context.Background(), dir, 32)
		if errors.Is(err, errMediaAnonymousUnsupported) {
			os.Exit(77)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err = stage.Write([]byte("private bytes")); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("HOMEBOX_ANONYMOUS_MEDIA_STAGE_TEST_SEAL") == "1" {
			if _, err = stage.seal(); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(0)
	}
	for _, seal := range []string{"0", "1"} {
		t.Run("sealed="+seal, func(t *testing.T) {
			dir := privateTempDir(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestAnonymousMediaStageProcessExitLeavesNoFile$")
			cmd.Env = append(os.Environ(), "HOMEBOX_ANONYMOUS_MEDIA_STAGE_TEST_DIR="+dir, "HOMEBOX_ANONYMOUS_MEDIA_STAGE_TEST_SEAL="+seal)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 77 {
				t.Skip("anonymous temporary files are unavailable on this test filesystem")
			}
			if err != nil {
				t.Fatalf("anonymous-stage child failed: %v: %s", err, output)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("process exit left an unjournaled stage", entries, err)
			}
		})
	}
}
