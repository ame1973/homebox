// Package migrations embeds the immutable, forward-only database migrations.
package migrations

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Migration contains the original checksum and a transaction-neutral SQL body.
type Migration struct {
	Version  int
	Filename string
	Checksum string
	SQL      string
}

// Load returns migrations in contiguous version order.
func Load() ([]Migration, error) { return load(files) }

func load(source fs.FS) ([]Migration, error) {
	names, err := fs.Glob(source, "*.sql")
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no embedded migrations")
	}
	result := make([]Migration, 0, len(names))
	for index, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		version, parseErr := strconv.Atoi(prefix)
		if !ok || len(prefix) != 4 || parseErr != nil || version != index+1 {
			return nil, fmt.Errorf("migration %q is not contiguous version %04d", name, index+1)
		}
		raw, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(raw)
		body, err := transactionBody(string(raw))
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", name, err)
		}
		result = append(result, Migration{version, name, hex.EncodeToString(digest[:]), body})
	}
	return result, nil
}

func transactionBody(raw string) (string, error) {
	// Preserve the reference files byte-for-byte while letting the runner own
	// the transaction containing both their DDL and the checksum ledger.
	lines := strings.Split(raw, "\n")
	begins, commits := 0, 0
	for index, line := range lines {
		switch strings.TrimSpace(line) {
		case "PRAGMA foreign_keys=ON;":
			lines[index] = ""
		case "BEGIN IMMEDIATE;":
			begins++
			lines[index] = ""
		case "COMMIT;":
			commits++
			lines[index] = ""
		}
	}
	if begins != commits || begins > 1 {
		return "", fmt.Errorf("unexpected transaction wrapper")
	}
	return strings.Join(lines, "\n"), nil
}
