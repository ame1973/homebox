package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

const testSyncProofInstance = "0123456789abcdef0123456789abcdef"

type cancelOnSearchProofCheck struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelOnSearchProofCheck) Err() error {
	c.checks++
	if c.checks == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestSyncSearchProofMatchesIndependentFramedVector(t *testing.T) {
	secret := make([]byte, 32)
	for index := range secret {
		secret[index] = byte(index)
	}
	auth := &Auth{serverSecret: secret}
	epoch, identity := "epoch-家庭", projectionPair("item", "sample\x00")
	payload := []byte(`{"entity_type":"item","entity_id":"sample\u0000","approved_text":"Reviewed"}`)
	original := bytes.Clone(payload)
	proof, err := auth.syncSearchProof(t.Context(), testSyncProofInstance, epoch, identity, 42, 7, payload)
	if err != nil || hex.EncodeToString(proof[:]) != "4a84586495b6315225880ba1df66a0d9e74ceda196c5b50e5de6a8d5168efa44" || !bytes.Equal(payload, original) {
		t.Fatal("proof did not preserve the independently framed vector", err)
	}
	again, err := auth.syncSearchProof(t.Context(), testSyncProofInstance, epoch, identity, 42, 7, payload)
	if err != nil || proof != again {
		t.Fatal("identical frozen input produced a different proof", err)
	}
}

func TestSyncSearchProofBindsEveryExactInputAndItsPrivateKey(t *testing.T) {
	auth := &Auth{serverSecret: bytes.Repeat([]byte{1}, 32)}
	epoch, identity, payload := "epoch-1", projectionPair("item", "source"), []byte(`{"approved_text":"reviewed"}`)
	baseline, err := auth.syncSearchProof(t.Context(), testSyncProofInstance, epoch, identity, 42, 7, payload)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, epoch, identity string
		seq, revision         int64
		payload, secret       []byte
	}{
		{"epoch", "epoch-2", identity, 42, 7, payload, auth.serverSecret},
		{"identity", epoch, projectionPair("unit", "source"), 42, 7, payload, auth.serverSecret},
		{"sequence", epoch, identity, 43, 7, payload, auth.serverSecret},
		{"revision", epoch, identity, 42, 8, payload, auth.serverSecret},
		{"exact bytes", epoch, identity, 42, 7, []byte(`{ "approved_text":"reviewed"}`), auth.serverSecret},
		{"key", epoch, identity, 42, 7, payload, bytes.Repeat([]byte{2}, 32)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			other := &Auth{serverSecret: test.secret}
			proof, err := other.syncSearchProof(t.Context(), testSyncProofInstance, test.epoch, test.identity, test.seq, test.revision, test.payload)
			if err != nil || proof == baseline {
				t.Fatal("changed input reused a search proof", err)
			}
		})
	}
	otherInstance, err := auth.syncSearchProof(t.Context(), "fedcba9876543210fedcba9876543210", epoch, identity, 42, 7, payload)
	if err != nil || otherInstance == baseline {
		t.Fatal("different server instances reused a search proof", err)
	}
	if bytes.Equal(baseline[:], auth.authMAC("", "sync-delta-cursor-v1", payload)) {
		t.Fatal("search proofs shared the cursor MAC domain")
	}
}

func TestSyncSearchProofBoundsInputsAndHonorsCancellation(t *testing.T) {
	auth := &Auth{serverSecret: bytes.Repeat([]byte{1}, 32)}
	identity, payload := projectionPair("item", "source"), []byte(`{}`)
	for _, test := range []struct {
		name, epoch, identity string
		seq, revision         int64
		payload               []byte
	}{
		{"missing epoch", "", identity, 1, 1, payload},
		{"oversized epoch", strings.Repeat("😀", 129), identity, 1, 1, payload},
		{"invalid epoch UTF-8", string([]byte{0xff}), identity, 1, 1, payload},
		{"malformed identity", "epoch", `["item", "source"]`, 1, 1, payload},
		{"unsupported kind", "epoch", projectionPair("other", "source"), 1, 1, payload},
		{"zero sequence", "epoch", identity, 0, 1, payload},
		{"unsafe sequence", "epoch", identity, maxSafeInteger + 1, 1, payload},
		{"zero revision", "epoch", identity, 1, 0, payload},
		{"unsafe revision", "epoch", identity, 1, maxSafeInteger + 1, payload},
		{"empty payload", "epoch", identity, 1, 1, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof, err := auth.syncSearchProof(t.Context(), testSyncProofInstance, test.epoch, test.identity, test.seq, test.revision, test.payload)
			if !errors.Is(err, ErrDomainInput) || proof != [32]byte{} {
				t.Fatal("invalid proof input was accepted", err)
			}
		})
	}
	for _, instance := range []string{"", "0123456789abcdef0123456789abcdeF", strings.Repeat("a", 33)} {
		if proof, err := auth.syncSearchProof(t.Context(), instance, "epoch", identity, 1, 1, payload); !errors.Is(err, ErrDomainInput) || proof != [32]byte{} {
			t.Fatal("invalid server identity was accepted", err)
		}
	}
	if proof, err := auth.syncSearchProof(t.Context(), testSyncProofInstance, strings.Repeat("😀", 128), projectionPair("unit", strings.Repeat("\x00", 128)), maxSafeInteger, maxSafeInteger, payload); err != nil || proof == [32]byte{} {
		t.Fatal("maximum valid dataset and identity were rejected", err)
	}
	var absent *Auth
	if proof, err := absent.syncSearchProof(t.Context(), testSyncProofInstance, "epoch", identity, 1, 1, payload); !errors.Is(err, ErrAuthState) || proof != [32]byte{} {
		t.Fatal("missing signer produced a proof", err)
	}
	if proof, err := auth.syncSearchProof(nil, testSyncProofInstance, "epoch", identity, 1, 1, payload); !errors.Is(err, ErrDomainInput) || proof != [32]byte{} {
		t.Fatal("nil context produced a proof", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	counted := &cancelOnSearchProofCheck{Context: ctx, cancel: cancel}
	defer cancel()
	if proof, err := auth.syncSearchProof(counted, testSyncProofInstance, "epoch", identity, 1, 1, bytes.Repeat([]byte("x"), 3*(64<<10))); !errors.Is(err, context.Canceled) || proof != [32]byte{} || counted.checks < 3 {
		t.Fatal("mid-payload cancellation returned a proof", err)
	}
	// The MAC binds bytes but does not validate their schema or reviewed origin.
	if _, err := auth.syncSearchProof(t.Context(), testSyncProofInstance, "epoch", identity, 1, 1, []byte("not-json")); err != nil {
		t.Fatal("the binding helper unexpectedly claimed to validate source", err)
	}
}

func FuzzSyncSearchProof(f *testing.F) {
	f.Add("epoch", projectionPair("item", "source"), int64(1), int64(1), []byte(`{"value":"reviewed"}`))
	f.Add("epoch\x00", projectionPair("unit", "source\x00"), int64(42), int64(7), []byte("not-json"))
	f.Add("", "not-a-pair", int64(0), int64(-1), []byte{})
	f.Fuzz(func(t *testing.T, epoch, identity string, seq, revision int64, payload []byte) {
		if len(epoch) > 1024 || len(identity) > 4096 || len(payload) > 64<<10 {
			t.Skip()
		}
		auth := &Auth{serverSecret: bytes.Repeat([]byte{7}, 32)}
		original := bytes.Clone(payload)
		first, firstErr := auth.syncSearchProof(t.Context(), testSyncProofInstance, epoch, identity, seq, revision, payload)
		second, secondErr := auth.syncSearchProof(t.Context(), testSyncProofInstance, epoch, identity, seq, revision, payload)
		if !bytes.Equal(original, payload) || first != second || (firstErr == nil) != (secondErr == nil) {
			t.Fatal("the same input was changed or produced inconsistent proofs")
		}
		if firstErr != nil {
			if !errors.Is(firstErr, ErrDomainInput) || first != [32]byte{} {
				t.Fatal("invalid input produced a proof", firstErr)
			}
			return
		}
		changed := append(bytes.Clone(payload), 0)
		other, err := auth.syncSearchProof(t.Context(), testSyncProofInstance, epoch, identity, seq, revision, changed)
		if err != nil || other == first {
			t.Fatal("different exact bytes produced the same proof", err)
		}
	})
}
