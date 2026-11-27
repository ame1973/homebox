//go:build linux

package store

import (
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMigrationSpaceUsesFilesystemFragmentSize(t *testing.T) {
	for _, test := range []struct {
		name      string
		stat      unix.Statfs_t
		database  uint64
		reserve   uint64
		available uint64
		valid     bool
	}{
		{"ordinary exact boundary", unix.Statfs_t{Bsize: 4096, Frsize: 4096, Blocks: 100, Bavail: 10}, 18432, 4096, 40960, true},
		{"one byte short", unix.Statfs_t{Bsize: 4096, Frsize: 4096, Blocks: 100, Bavail: 10}, 18432, 4097, 40960, false},
		{"different fragment size", unix.Statfs_t{Bsize: 4096, Frsize: 1024, Blocks: 100, Bavail: 5}, 3000, 0, 5120, false},
		{"different fragment exact", unix.Statfs_t{Bsize: 4096, Frsize: 1024, Blocks: 100, Bavail: 10}, 4096, 2048, 10240, true},
		{"absent fragment falls back", unix.Statfs_t{Bsize: 4096, Blocks: 100, Bavail: 10}, 18432, 4096, 40960, true},
		{"negative fragment", unix.Statfs_t{Bsize: 4096, Frsize: -1, Blocks: 100, Bavail: 10}, 1, 0, 0, false},
		{"zero transfer block", unix.Statfs_t{Bsize: 0, Frsize: 4096, Blocks: 100, Bavail: 10}, 1, 0, 0, false},
		{"available exceeds blocks", unix.Statfs_t{Bsize: 4096, Frsize: 4096, Blocks: 1, Bavail: 2}, 1, 0, 0, false},
		{"available byte overflow", unix.Statfs_t{Bsize: 2, Frsize: 2, Blocks: math.MaxUint64, Bavail: math.MaxUint64}, 1, 0, 0, false},
		{"requirement overflow", unix.Statfs_t{Bsize: 1, Frsize: 1, Blocks: math.MaxUint64, Bavail: math.MaxUint64}, math.MaxUint64/2 + 1, 0, 0, false},
		{"above browser integer limit", unix.Statfs_t{Bsize: 1, Frsize: 1, Blocks: uint64(maxSafeInteger) + 2, Bavail: uint64(maxSafeInteger) + 2}, 1, 0, uint64(maxSafeInteger) + 2, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			available, err := checkMigrationSpace(test.stat, test.database, test.reserve)
			if test.valid && (err != nil || available != test.available) || !test.valid && (!errors.Is(err, ErrInsufficientSpace) || available != test.available) {
				t.Fatal("invalid migration free-space decision", available, err)
			}
		})
	}
}

func TestMigrationSpaceReadsActualFilesystem(t *testing.T) {
	directory := t.TempDir()
	if err := requireSpace(directory, 0, 0); err != nil {
		t.Fatal("could not inspect disposable filesystem", err)
	}
	if err := requireSpace(directory, 0, math.MaxUint64); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatal("impossible reserve was accepted", err)
	}
	if err := requireSpace(filepath.Join(directory, "missing"), 0, 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing filesystem path did not fail", err)
	}
}

func FuzzMigrationSpace(f *testing.F) {
	f.Add(int64(4096), int64(1024), uint64(100), uint64(5), uint64(3000), uint64(0))
	f.Add(int64(4096), int64(0), uint64(100), uint64(10), uint64(18432), uint64(4096))
	f.Add(int64(1), int64(1), uint64(math.MaxUint64), uint64(math.MaxUint64), uint64(math.MaxUint64), uint64(0))
	f.Fuzz(func(t *testing.T, block, fragment int64, blocks, available, database, reserve uint64) {
		actual, err := checkMigrationSpace(unix.Statfs_t{Bsize: block, Frsize: fragment, Blocks: blocks, Bavail: available}, database, reserve)
		invalid := block <= 0 || fragment < 0 || available > blocks
		var expected uint64
		if !invalid {
			if fragment == 0 {
				fragment = block
			}
			free := new(big.Int).Mul(new(big.Int).SetUint64(available), big.NewInt(fragment))
			need := new(big.Int).Lsh(new(big.Int).SetUint64(database), 1)
			need.Add(need, new(big.Int).SetUint64(reserve))
			invalid = free.BitLen() > 64 || need.BitLen() > 64
			if !invalid {
				expected = free.Uint64()
				invalid = free.Cmp(need) < 0
			}
		}
		if actual != expected || invalid && !errors.Is(err, ErrInsufficientSpace) || !invalid && err != nil {
			t.Fatal("migration space decision disagrees with exact arithmetic", actual, expected, err)
		}
	})
}
