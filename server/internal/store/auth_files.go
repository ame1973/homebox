package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// BootstrapFile describes local delivery without containing the credential itself.
type BootstrapFile struct {
	Path      string    `json:"bootstrap_file"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IssueBootstrapFile is an offline operator workflow. It never overwrites an existing path.
// A durable private file is published before the hash/audit transaction commits.
func (a *Auth) IssueBootstrapFile(ctx context.Context, path string) (_ BootstrapFile, finalErr error) {
	if err := ctx.Err(); err != nil {
		return BootstrapFile{}, err
	}
	if path == "" {
		return BootstrapFile{}, ErrUnsafePath
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return BootstrapFile{}, err
	}
	directory, err := privateDir(filepath.Dir(path))
	if err != nil {
		return BootstrapFile{}, err
	}
	path = filepath.Join(directory, filepath.Base(path))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return BootstrapFile{}, err
	}
	committed := false
	defer func() {
		if file != nil {
			finalErr = errors.Join(finalErr, file.Close())
		}
		if !committed {
			// Only the fresh O_EXCL file belongs to this attempt; existing paths are untouched.
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				finalErr = errors.Join(finalErr, err)
			}
			finalErr = errors.Join(finalErr, syncDirectory(directory))
		}
	}()
	if err = file.Chmod(0o600); err != nil {
		return BootstrapFile{}, err
	}
	_, expires, err := a.issueBootstrap(ctx, func(token string) error {
		if _, err := file.WriteString(token + "\n"); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		err := file.Close()
		file = nil
		if err != nil {
			return err
		}
		return syncDirectory(directory)
	})
	if err != nil {
		return BootstrapFile{}, err
	}
	committed = true
	return BootstrapFile{Path: path, ExpiresAt: expires}, nil
}
