package service

import (
	"context"
	"errors"
	"testing"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
)

func pollenInput() CountPollenInput {
	return CountPollenInput{
		Operation: "op-pollen", Generation: 1,
		DeclaredTotal: 140, EnteredBy: "alice",
		Counts: []evidence.PollenCountInput{
			{Slide: "SL-1", Class: catalog.PollenTarget, Count: 120},
			{Slide: "SL-1", Class: catalog.PollenAccompanying, Count: 10},
			{Slide: "SL-1", Class: catalog.PollenUnknown, Count: 5},
			{Slide: "SL-1", Class: catalog.PollenContaminant, Count: 5},
		},
	}
}

func TestPollenCoverageComplete(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-1", "S-1"))
	e.advanceTo(t, id, gen, inspection.StateCountingPollen)
	in := pollenInput()
	in.TaskID = id
	in.Generation = gen
	if err := e.svc.CountPollen(context.Background(), in); err != nil {
		t.Fatalf("CountPollen: %v", err)
	}
	if e.currentState(t, id) != inspection.StateVerifyingDNA {
		t.Fatalf("state = %s", e.currentState(t, id))
	}
}

func TestPollenCountNotConservedRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-2", "S-2"))
	e.advanceTo(t, id, gen, inspection.StateCountingPollen)
	in := pollenInput()
	in.TaskID = id
	in.Generation = gen
	in.DeclaredTotal = 999 // mismatch with actual sum
	if err := e.svc.CountPollen(context.Background(), in); !errors.Is(err, evidence.ErrCountNotConserved) {
		t.Fatalf("got %v, want ErrCountNotConserved", err)
	}
}

func TestPollenUnknownClassRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-3", "S-3"))
	e.advanceTo(t, id, gen, inspection.StateCountingPollen)
	in := pollenInput()
	in.TaskID = id
	in.Generation = gen
	in.Counts[0].Class = catalog.PollenClass("alien")
	if err := e.svc.CountPollen(context.Background(), in); !errors.Is(err, evidence.ErrNotLockedClass) {
		t.Fatalf("got %v, want ErrNotLockedClass", err)
	}
}

func TestPollenCoverageIncompleteRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-4", "S-4"))
	e.advanceTo(t, id, gen, inspection.StateCountingPollen)
	in := pollenInput()
	in.TaskID = id
	in.Generation = gen
	in.Counts = in.Counts[:3] // drop the contaminant class -> incomplete
	in.DeclaredTotal = 135
	if err := e.svc.CountPollen(context.Background(), in); !errors.Is(err, evidence.ErrCoverIncomplete) {
		t.Fatalf("got %v, want ErrCoverIncomplete", err)
	}
}

func TestPollenBelowThresholdRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-5", "S-5"))
	e.advanceTo(t, id, gen, inspection.StateCountingPollen)
	in := pollenInput()
	in.TaskID = id
	in.Generation = gen
	in.Counts[0].Count = 50 // target below min 100
	in.DeclaredTotal = 70
	if err := e.svc.CountPollen(context.Background(), in); !errors.Is(err, evidence.ErrPollenBelowThreshold) {
		t.Fatalf("got %v, want ErrPollenBelowThreshold", err)
	}
}

func TestIllegalCountNotWritten(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-6", "S-6"))
	e.advanceTo(t, id, gen, inspection.StateCountingPollen)
	in := pollenInput()
	in.TaskID = id
	in.Generation = gen
	in.Counts[0].Count = -1
	if err := e.svc.CountPollen(context.Background(), in); !errors.Is(err, evidence.ErrNegativeCount) {
		t.Fatalf("got %v, want ErrNegativeCount", err)
	}
	cells, err := e.store.LoadPollenCells(context.Background(), id, gen)
	if err != nil {
		t.Fatalf("LoadPollenCells: %v", err)
	}
	if len(cells) != 0 {
		t.Fatalf("no cover cells should be written, got %d", len(cells))
	}
}
