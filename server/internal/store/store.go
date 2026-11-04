// Package store owns database access. Domain services must not expose raw SQL handles.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"homebox/migrations"
	_ "modernc.org/sqlite"
)

var (
	ErrLocked            = errors.New("database is already owned by another HomeBox process")
	ErrUnsafePath        = errors.New("unsafe database path or permissions")
	ErrMigrationRequired = errors.New("explicit database migration is required")
	ErrLegacyApproval    = errors.New("legacy database adoption requires explicit approval")
	ErrSchemaChanged     = errors.New("database schema or migration checksum differs from the supported history")
	ErrNewerSchema       = errors.New("database schema is newer than this application")
	ErrInsufficientSpace = errors.New("insufficient free space for a safe migration")
	ErrUnitMapping       = errors.New("explicit legacy base-unit mapping is required")
)

const defaultReserveBytes = 16 << 20

// Config controls local persistence. Migrations are opt-in, never a startup side effect.
type Config struct {
	Path         string
	Migrate      bool
	AllowLegacy  bool
	BackupDir    string
	ReserveBytes uint64
	LegacyUnits  map[string]UnitMapping
}

// UnitMapping certifies the meaning of existing integer base counts; counts are never rescaled.
type UnitMapping struct {
	BaseUnit    string `json:"base_unit"`
	ScaleFactor int64  `json:"scale_factor"`
}

// MigrationReport describes an operator-authorized schema transition.
// BackupPath is local-only information and must not be exposed through public API responses.
type MigrationReport struct {
	From          int    `json:"from_version"`
	To            int    `json:"to_version"`
	LegacyAdopted bool   `json:"legacy_adopted"`
	BackupPath    string `json:"backup_path,omitempty"`
	BackupSHA256  string `json:"backup_sha256,omitempty"`
}

// Health contains non-sensitive persistence diagnostics.
type Health struct {
	SQLiteVersion string `json:"sqlite_version"`
	SchemaVersion int    `json:"schema_version"`
	JournalMode   string `json:"journal_mode"`
	ForeignKeys   bool   `json:"foreign_keys"`
	BusyTimeoutMS int    `json:"busy_timeout_ms"`
	Integrity     string `json:"integrity"`
}

// Store serializes the authoritative database and holds a process-lifetime lock.
type Store struct {
	db        *sql.DB
	path      string
	lock      *os.File
	report    MigrationReport
	closeOnce sync.Once
	closeErr  error
}

// Open checks capabilities and history before making the store available to callers.
func Open(ctx context.Context, cfg Config) (_ *Store, finalErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Path == "" || cfg.Path == ":memory:" || strings.HasPrefix(cfg.Path, "file:") {
		return nil, fmt.Errorf("%w: provide a local filesystem database path", ErrUnsafePath)
	}
	path, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, err
	}
	dir, err := privateDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	path = filepath.Join(dir, filepath.Base(path))
	lock, err := acquireLock(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, lock: lock}
	defer func() {
		if finalErr != nil {
			finalErr = errors.Join(finalErr, s.Close())
		}
	}()
	file, err := privateFile(path, cfg.Migrate)
	if err != nil {
		if !cfg.Migrate && errors.Is(err, os.ErrNotExist) {
			return nil, ErrMigrationRequired
		}
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	s.db, err = openSQL(path, false)
	if err != nil {
		return nil, err
	}
	if err = s.db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	if err = s.capabilities(ctx); err != nil {
		return nil, err
	}
	all, err := migrations.Load()
	if err != nil {
		return nil, err
	}
	if cfg.ReserveBytes == 0 {
		cfg.ReserveBytes = defaultReserveBytes
	}
	s.report, err = s.migrate(ctx, cfg, all)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func dsn(path string, readOnly bool) string {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Set("mode", "rw")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Set("_defensive", "true")
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(FULL)")
		q.Set("_txlock", "immediate")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func openSQL(path string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, readOnly))
	if err != nil {
		return nil, err
	}
	// ponytail: one connection serializes household writes; add a read-only pool
	// only after profiling shows meaningful read contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func (s *Store) capabilities(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, query := range []string{
		"CREATE TEMP TABLE homebox_strict_probe(value INTEGER NOT NULL) STRICT",
		"CREATE VIRTUAL TABLE temp.homebox_fts_probe USING fts5(value)",
		"INSERT INTO homebox_fts_probe(value) VALUES('capability')",
	} {
		if _, err = conn.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("required SQLite STRICT/FTS5 support: %w", err)
		}
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "DROP TABLE temp.homebox_fts_probe; DROP TABLE temp.homebox_strict_probe")
	}()
	var valid, matches, foreignKeys int
	if err = conn.QueryRowContext(ctx, "SELECT json_valid('{\"valid\":true}')").Scan(&valid); err != nil {
		return fmt.Errorf("required SQLite JSON support: %w", err)
	}
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM homebox_fts_probe WHERE homebox_fts_probe MATCH 'capability'").Scan(&matches); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	if valid != 1 || matches != 1 || foreignKeys != 1 {
		return errors.New("SQLite JSON, FTS5, or foreign-key capability check failed")
	}
	return nil
}

// Report returns the completed migration outcome, not a mutable database handle.
func (s *Store) Report() MigrationReport { return s.report }

// Close releases the database before releasing its process lock; it is idempotent.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
		if s.lock != nil {
			s.closeErr = errors.Join(s.closeErr, s.lock.Close())
		}
	})
	return s.closeErr
}

// Check performs full SQLite and foreign-key integrity checks without returning private data.
func (s *Store) Check(ctx context.Context) (Health, error) {
	var health Health
	if err := integrity(ctx, s.db); err != nil {
		return health, err
	}
	queries := []struct {
		query  string
		target any
	}{
		{"SELECT sqlite_version()", &health.SQLiteVersion},
		{"SELECT COALESCE(MAX(version),0) FROM schema_migrations", &health.SchemaVersion},
		{"PRAGMA journal_mode", &health.JournalMode},
		{"PRAGMA foreign_keys", &health.ForeignKeys},
		{"PRAGMA busy_timeout", &health.BusyTimeoutMS},
	}
	for _, query := range queries {
		if err := s.db.QueryRowContext(ctx, query.query).Scan(query.target); err != nil {
			return health, err
		}
	}
	health.Integrity = "ok"
	return health, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func integrity(ctx context.Context, db queryer) error {
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	ok := false
	for rows.Next() {
		var message string
		if err = rows.Scan(&message); err != nil {
			break
		}
		if message != "ok" {
			err = errors.New("SQLite integrity check failed")
			break
		}
		ok = true
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("SQLite integrity check returned no result")
	}
	rows, err = db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	if rows.Next() {
		err = errors.New("SQLite foreign-key check failed")
	}
	return errors.Join(err, rows.Err(), rows.Close())
}
