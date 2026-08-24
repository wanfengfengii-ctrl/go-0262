package service

import (
	"context"
	"errors"
	"testing"

	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/store"
)

func TestBlindCodeDuplicateRejected(t *testing.T) {
	e := newEnv(t, nil)
	in1 := defaultLockInput("B-1", "S-1")
	in1.BlindCode = "BC-dup"
	id1, gen1 := e.lock(t, in1)
	e.advanceTo(t, id1, gen1, inspection.StateSealingSamples)

	in2 := defaultLockInput("B-2", "S-2")
	in2.BlindCode = "BC-dup"
	id2, gen2 := e.lock(t, in2)
	e.advanceTo(t, id2, gen2, inspection.StateSealingSamples)

	seal := func(id inspection.TaskID, gen inspection.Generation, op string) error {
		return e.svc.SealSamples(context.Background(), SealSamplesInput{
			Operation: inspection.OperationID(op), TaskID: id, Generation: gen,
			BlindCode: "BC-dup", Triplicates: []string{"T-1", "T-2", "T-3"}, SealedBy: "alice",
		})
	}
	if err := seal(id1, gen1, "seal-1"); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	if err := seal(id2, gen2, "seal-2"); !errors.Is(err, ErrResourceOccupied) {
		t.Fatalf("got %v, want ErrResourceOccupied", err)
	}
}

func TestPrematureRevealRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-3", "S-3"))
	// Still in pending-sampling-confirm: reveal is premature.
	err := e.svc.Reveal(context.Background(), RevealInput{TaskID: id, Generation: gen})
	if !errors.Is(err, ledger.ErrPrematureReveal) {
		t.Fatalf("got %v, want ErrPrematureReveal", err)
	}
}

func TestRevealAfterEvidenceAllowed(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-4", "S-4"))
	e.advanceTo(t, id, gen, inspection.StateVerifyingDNA)
	if err := e.svc.Reveal(context.Background(), RevealInput{TaskID: id, Generation: gen}); err != nil {
		t.Fatalf("reveal at verifying_dna should be allowed: %v", err)
	}
	b, err := e.store.LoadBlindSample(context.Background(), id)
	if err != nil {
		t.Fatalf("LoadBlindSample: %v", err)
	}
	if b.RevealState != ledger.RevealOpen {
		t.Fatalf("reveal state = %d, want open", b.RevealState)
	}
}

func TestTriplicateCountMismatchRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-5", "S-5"))
	e.advanceTo(t, id, gen, inspection.StateSealingSamples)
	err := e.svc.SealSamples(context.Background(), SealSamplesInput{
		Operation: "op-seal", TaskID: id, Generation: gen,
		BlindCode: "BC-B-5", Triplicates: []string{"T-1", "T-2"}, SealedBy: "alice",
	})
	if !errors.Is(err, ledger.ErrTriplicateCount) {
		t.Fatalf("got %v, want ErrTriplicateCount", err)
	}
	// The failed seal must not leave a blind sample record behind.
	if _, err := e.store.LoadBlindSample(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound (no partial seal)", err)
	}
}

func TestSealUnqualifiedRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-6", "S-6"))
	e.advanceTo(t, id, gen, inspection.StateSealingSamples)
	err := e.svc.SealSamples(context.Background(), SealSamplesInput{
		Operation: "op-seal", TaskID: id, Generation: gen,
		BlindCode: "BC-B-6", Triplicates: []string{"T-1", "T-2", "T-3"}, SealedBy: "mallory",
	})
	if !errors.Is(err, ErrUnqualified) {
		t.Fatalf("got %v, want ErrUnqualified", err)
	}
}
