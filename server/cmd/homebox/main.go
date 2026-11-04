package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"homebox/internal/store"
)

var (
	version = "0.1.0-dev"
	buildID = "local"
)

const usage = `HomeBox

Usage:
  homebox version
  homebox db migrate [--database PATH] [--allow-legacy] [--legacy-units FILE]
                     [--backup-dir DIR] [--reserve-bytes BYTES] [--timeout DURATION]
  homebox db check [--database PATH] [--timeout DURATION]
  homebox db verify-snapshot --snapshot PATH [--timeout DURATION]

Database commands require exclusive access to a local, private data directory.
HTTP serving and the PWA are not yet available in this development build.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "homebox:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (finalErr error) {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	if args[0] == "version" && len(args) == 1 {
		return json.NewEncoder(stdout).Encode(struct {
			Version string `json:"version"`
			BuildID string `json:"build_id"`
			OS      string `json:"os"`
			Arch    string `json:"arch"`
		}{version, buildID, runtime.GOOS, runtime.GOARCH})
	}
	if len(args) < 2 || args[0] != "db" {
		return errors.New("unknown command; run 'homebox --help'")
	}
	verb := args[1]
	if verb != "migrate" && verb != "check" && verb != "verify-snapshot" {
		return errors.New("unknown database command; run 'homebox --help'")
	}
	flags := flag.NewFlagSet("homebox db "+verb, flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", 2*time.Minute, "Maximum operation duration")
	var database, snapshot, backupDir, mappingsPath string
	var allowLegacy bool
	var reserve uint64
	if verb == "verify-snapshot" {
		flags.StringVar(&snapshot, "snapshot", "", "Private SQLite snapshot with an adjacent .sha256 marker")
	} else {
		flags.StringVar(&database, "database", "data/homebox.sqlite", "Local SQLite database file")
	}
	if verb == "migrate" {
		flags.BoolVar(&allowLegacy, "allow-legacy", false, "Adopt an exact reference v0.7/v0.9 database after a verified snapshot")
		flags.StringVar(&mappingsPath, "legacy-units", "", "JSON map of legacy item IDs to base_unit and scale_factor")
		flags.StringVar(&backupDir, "backup-dir", "", "Private pre-migration snapshot directory; default is beside the database")
		flags.Uint64Var(&reserve, "reserve-bytes", 16<<20, "Free-space reserve in addition to twice the logical database size")
	}
	if err := flags.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments after database command")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if verb == "verify-snapshot" {
		if snapshot == "" {
			return errors.New("--snapshot is required")
		}
		if err := store.VerifySnapshot(ctx, snapshot); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(struct {
			Verified bool `json:"verified"`
		}{true})
	}
	var mappings map[string]store.UnitMapping
	if mappingsPath != "" {
		var err error
		mappings, err = readUnitMappings(mappingsPath)
		if err != nil {
			return err
		}
	}
	db, err := store.Open(ctx, store.Config{Path: database, Migrate: verb == "migrate", AllowLegacy: allowLegacy,
		BackupDir: backupDir, ReserveBytes: reserve, LegacyUnits: mappings})
	if err != nil {
		return err
	}
	defer func() { finalErr = errors.Join(finalErr, db.Close()) }()
	health, err := db.Check(ctx)
	if err != nil {
		return err
	}
	if verb == "check" {
		return json.NewEncoder(stdout).Encode(health)
	}
	return json.NewEncoder(stdout).Encode(struct {
		Migration store.MigrationReport `json:"migration"`
		Database  store.Health          `json:"database"`
	}{db.Report(), health})
}

func readUnitMappings(path string) (map[string]store.UnitMapping, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("unit mappings must be a regular JSON file no larger than 1 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, (1<<20)+1))
	decoder.DisallowUnknownFields()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("unit mappings must be a JSON object")
	}
	mappings := make(map[string]store.UnitMapping)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		id, ok := key.(string)
		if !ok {
			return nil, errors.New("unit mapping keys must be item IDs")
		}
		if _, exists := mappings[id]; exists {
			return nil, errors.New("duplicate item ID in unit mappings")
		}
		var mapping store.UnitMapping
		if err = decoder.Decode(&mapping); err != nil {
			return nil, fmt.Errorf("decode unit mapping: %w", err)
		}
		mappings[id] = mapping
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("unit mappings must contain exactly one JSON object")
	}
	return mappings, nil
}
