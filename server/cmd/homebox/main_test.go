package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"homebox/internal/store"
	"homebox/migrations"
)

func privateDirectory(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func invoke(t *testing.T, args ...string) (map[string]any, error) {
	t.Helper()
	var output, diagnostic bytes.Buffer
	err := run(t.Context(), args, &output, &diagnostic)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output %q: %v", output.String(), err)
	}
	return result, nil
}

func TestVersionAndHelpDoNotOpenDatabase(t *testing.T) {
	result, err := invoke(t, "version")
	if err != nil || result["version"] != version || result["build_id"] != buildID {
		t.Fatalf("version: %v, %v", result, err)
	}
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"db", "migrate", "--help"}} {
		var out, diagnostic bytes.Buffer
		if err := run(t.Context(), args, &out, &diagnostic); err != nil {
			t.Fatal(err)
		}
		if out.Len()+diagnostic.Len() == 0 {
			t.Fatal("help did not describe the command")
		}
	}
}

func TestMigrateCheckAndRepeatedMigrate(t *testing.T) {
	path := filepath.Join(privateDirectory(t), "homebox.sqlite")
	if _, err := invoke(t, "db", "check", "--database", path); !errors.Is(err, store.ErrMigrationRequired) {
		t.Fatalf("check initialized a missing database: %v", err)
	}
	result, err := invoke(t, "db", "migrate", "--database", path)
	if err != nil {
		t.Fatal(err)
	}
	report := result["migration"].(map[string]any)
	if report["from_version"] != float64(0) || report["legacy_adopted"] != false {
		t.Fatalf("unexpected migration report: %v", report)
	}
	result, err = invoke(t, "db", "check", "--database", path)
	if err != nil || result["integrity"] != "ok" || result["journal_mode"] != "wal" || result["foreign_keys"] != true {
		t.Fatalf("check: %v, %v", result, err)
	}
	result, err = invoke(t, "db", "migrate", "--database", path)
	if err != nil {
		t.Fatal(err)
	}
	report = result["migration"].(map[string]any)
	if report["from_version"] != report["to_version"] {
		t.Fatal("second migration changed schema")
	}
}

func TestLegacyMigrationAndSnapshotVerificationCommands(t *testing.T) {
	directory := privateDirectory(t)
	path := filepath.Join(directory, "legacy.sqlite")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(all[0].SQL); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO items(id,name,tracking_mode,unit_code,created_at,updated_at) VALUES('legacy-item','Flour','quantity','kg','2026','2026')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	mappingPath := filepath.Join(directory, "units.json")
	if err = os.WriteFile(mappingPath, []byte(`{"legacy-item":{"base_unit":"g","scale_factor":1000}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := invoke(t, "db", "migrate", "--database", path, "--allow-legacy", "--legacy-units", mappingPath)
	if err != nil {
		t.Fatal(err)
	}
	report := result["migration"].(map[string]any)
	if report["legacy_adopted"] != true {
		t.Fatal("legacy approval not recorded")
	}
	result, err = invoke(t, "db", "verify-snapshot", "--snapshot", report["backup_path"].(string))
	if err != nil || result["verified"] != true {
		t.Fatalf("verify-snapshot: %v, %v", result, err)
	}
}

func TestInvalidCommandsFlagsAndTimeoutsFailWithoutMutation(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"}, {"db"}, {"version", "extra"}, {"db", "unknown"},
		{"db", "check", "--allow-legacy"}, {"db", "migrate", "extra"},
		{"db", "check", "--timeout", "0"}, {"db", "check", "--timeout", "invalid"},
		{"db", "migrate", "--database", ""}, {"db", "verify-snapshot"},
		{"db", "migrate", "--legacy-units", "/definitely-not-present/homebox.json"},
		{"db", "verify-snapshot", "--snapshot", "/definitely-not-present/snapshot.sqlite"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := run(t.Context(), args, io.Discard, io.Discard); err == nil {
				t.Fatal("invalid command succeeded")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	path := filepath.Join(privateDirectory(t), "cancelled.sqlite")
	if err := run(ctx, []string{"db", "migrate", "--database", path}, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUnitMappingFileValidation(t *testing.T) {
	directory := privateDirectory(t)
	path := filepath.Join(directory, "units.json")
	for name, body := range map[string]string{
		"null": "null", "array": "[]", "truncated": "{", "extra document": "{} {}",
		"unknown field":     `{"id":{"base_unit":"g","scale_factor":1000,"unknown":true}}`,
		"fractional factor": `{"id":{"base_unit":"g","scale_factor":1.5}}`,
		"duplicate item":    `{"id":{"base_unit":"g","scale_factor":1},"id":{"base_unit":"g","scale_factor":2}}`,
		"oversize":          strings.Repeat(" ", (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readUnitMappings(path); err == nil {
				t.Fatal("invalid unit mapping file accepted")
			}
		})
	}
	if _, err := readUnitMappings(directory); err == nil {
		t.Fatal("directory accepted as a mapping file")
	}
	fifo := filepath.Join(directory, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readUnitMappings(fifo); err == nil {
		t.Fatal("FIFO accepted as a mapping file")
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOutputErrorsAreNotReportedAsSuccess(t *testing.T) {
	if err := run(t.Context(), []string{"version"}, failedOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	path := filepath.Join(privateDirectory(t), "homebox.sqlite")
	if err := run(t.Context(), []string{"db", "migrate", "--database", path}, failedOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if _, err := invoke(t, "db", "check", "--database", path); err != nil {
		t.Fatalf("output error leaked the database lock: %v", err)
	}
}
