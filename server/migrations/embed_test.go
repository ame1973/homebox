package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadPreservesOriginalChecksumsAndRemovesOnlyTransactionWrappers(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 2 {
		t.Fatal("reference migrations are missing")
	}
	for _, migration := range all {
		raw, err := files.ReadFile(migration.Filename)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		if migration.Checksum != hex.EncodeToString(digest[:]) {
			t.Fatal("checksum is not based on original bytes")
		}
		for _, line := range strings.Split(migration.SQL, "\n") {
			if line == "BEGIN IMMEDIATE;" || line == "COMMIT;" || line == "PRAGMA foreign_keys=ON;" {
				t.Fatal("runner must own the migration transaction")
			}
		}
	}
	if !strings.Contains(all[0].SQL, "CREATE TRIGGER search_docs_update") || !strings.Contains(all[0].SQL, "END;") {
		t.Fatal("transaction extraction damaged trigger bodies")
	}
}

func TestLoadRejectsGapsDuplicatesAndMalformedNames(t *testing.T) {
	for _, names := range [][]string{{}, {"0002_second.sql"}, {"0001_a.sql", "0001_b.sql"}, {"0001_a.sql", "0003_gap.sql"}, {"1_short.sql"}, {"oops.sql"}} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			source := fstest.MapFS{}
			for _, name := range names {
				source[name] = &fstest.MapFile{Data: []byte("SELECT 1;")}
			}
			if _, err := load(source); err == nil {
				t.Fatal("invalid migration history accepted")
			}
		})
	}
}

func TestTransactionWrappersMustBeBalancedAndUnique(t *testing.T) {
	for _, raw := range []string{"BEGIN IMMEDIATE;\nSELECT 1;", "COMMIT;", "BEGIN IMMEDIATE;\nCOMMIT;\nBEGIN IMMEDIATE;\nCOMMIT;"} {
		if _, err := transactionBody(raw); err == nil {
			t.Fatalf("accepted malformed wrapper %q", raw)
		}
	}
	for _, raw := range []string{"SELECT 1;", "BEGIN IMMEDIATE;\nSELECT 1;\nCOMMIT;"} {
		if _, err := transactionBody(raw); err != nil {
			t.Fatal(err)
		}
	}
}
