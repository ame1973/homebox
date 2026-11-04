// Package password provides bounded Argon2id hashing without logging credentials.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var (
	ErrPassword = errors.New("password must be valid UTF-8 with 12 to 256 characters")
	ErrParams   = errors.New("Argon2id parameters are outside the supported safety bounds")
	ErrEncoding = errors.New("unsupported or malformed password hash")
	ErrBusy     = errors.New("password verification capacity is busy")
)

// ponytail: one process-wide KDF bounds peak memory even across service instances.
// Increase concurrency only after measuring the target NAS memory budget.
var work sync.Mutex

// Params uses KiB, not bytes. Existing PHC hashes retain their own bounded costs.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

// DefaultParams provides a conservative household-server starting point, not a NAS benchmark.
func DefaultParams() Params { return Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 1} }

func (p Params) valid() bool {
	return p.MemoryKiB >= 19*1024 && p.MemoryKiB <= 128*1024 &&
		p.Iterations >= 2 && p.Iterations <= 6 && p.Parallelism >= 1 && p.Parallelism <= 4
}

// Hasher creates canonical PHC strings using immutable, validated parameters.
type Hasher struct{ params Params }

func New(p Params) (*Hasher, error) {
	if !p.valid() {
		return nil, ErrParams
	}
	return &Hasher{params: p}, nil
}

// ValidateNew applies the public contract's Unicode character bounds without trimming or normalization.
func ValidateNew(value string) error {
	if !validPassword(value, 12) {
		return ErrPassword
	}
	return nil
}

func validPassword(value string, minimum int) bool {
	// Bound bytes before counting runes; a Unicode scalar occupies at most four bytes.
	return len(value) <= 4*256 && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) >= minimum && utf8.RuneCountInString(value) <= 256
}

// Hash uses a fresh 128-bit salt and a 256-bit result. It never queues unbounded KDF work.
func (h *Hasher) Hash(ctx context.Context, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidateNew(value); err != nil {
		return "", err
	}
	if !h.params.valid() {
		return "", ErrParams
	}
	if !work.TryLock() {
		return "", ErrBusy
	}
	defer work.Unlock()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := h.params
	key := argon2.IDKey([]byte(value), salt, p.Iterations, p.MemoryKiB, p.Parallelism, 32)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify validates the stored PHC envelope before allocating KDF memory, and compares in constant time.
// The library KDF itself is not interruptible; cancellation discards its bounded result.
func (h *Hasher) Verify(ctx context.Context, value, encoded string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !validPassword(value, 1) {
		return false, nil
	}
	p, salt, expected, err := parse(encoded)
	if err != nil {
		return false, err
	}
	if !work.TryLock() {
		return false, ErrBusy
	}
	defer work.Unlock()
	actual := argon2.IDKey([]byte(value), salt, p.Iterations, p.MemoryKiB, p.Parallelism, 32)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func parse(encoded string) (Params, []byte, []byte, error) {
	invalid := func() (Params, []byte, []byte, error) { return Params{}, nil, nil, ErrEncoding }
	if len(encoded) > 256 {
		return invalid()
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return invalid()
	}
	costs := strings.Split(parts[3], ",")
	if len(costs) != 3 {
		return invalid()
	}
	var numbers [3]uint64
	for i, prefix := range []string{"m=", "t=", "p="} {
		value, ok := strings.CutPrefix(costs[i], prefix)
		if !ok {
			return invalid()
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != value {
			return invalid()
		}
		numbers[i] = n
	}
	if numbers[2] > 255 {
		return invalid()
	}
	p := Params{uint32(numbers[0]), uint32(numbers[1]), uint8(numbers[2])}
	if !p.valid() {
		return invalid()
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != 16 || base64.RawStdEncoding.EncodeToString(salt) != parts[4] {
		return invalid()
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) != 32 || base64.RawStdEncoding.EncodeToString(key) != parts[5] {
		return invalid()
	}
	return p, salt, key, nil
}
