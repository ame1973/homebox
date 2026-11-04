package password

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Generated independently with libargon2's argon2id_hash_raw, not this Go adapter.
const referencePHC = "$argon2id$v=19$m=19456,t=2,p=1$c29tZXNhbHQxMjM0NTY3OA$iqbHhgtqJMWWf23kIatRWniYNRyhFWif/l6I+m/2Exw"
const referencePassword = "correct horse battery staple"

func newTestHasher(t *testing.T) *Hasher {
	t.Helper()
	h, err := New(Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestReferenceVectorAndStoredCosts(t *testing.T) {
	h, err := New(DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		password string
		want     bool
	}{
		{referencePassword, true}, {"incorrect password", false}, {"", false},
		{strings.Repeat("x", 257), false}, {string([]byte{0xff}), false},
		{strings.Repeat("界", 1025), false},
	} {
		ok, err := h.Verify(t.Context(), input.password, referencePHC)
		if err != nil || ok != input.want {
			t.Fatalf("verification: match=%v, err=%v", ok, err)
		}
	}
}

func TestFreshSaltsAndUnicodeRoundTrip(t *testing.T) {
	h := newTestHasher(t)
	value := "保留所有密碼字元以及前後空格  "
	first, err := h.Hash(t.Context(), value)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Hash(t.Context(), value)
	if err != nil || second == first {
		t.Fatalf("salts were reused or hashing failed: %v", err)
	}
	_, salt1, _, err := parse(first)
	if err != nil {
		t.Fatal(err)
	}
	_, salt2, _, err := parse(second)
	if err != nil || string(salt1) == string(salt2) {
		t.Fatal("expected independent 128-bit salts")
	}
	if ok, err := h.Verify(t.Context(), value, first); !ok || err != nil {
		t.Fatalf("Unicode roundtrip failed: %v", err)
	}
	if ok, err := h.Verify(t.Context(), strings.TrimSpace(value), first); ok || err != nil {
		t.Fatal("password whitespace was silently normalized")
	}
}

func TestNewPasswordCharacterBounds(t *testing.T) {
	for _, value := range []string{strings.Repeat("界", 12), strings.Repeat("界", 256), strings.Repeat("x", 256)} {
		if err := ValidateNew(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"", strings.Repeat("x", 11), strings.Repeat("界", 257), strings.Repeat("x", 1025), "valid-length-but-invalid-utf8-\xff"} {
		if err := ValidateNew(value); !errors.Is(err, ErrPassword) {
			t.Fatal("invalid password accepted")
		}
		if hash, err := newTestHasher(t).Hash(t.Context(), value); hash != "" || !errors.Is(err, ErrPassword) {
			t.Fatal("invalid password reached the KDF")
		}
	}
}

func TestUnsafeParametersAreRejected(t *testing.T) {
	for _, p := range []Params{
		{}, {19*1024 - 1, 2, 1}, {128*1024 + 1, 2, 1},
		{19 * 1024, 1, 1}, {19 * 1024, 7, 1}, {19 * 1024, 2, 0}, {19 * 1024, 2, 5},
	} {
		if _, err := New(p); !errors.Is(err, ErrParams) {
			t.Fatal("unsafe cost accepted")
		}
	}
	if _, err := New(Params{128 * 1024, 6, 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := new(Hasher).Hash(t.Context(), referencePassword); !errors.Is(err, ErrParams) {
		t.Fatal("zero-value hasher must fail rather than panic in Argon2")
	}
}

func TestMalformedPHCIsRejectedBeforeAllocatingWork(t *testing.T) {
	h := newTestHasher(t)
	work.Lock()
	defer work.Unlock()
	cases := []string{"", strings.Repeat("x", 257), referencePHC + "$extra", "prefix" + referencePHC}
	for _, pair := range [][2]string{
		{"argon2id", "argon2i"}, {"v=19", "v=16"}, {"m=19456", "m=4294967295"},
		{"m=19456", "m=4294967296"}, {"m=19456", "m=1"}, {"m=19456", "m=-1"},
		{"m=19456", "m=019456"}, {"m=19456", "m=+19456"}, {"m=19456", "n=19456"},
		{"t=2", "t=0"}, {"t=2", "t=999"}, {"t=2", "t=2,junk=1"},
		{"p=1", "p=0"}, {"p=1", "p=257"}, {"p=1", "p=256"}, {"p=1", "p=1junk"},
		{"c29tZXNhbHQxMjM0NTY3OA", "AA"}, {"c29tZXNhbHQxMjM0NTY3OA", "c29tZXNhbHQxMjM0NTY3OB"},
		{"c29tZXNhbHQxMjM0NTY3OA", "c29tZXNhbHQxMjM0NTY3OA=="},
		{"c29tZXNhbHQxMjM0NTY3OA", "c29tZXNhbHQxMjM0NTY3OA\n"},
		{"iqbHhgtqJMWWf23kIatRWniYNRyhFWif/l6I+m/2Exw", "!invalid"},
		{"iqbHhgtqJMWWf23kIatRWniYNRyhFWif/l6I+m/2Exw", "AA"},
		{"iqbHhgtqJMWWf23kIatRWniYNRyhFWif/l6I+m/2Exw", "iqbHhgtqJMWWf23kIatRWniYNRyhFWif/l6I+m/2Exw\r"},
	} {
		cases = append(cases, strings.Replace(referencePHC, pair[0], pair[1], 1))
	}
	for _, encoded := range cases {
		if ok, err := h.Verify(t.Context(), referencePassword, encoded); ok || !errors.Is(err, ErrEncoding) {
			t.Fatalf("malformed encoding did not fail before KDF admission: %v", err)
		}
	}
}

func TestBusyLimitIsProcessWideAndCancellationWins(t *testing.T) {
	first, second := newTestHasher(t), newTestHasher(t)
	work.Lock()
	defer work.Unlock()
	if _, err := first.Hash(t.Context(), referencePassword); !errors.Is(err, ErrBusy) {
		t.Fatal("hash unexpectedly queued work")
	}
	if _, err := second.Verify(t.Context(), referencePassword, referencePHC); !errors.Is(err, ErrBusy) {
		t.Fatal("new instance bypassed process-wide work limit")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := first.Hash(ctx, referencePassword); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled hashing reached admission")
	}
	if _, err := second.Verify(ctx, referencePassword, referencePHC); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled verification reached admission")
	}
}

// Cancel at the post-KDF check without a sleep-dependent race in the test.
type cancelAtResult struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelAtResult) Err() error {
	c.checks++
	if c.checks == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestCanceledResultsAreDiscardedAndCapacityReleased(t *testing.T) {
	h := newTestHasher(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if result, err := h.Hash(&cancelAtResult{Context: ctx, cancel: cancel}, referencePassword); result != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled hash result escaped")
	}
	ctx, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	if ok, err := h.Verify(&cancelAtResult{Context: ctx, cancel: cancel2}, referencePassword, referencePHC); ok || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled verification result escaped")
	}
	if !work.TryLock() {
		t.Fatal("KDF capacity was not released")
	}
	work.Unlock()
}

func TestDefaultHashRoundTrip(t *testing.T) {
	h, err := New(DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := h.Hash(t.Context(), referencePassword)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.Verify(t.Context(), referencePassword, encoded); err != nil || !ok {
		t.Fatalf("default roundtrip failed: %v", err)
	}
}

func FuzzParsePHC(f *testing.F) {
	for _, seed := range []string{referencePHC, "", "$argon2id$v=19$m=99999999,t=1,p=1$a$b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, encoded string) {
		p, salt, key, err := parse(encoded)
		if err == nil && (!p.valid() || len(salt) != 16 || len(key) != 32) {
			t.Fatal("parser accepted an unsafe envelope")
		}
	})
}

func BenchmarkDefaultHash(b *testing.B) {
	h, err := New(DefaultParams())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.Hash(b.Context(), referencePassword); err != nil {
			b.Fatal(err)
		}
	}
}
