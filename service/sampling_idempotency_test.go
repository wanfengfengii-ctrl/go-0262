package service

import (
	"context"
	"errors"
	"testing"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
)

func confirmInput() ConfirmSamplingInput {
	return ConfirmSamplingInput{
		Operation: "op-confirm", Generation: 1,
		Samplers: []catalog.PersonnelID{"alice", "bob"}, Barrel: "B-001", Seal: "S-001",
	}
}

func TestConfirmSamplingSuccess(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-001", "S-001"))
	in := confirmInput()
	in.TaskID = id
	in.Generation = gen
	if err := e.svc.ConfirmSampling(context.Background(), in); err != nil {
		t.Fatalf("ConfirmSampling: %v", err)
	}
	if e.currentState(t, id) != inspection.StateSealingSamples {
		t.Fatalf("state = %s, want sealing_samples", e.currentState(t, id))
	}
}

func TestConfirmSamplingOverlapRejected(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-001", "S-001"))
	in := confirmInput()
	in.TaskID = id
	in.Generation = gen
	in.Samplers = []catalog.PersonnelID{"alice", "alice"} // duplicate sampler
	if err := e.svc.ConfirmSampling(context.Background(), in); !errors.Is(err, inspection.ErrSamplerCount) {
		t.Fatalf("got %v, want ErrSamplerCount", err)
	}
}

func TestConfirmSamplingIdempotentReplay(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-001", "S-001"))
	in := confirmInput()
	in.TaskID = id
	in.Generation = gen
	if err := e.svc.ConfirmSampling(context.Background(), in); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := e.svc.ConfirmSampling(context.Background(), in); err != nil {
		t.Fatalf("replay should be idempotent, got %v", err)
	}
	if e.currentState(t, id) != inspection.StateSealingSamples {
		t.Fatalf("state = %s", e.currentState(t, id))
	}
}

func TestConfirmSamplingContentConflict(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-001", "S-001"))
	in := confirmInput()
	in.TaskID = id
	in.Generation = gen
	if err := e.svc.ConfirmSampling(context.Background(), in); err != nil {
		t.Fatalf("first: %v", err)
	}
	in.Seal = "S-999" // same operation, different content
	if err := e.svc.ConfirmSampling(context.Background(), in); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("got %v, want ErrOperationConflict", err)
	}
}
