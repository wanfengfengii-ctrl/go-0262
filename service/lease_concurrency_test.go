package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"nectargate/raw-honey-maturation-intake/inspection"
)

func TestLeaseConcurrencySingleWinner(t *testing.T) {
	e := newEnv(t, nil)

	// Two tasks freeze the same tank slot, well and slide at lock time (the
	// lease uniqueness is only decided at claim time).
	shared := func(barrel, seal string) LockInput {
		in := defaultLockInput(barrel, seal)
		in.TankSlot = "TS-shared"
		in.Well = "W-shared"
		in.Slides = []string{"SL-shared"}
		return in
	}
	id1, gen1 := e.lock(t, shared("B-A", "S-A"))
	id2, gen2 := e.lock(t, shared("B-B", "S-B"))
	e.advanceTo(t, id1, gen1, inspection.StateClaimingResources)
	e.advanceTo(t, id2, gen2, inspection.StateClaimingResources)

	claim := func(id inspection.TaskID, gen inspection.Generation, op string) error {
		return e.svc.ClaimLeases(context.Background(), ClaimLeasesInput{
			Operation: inspection.OperationID(op), TaskID: id, Generation: gen,
			TankSlot: "TS-shared", Well: "W-shared", Slides: []string{"SL-shared"},
		})
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = claim(id1, gen1, "claim-1") }()
	go func() { defer wg.Done(); errs[1] = claim(id2, gen2, "claim-2") }()
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrResourceOccupied):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1 and 1", successes, conflicts)
	}
}

func TestSwapWellConflictLeavesNoPartialLease(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-C", "S-C"))
	e.advanceTo(t, id, gen, inspection.StateCountingPollen)

	// Task B-D freezes and then occupies the same new well first.
	inD := defaultLockInput("B-D", "S-D")
	inD.Well = "W-TAKEN"
	inD.Slides = []string{"SL-D"}
	id2, gen2 := e.lock(t, inD)
	e.advanceTo(t, id2, gen2, inspection.StateClaimingResources)
	if err := e.svc.ClaimLeases(context.Background(), ClaimLeasesInput{
		Operation: "op-claim-2", TaskID: id2, Generation: gen2,
		TankSlot: "TS-B-D", Well: "W-TAKEN", Slides: []string{"SL-D"},
	}); err != nil {
		t.Fatalf("claim for B-D: %v", err)
	}

	// B-C tries to swap its well to W-TAKEN, which B-D already holds.
	err := e.svc.SwapWell(context.Background(), SwapWellInput{
		Operation: "op-swap", TaskID: id, Generation: gen,
		OldWell: "W-B-C", NewWell: "W-TAKEN",
	})
	if !errors.Is(err, ErrResourceOccupied) {
		t.Fatalf("got %v, want ErrResourceOccupied", err)
	}

	// B-C must still hold its original well.
	leases, err := e.store.LoadLeases(context.Background(), id)
	if err != nil {
		t.Fatalf("LoadLeases: %v", err)
	}
	found := false
	for _, l := range leases {
		if l.ResourceType == "well" && l.ResourceID == "W-B-C" && l.Status == 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("original well lease was lost after failed swap")
	}
}
