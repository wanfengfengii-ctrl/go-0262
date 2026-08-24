package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
)

func TestRejudgeDuplicateRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-1", "S-1"))
	e.advanceTo(t, id, gen, inspection.StateVerifyingDNA)

	rej := func(op string) error {
		return e.svc.Rejudge(context.Background(), RejudgeInput{
			Operation: inspection.OperationID(op), TaskID: id, Generation: gen,
			BlindCode: "BC-B-1", Slide: "SL-1", Well: "W-B-1", Reason: "fingerprint mismatch", Reviewer: "carol",
		})
	}
	if err := rej("rej-1"); err != nil {
		t.Fatalf("first rejudge: %v", err)
	}
	if err := rej("rej-2"); !errors.Is(err, evidence.ErrDuplicateRejudge) {
		t.Fatalf("got %v, want ErrDuplicateRejudge", err)
	}
}

func TestFinalizeMaturedOnCleanEvidence(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-2", "S-2"))
	e.advanceTo(t, id, gen, inspection.StateReadyForMaturation)
	res, err := e.svc.Finalize(context.Background(), FinalizeInput{
		Operation: "op-final", TaskID: id, Generation: gen, Reviewer: "carol",
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.FinalType != arbiter.FinalMatured {
		t.Fatalf("final = %s, want matured", res.FinalType)
	}
	if e.currentState(t, id) != inspection.StateMatured {
		t.Fatalf("state = %s, want matured", e.currentState(t, id))
	}
}

func TestFinalizeQuarantineOnRejudge(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-3", "S-3"))
	e.advanceTo(t, id, gen, inspection.StateVerifyingDNA)
	if err := e.svc.Rejudge(context.Background(), RejudgeInput{
		Operation: "rej", TaskID: id, Generation: gen,
		BlindCode: "BC-B-3", Slide: "SL-1", Well: "W-B-3", Reason: "anomaly", Reviewer: "carol",
	}); err != nil {
		t.Fatalf("rejudge: %v", err)
	}
	e.advanceTo(t, id, gen, inspection.StateReadyForMaturation)
	res, err := e.svc.Finalize(context.Background(), FinalizeInput{
		Operation: "op-final", TaskID: id, Generation: gen, Reviewer: "carol",
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.FinalType != arbiter.FinalQuarantined {
		t.Fatalf("final = %s, want quarantined", res.FinalType)
	}
}

func TestFinalizeSingleWinner(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-4", "S-4"))
	e.advanceTo(t, id, gen, inspection.StateReadyForMaturation)

	var wg sync.WaitGroup
	results := make([]FinalizeResult, 2)
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0], errs[0] = e.svc.Finalize(context.Background(), FinalizeInput{
			Operation: "final-a", TaskID: id, Generation: gen, Reviewer: "carol",
		})
	}()
	go func() {
		defer wg.Done()
		results[1], errs[1] = e.svc.Finalize(context.Background(), FinalizeInput{
			Operation: "final-b", TaskID: id, Generation: gen, Reviewer: "dave",
		})
	}()
	wg.Wait()

	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		switch {
		case errs[i] == nil:
			success++
		case errors.Is(errs[i], arbiter.ErrFinalAlreadySet), errors.Is(errs[i], ErrTerminalState), errors.Is(errs[i], inspection.ErrTerminalState):
			conflict++
		default:
			t.Fatalf("unexpected finalize error: %v", errs[i])
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d, want 1 and 1 (results=%+v errs=%v)", success, conflict, results, errs)
	}
}

func TestTerminalRejectsFurtherOperations(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-5", "S-5"))
	e.advanceTo(t, id, gen, inspection.StateReadyForMaturation)
	if _, err := e.svc.Finalize(context.Background(), FinalizeInput{
		Operation: "op-final", TaskID: id, Generation: gen, Reviewer: "carol",
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Every ordinary operation must now be rejected without changing state.
	if err := e.svc.SubmitDNA(context.Background(), SubmitDNAInput{
		Operation: "post-dna", TaskID: id, Generation: gen, Reading: "20.00",
	}); !errors.Is(err, inspection.ErrTerminalState) {
		t.Fatalf("SubmitDNA got %v, want ErrTerminalState", err)
	}
	if err := e.svc.AddReview(context.Background(), AddReviewInput{
		Operation: "post-rev", TaskID: id, Generation: gen, Reviewer: "carol",
	}); !errors.Is(err, inspection.ErrTerminalState) {
		t.Fatalf("AddReview got %v, want ErrTerminalState", err)
	}
}

func TestLateGenerationReadingIsolated(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-6", "S-6"))
	e.advanceTo(t, id, gen, inspection.StateVerifyingDNA)

	// A reading claiming a stale generation is rejected and cannot overwrite
	// the current evidence chain.
	err := e.svc.SubmitDNA(context.Background(), SubmitDNAInput{
		Operation: "op-dna", TaskID: id, Generation: gen - 1, Reading: "40.00",
	})
	if !errors.Is(err, inspection.ErrGenerationMismatch) {
		t.Fatalf("got %v, want ErrGenerationMismatch", err)
	}
	chain, err := e.store.LoadEvidence(context.Background(), id, gen)
	if err != nil {
		t.Fatalf("LoadEvidence: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("stale reading polluted the chain: %+v", chain)
	}
	if e.currentState(t, id) != inspection.StateVerifyingDNA {
		t.Fatalf("state changed on stale reading: %s", e.currentState(t, id))
	}
}
