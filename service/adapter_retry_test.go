package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/store"
)

func qpcrEnv(t *testing.T, errCode string) (*testEnv, inspection.TaskID, inspection.Generation) {
	t.Helper()
	adapters := map[evidence.InstrumentType]evidence.InstrumentAdapter{
		evidence.InstrumentQPCR: evidence.NewScriptedAdapter(evidence.InstrumentQPCR, "30.00", errCode),
	}
	e := newEnv(t, adapters)
	id, gen := e.lock(t, defaultLockInput("B-1", "S-1"))
	e.advanceTo(t, id, gen, inspection.StateVerifyingDNA)
	return e, id, gen
}

func TestAdapterRejectedRecordsRetry(t *testing.T) {
	e, id, gen := qpcrEnv(t, "rejected")
	err := e.svc.SubmitDNA(context.Background(), SubmitDNAInput{
		Operation: "op-dna-1", TaskID: id, Generation: gen,
		Instrument: "qpcr", AdapterCall: "run-1",
	})
	if !errors.Is(err, ErrAdapterRetry) {
		t.Fatalf("got %v, want ErrAdapterRetry", err)
	}
	attempts, err := e.store.LoadAttempts(context.Background(), id)
	if err != nil {
		t.Fatalf("LoadAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Result != evidence.AttemptRejected || attempts[0].RetryCount != 1 {
		t.Fatalf("unexpected attempts: %+v", attempts)
	}
	// State must not advance on a failed adapter call.
	if e.currentState(t, id) != inspection.StateVerifyingDNA {
		t.Fatalf("state advanced on failure: %s", e.currentState(t, id))
	}
}

func TestAdapterFailureKindsRecorded(t *testing.T) {
	cases := []struct {
		code string
		want evidence.AttemptResult
	}{
		{"disconnected", evidence.AttemptDisconnected},
		{"timeout", evidence.AttemptTimeout},
		{"garbage", evidence.AttemptMalformed},
	}
	for _, c := range cases {
		e, id, gen := qpcrEnv(t, c.code)
		if err := e.svc.SubmitDNA(context.Background(), SubmitDNAInput{
			Operation: "op-dna", TaskID: id, Generation: gen, Instrument: "qpcr", AdapterCall: "run",
		}); !errors.Is(err, ErrAdapterRetry) {
			t.Fatalf("code %s: got %v, want ErrAdapterRetry", c.code, err)
		}
		attempts, _ := e.store.LoadAttempts(context.Background(), id)
		if attempts[0].Result != c.want {
			t.Fatalf("code %s: result %s, want %s", c.code, attempts[0].Result, c.want)
		}
	}
}

func TestAdapterRetryCountIncrements(t *testing.T) {
	e, id, gen := qpcrEnv(t, "rejected")
	for i := 1; i <= 3; i++ {
		if err := e.svc.SubmitDNA(context.Background(), SubmitDNAInput{
			Operation: inspection.OperationID("op-dna-" + string(rune('0'+i))),
			TaskID:    id, Generation: gen, Instrument: "qpcr", AdapterCall: "run",
		}); !errors.Is(err, ErrAdapterRetry) {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	attempts, _ := e.store.LoadAttempts(context.Background(), id)
	if evidence.PendingRetries(attempts, "dna") != 3 {
		t.Fatalf("pending retries = %d, want 3", evidence.PendingRetries(attempts, "dna"))
	}
}

func TestAdapterRetryPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nectargate.db")

	adapters := map[evidence.InstrumentType]evidence.InstrumentAdapter{
		evidence.InstrumentQPCR: evidence.NewScriptedAdapter(evidence.InstrumentQPCR, "30.00", "timeout"),
	}

	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	e := newEnvStore(t, st, adapters)
	e.clock.t = 7
	id, gen := e.lock(t, defaultLockInput("B-1", "S-1"))
	e.advanceTo(t, id, gen, inspection.StateVerifyingDNA)

	if err := e.svc.SubmitDNA(context.Background(), SubmitDNAInput{
		Operation: "d1", TaskID: id, Generation: gen, Instrument: "qpcr", AdapterCall: "run",
	}); !errors.Is(err, ErrAdapterRetry) {
		t.Fatalf("got %v, want ErrAdapterRetry", err)
	}
	st.Close()

	// Restart: reopen and verify the retry record survived with its retry
	// count and logical time intact.
	st2, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	attempts, err := st2.LoadAttempts(context.Background(), id)
	if err != nil {
		t.Fatalf("LoadAttempts after restart: %v", err)
	}
	if len(attempts) != 1 || attempts[0].RetryCount != 1 || attempts[0].At != 7 {
		t.Fatalf("attempt not recovered: %+v", attempts)
	}
}
