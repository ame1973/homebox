package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"homebox/migrations"
)

const checksumDDL = `CREATE TABLE IF NOT EXISTS schema_migration_checksums (
 version INTEGER PRIMARY KEY REFERENCES schema_migrations(version),
 filename TEXT NOT NULL,
 sha256 TEXT NOT NULL CHECK(length(sha256)=64)
) STRICT`

func (s *Store) migrate(ctx context.Context, cfg Config, all []migrations.Migration) (report MigrationReport, finalErr error) {
	version, hasLedger, err := history(ctx, s.db, all)
	if err != nil {
		return report, err
	}
	report.From, report.To = version, len(all)
	legacy := version > 0 && !hasLedger
	if legacy && (!cfg.Migrate || !cfg.AllowLegacy) {
		return report, ErrLegacyApproval
	}
	if version < len(all) && !cfg.Migrate {
		return report, ErrMigrationRequired
	}
	if err = integrity(ctx, s.db); err != nil {
		return report, err
	}
	if legacy {
		if version > 2 {
			return report, fmt.Errorf("%w: only reference versions 1 and 2 can be adopted", ErrSchemaChanged)
		}
		if err = verifyLegacySchema(ctx, s.db, all[:version]); err != nil {
			return report, err
		}
	}
	needsMapping := (version > 0 && version < 2 && len(all) >= 2) || legacy
	if err = validateUnitMappings(ctx, s.db, cfg.LegacyUnits, needsMapping); err != nil {
		return report, err
	}
	if !legacy && version == len(all) {
		return report, nil
	}
	if version > 0 {
		if err = validateLegacyDomain(ctx, s.db); err != nil {
			return report, err
		}
	}
	bytes, err := databaseBytes(ctx, s.db)
	if err != nil {
		return report, err
	}
	if err = requireSpace(filepath.Dir(s.path), bytes, cfg.ReserveBytes); err != nil {
		return report, err
	}
	if version > 0 {
		backupDir := cfg.BackupDir
		if backupDir == "" {
			backupDir = filepath.Join(filepath.Dir(s.path), "migration-backups")
		}
		backupDir, err = privateDir(backupDir)
		if err != nil {
			return report, err
		}
		if err = requireSpace(backupDir, bytes, cfg.ReserveBytes); err != nil {
			return report, err
		}
		report.BackupPath, report.BackupSHA256, err = s.snapshot(ctx, backupDir)
		if err != nil {
			return report, fmt.Errorf("pre-migration backup failed: %w", err)
		}
	}
	defer func() {
		if finalErr != nil && report.BackupPath != "" {
			finalErr = fmt.Errorf("migration rolled back; verified recovery snapshot retained at %s: %w", report.BackupPath, finalErr)
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, checksumDDL); err != nil {
		return report, err
	}
	for _, migration := range all {
		if migration.Version <= version {
			if legacy {
				if err = requireDomainRow(tx.ExecContext(ctx, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)); err != nil {
					return report, err
				}
			}
			continue
		}
		if _, err = tx.ExecContext(ctx, migration.SQL); err != nil {
			return report, fmt.Errorf("apply migration %s: %w", migration.Filename, err)
		}
		if migration.Version == 9 {
			if err = validateCatalogLinkBackfill(ctx, tx); err != nil {
				return report, fmt.Errorf("validate catalog link backfill: %w", err)
			}
		}
		if migration.Version == 10 {
			if err = validateHoldingsBackfill(ctx, tx); err != nil {
				return report, fmt.Errorf("validate physical holding backfill: %w", err)
			}
		}
		if migration.Version == 13 {
			if err = validatePurchasesBackfill(ctx, tx); err != nil {
				return report, fmt.Errorf("validate purchase allocation backfill: %w", err)
			}
		}
		if migration.Version == 15 {
			if err = validateAssetsBackfill(ctx, tx); err != nil {
				return report, fmt.Errorf("validate asset financial backfill: %w", err)
			}
		}
		if migration.Version == 19 {
			if err = validateMediaBackfill(ctx, tx); err != nil {
				return report, fmt.Errorf("validate retained media backfill: %w", err)
			}
		}
		// Reference migrations insert their own version row. New migrations can
		// leave metadata to the runner, which always owns checksum publication.
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,applied_at)
 SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version=?)`,
			migration.Version, migration.Filename, time.Now().UTC().Format(time.RFC3339Nano), migration.Version); err != nil {
			return report, err
		}
		if err = requireDomainRow(tx.ExecContext(ctx, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)); err != nil {
			return report, err
		}
	}
	if len(all) >= 2 {
		for id, mapping := range cfg.LegacyUnits {
			if _, err = tx.ExecContext(ctx, "UPDATE items SET base_unit=?,scale_factor=? WHERE id=?", mapping.BaseUnit, mapping.ScaleFactor, id); err != nil {
				return report, err
			}
		}
		if legacy {
			if _, err = tx.ExecContext(ctx, "UPDATE sync_state SET dataset_epoch=lower(hex(randomblob(16))),updated_at=? WHERE singleton=1", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return report, err
			}
		}
	}
	// Recheck required media materialization after every migration-ledger and
	// legacy-mapping write, not only immediately after its SQL backfill.
	if version < 19 && len(all) >= 19 {
		if err = validateMediaBackfill(ctx, tx); err != nil {
			return report, fmt.Errorf("revalidate retained media backfill: %w", err)
		}
	}
	finalVersion, finalLedger, err := history(ctx, tx, all)
	if err != nil {
		return report, err
	}
	if !finalLedger || finalVersion != len(all) {
		return report, fmt.Errorf("%w: migration history did not fully persist", ErrSchemaChanged)
	}
	if err = integrity(ctx, tx); err != nil {
		return report, err
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	report.LegacyAdopted = legacy
	return report, nil
}

func history(ctx context.Context, db queryer, all []migrations.Migration) (int, bool, error) {
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='schema_migrations'").Scan(&exists); err != nil {
		return 0, false, err
	}
	if exists == 0 {
		var objects int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
			return 0, false, err
		}
		if objects != 0 {
			return 0, false, fmt.Errorf("%w: unversioned nonempty database", ErrSchemaChanged)
		}
		return 0, false, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, false, err
	}
	version := 0
	for rows.Next() {
		var current int
		if err = rows.Scan(&current); err != nil {
			break
		}
		if current > len(all) {
			err = ErrNewerSchema
			break
		}
		if current != version+1 {
			err = ErrSchemaChanged
			break
		}
		version = current
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return 0, false, err
	}
	if version == 0 {
		return 0, false, fmt.Errorf("%w: empty migration history", ErrSchemaChanged)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='schema_migration_checksums'").Scan(&exists); err != nil {
		return 0, false, err
	}
	if exists == 0 {
		return version, false, nil
	}
	rows, err = db.QueryContext(ctx, "SELECT version,filename,sha256 FROM schema_migration_checksums ORDER BY version")
	if err != nil {
		return 0, false, fmt.Errorf("%w: invalid checksum ledger: %v", ErrSchemaChanged, err)
	}
	count := 0
	for rows.Next() {
		var current int
		var filename, checksum string
		if err = rows.Scan(&current, &filename, &checksum); err != nil {
			break
		}
		if current != count+1 || current > version || filename != all[current-1].Filename || checksum != all[current-1].Checksum {
			err = ErrSchemaChanged
			break
		}
		count++
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return 0, false, err
	}
	if count != version {
		return 0, false, fmt.Errorf("%w: incomplete checksum ledger", ErrSchemaChanged)
	}
	return version, true, nil
}

func verifyLegacySchema(ctx context.Context, db *sql.DB, applied []migrations.Migration) error {
	expected, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	expected.SetMaxOpenConns(1)
	defer expected.Close()
	for _, migration := range applied {
		if _, err = expected.ExecContext(ctx, migration.SQL); err != nil {
			return err
		}
	}
	actualSchema, err := schemaDescription(ctx, db)
	if err != nil {
		return err
	}
	expectedSchema, err := schemaDescription(ctx, expected)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actualSchema, expectedSchema) {
		return fmt.Errorf("%w: legacy DDL is not the known reference schema", ErrSchemaChanged)
	}
	return nil
}

func schemaDescription(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_schema
 WHERE name NOT LIKE 'sqlite_%' AND name<>'schema_migration_checksums' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var kind, name, table, query string
		if err = rows.Scan(&kind, &name, &table, &query); err != nil {
			return nil, err
		}
		result = append(result, kind+"|"+name+"|"+table+"|"+strings.Join(strings.Fields(query), " "))
	}
	return result, rows.Err()
}

var baseUnitPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_/-]{0,31}$`)

func validateUnitMappings(ctx context.Context, db *sql.DB, mappings map[string]UnitMapping, needed bool) error {
	if !needed {
		if len(mappings) > 0 {
			return fmt.Errorf("%w: mappings are only accepted during a legacy precision transition", ErrUnitMapping)
		}
		return nil
	}
	rows, err := db.QueryContext(ctx, "SELECT id FROM items WHERE unit_code<>'pcs'")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		mapping, ok := mappings[id]
		if !ok || !baseUnitPattern.MatchString(mapping.BaseUnit) || mapping.ScaleFactor < 1 || mapping.ScaleFactor > 9007199254740991 {
			return fmt.Errorf("%w: provide a valid mapping for item %s", ErrUnitMapping, id)
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != len(mappings) {
		return fmt.Errorf("%w: mapping contains an unknown or pcs item", ErrUnitMapping)
	}
	return nil
}

func validateLegacyDomain(ctx context.Context, db *sql.DB) error {
	for _, table := range []string{"locations", "categories"} {
		// Table names are compile-time constants, never user input. UNION bounds
		// traversal even when a legacy graph contains a multi-node cycle.
		query := `WITH RECURSIVE ancestors(start,id) AS (
 SELECT id,parent_id FROM ` + table + ` WHERE parent_id IS NOT NULL
 UNION SELECT a.start,n.parent_id FROM ancestors a JOIN ` + table + ` n ON n.id=a.id WHERE n.parent_id IS NOT NULL
) SELECT EXISTS(SELECT 1 FROM ancestors WHERE start=id)`
		var cycle bool
		if err := db.QueryRowContext(ctx, query).Scan(&cycle); err != nil {
			return err
		}
		if cycle {
			return fmt.Errorf("%w: legacy %s contains a cycle", ErrSchemaChanged, table)
		}
	}
	var invalid bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM item_units u JOIN items i ON i.id=u.item_id WHERE i.tracking_mode<>'individual'
 UNION ALL SELECT 1 FROM stock_balances b JOIN items i ON i.id=b.item_id WHERE i.tracking_mode<>'quantity'
)`).Scan(&invalid); err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("%w: legacy holdings do not match their tracking mode", ErrSchemaChanged)
	}
	return nil
}

func databaseBytes(ctx context.Context, db *sql.DB) (uint64, error) {
	var pages, size uint64
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return 0, err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return 0, err
	}
	if size == 0 || pages > math.MaxUint64/size {
		return 0, errors.New("invalid SQLite page size or count")
	}
	return pages * size, nil
}
