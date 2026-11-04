package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

func (s *Store) snapshot(ctx context.Context, directory string) (path, digest string, finalErr error) {
	staging, err := os.CreateTemp(directory, "migration-*.sqlite.partial")
	if err != nil {
		return "", "", err
	}
	stagingPath := staging.Name()
	defer func() {
		_ = staging.Close()
		if err := os.Remove(stagingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			finalErr = errors.Join(finalErr, err)
		}
	}()
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return "", "", err
	}
	defer connection.Close()
	u := url.URL{Scheme: "file", Path: stagingPath}
	q := url.Values{"mode": {"rw"}, "_pragma": {"journal_mode(DELETE)", "synchronous(FULL)"}, "_defensive": {"true"}}
	u.RawQuery = q.Encode()
	err = connection.Raw(func(raw any) (finalErr error) {
		// modernc's connection type is unexported; its documented backup API is
		// exposed through this narrow capability assertion.
		source, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver does not expose the Online Backup API")
		}
		backup, err := source.NewBackup(u.String())
		if err != nil {
			return err
		}
		defer func() { finalErr = errors.Join(finalErr, backup.Finish()) }()
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(128)
			if err != nil {
				return err
			}
			if !more {
				return nil
			}
		}
	})
	if err != nil {
		return "", "", err
	}
	// The Online Backup API copies the source WAL header. Reapply DELETE mode
	// after copying so the published snapshot never depends on a WAL sidecar.
	finished, err := sql.Open("sqlite", u.String())
	if err != nil {
		return "", "", err
	}
	err = errors.Join(finished.PingContext(ctx), finished.Close())
	if err != nil {
		return "", "", err
	}
	if err = staging.Sync(); err != nil {
		return "", "", err
	}
	if err = staging.Close(); err != nil {
		return "", "", err
	}
	if err = checkSnapshotDatabase(ctx, stagingPath); err != nil {
		return "", "", err
	}
	digest, err = hashFile(ctx, stagingPath)
	if err != nil {
		return "", "", err
	}
	path = strings.TrimSuffix(stagingPath, ".partial")
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return "", "", errors.New("backup destination already exists or cannot be inspected")
	}
	if err = os.Rename(stagingPath, path); err != nil {
		return "", "", err
	}
	// A checksum marker is published only after SQLite/FK verification. If
	// marker publication fails, abort the migration and retain the snapshot.
	marker, err := os.OpenFile(path+".sha256", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return path, digest, err
	}
	_, writeErr := fmt.Fprintf(marker, "%s  %s\n", digest, filepath.Base(path))
	err = errors.Join(writeErr, marker.Sync(), marker.Close())
	if err != nil {
		return path, digest, err
	}
	if err = syncDirectory(directory); err != nil {
		return path, digest, err
	}
	return path, digest, nil
}

// VerifySnapshot checks a private migration snapshot and its SHA-256 marker.
// It never opens the file for mutation or treats a raw SQLite file as a full media backup.
func VerifySnapshot(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return ErrUnsafePath
	}
	markerInfo, err := os.Lstat(path + ".sha256")
	if err != nil {
		return err
	}
	if !markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm()&0o077 != 0 || markerInfo.Size() > 512 {
		return ErrUnsafePath
	}
	markerFile, err := os.Open(path + ".sha256")
	if err != nil {
		return err
	}
	marker, readErr := io.ReadAll(io.LimitReader(markerFile, 513))
	if err = errors.Join(readErr, markerFile.Close()); err != nil {
		return err
	}
	if len(marker) > 512 {
		return errors.New("snapshot checksum marker is too large")
	}
	parts := strings.Fields(string(marker))
	if len(parts) != 2 || len(parts[0]) != 64 || parts[1] != filepath.Base(path) {
		return errors.New("invalid snapshot checksum marker")
	}
	actual, err := hashFile(ctx, path)
	if err != nil {
		return err
	}
	if actual != parts[0] {
		return errors.New("snapshot checksum mismatch")
	}
	return checkSnapshotDatabase(ctx, path)
}

func checkSnapshotDatabase(ctx context.Context, path string) error {
	if err := validateSidecars(path, true); err != nil {
		return err
	}
	db, err := openSQL(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = integrity(ctx, db); err != nil {
		return err
	}
	var mode string
	if err = db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return errors.New("snapshot must be a self-contained rollback-journal database")
	}
	return nil
}

func hashFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		_, _ = hash.Write(buffer[:n])
		if errors.Is(readErr, io.EOF) {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
}
