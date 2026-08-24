package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/store"
)

type fakeClock struct{ t inspection.LogicalTime }

func (c *fakeClock) Now() inspection.LogicalTime { return c.t }
func (c *fakeClock) advance()                    { c.t++ }

type testEnv struct {
	svc   *Service
	cat   *catalog.Catalog
	store store.Store
	clock *fakeClock
}

func newEnv(t *testing.T, adapters map[evidence.InstrumentType]evidence.InstrumentAdapter) *testEnv {
	t.Helper()
	return newEnvStore(t, store.NewMemory(), adapters)
}

func newEnvStore(t *testing.T, st store.Store, adapters map[evidence.InstrumentType]evidence.InstrumentAdapter) *testEnv {
	t.Helper()
	cat := catalog.Seed(time.Now())
	clk := &fakeClock{t: 1}
	svc := New(st, cat, clk, adapters)
	return &testEnv{svc: svc, cat: cat, store: st, clock: clk}
}

func defaultLockInput(barrel, seal string) LockInput {
	return LockInput{
		Operation:   inspection.OperationID("lock-" + barrel),
		Farm:        "farm-01",
		Season:      "spring-2026",
		BatchID:     "batch-01",
		Barrel:      barrel,
		Seal:        seal,
		Zone:        "4C",
		BlindCode:   "BC-" + barrel,
		Slides:      []string{"SL-1"},
		Well:        "W-" + barrel,
		TankSlot:    "TS-" + barrel,
		Samplers:    []catalog.PersonnelID{"alice", "bob"},
		Reviewers:   []catalog.PersonnelID{"carol", "dave"},
		RuleVersion: 1,
	}
}

// lock runs a lock and returns the resulting task id and generation.
func (e *testEnv) lock(t *testing.T, in LockInput) (inspection.TaskID, inspection.Generation) {
	t.Helper()
	res, err := e.svc.Lock(context.Background(), in)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	return res.TaskID, res.Generation
}

// advanceTo drives a task from pending-sampling-confirm to the given state
// using standard valid data derived from the task's frozen identifiers.
func (e *testEnv) advanceTo(t *testing.T, id inspection.TaskID, gen inspection.Generation, target inspection.State) {
	t.Helper()
	ctx := context.Background()
	cur := e.currentState(t, id)
	task, err := e.svc.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	barrel, seal, blind, tank, well := task.Barrel, task.Seal, task.BlindCode, task.TankSlot, task.Well
	slides := task.Slides
	slide := "SL-1"
	if len(slides) > 0 {
		slide = slides[0]
	}

	for cur != target {
		switch cur {
		case inspection.StatePendingSamplingConfirm:
			if err := e.svc.ConfirmSampling(ctx, ConfirmSamplingInput{
				Operation: inspection.OperationID(fmt.Sprintf("%s-confirm", id)), TaskID: id, Generation: gen,
				Samplers: []catalog.PersonnelID{"alice", "bob"}, Barrel: barrel, Seal: seal,
			}); err != nil {
				t.Fatalf("ConfirmSampling: %v", err)
			}
		case inspection.StateSealingSamples:
			if err := e.svc.SealSamples(ctx, SealSamplesInput{
				Operation: inspection.OperationID(fmt.Sprintf("%s-seal", id)), TaskID: id, Generation: gen,
				BlindCode: ledger.BlindCode(blind), Triplicates: []string{"T-1", "T-2", "T-3"}, SealedBy: "alice",
			}); err != nil {
				t.Fatalf("SealSamples: %v", err)
			}
		case inspection.StateClaimingResources:
			if err := e.svc.ClaimLeases(ctx, ClaimLeasesInput{
				Operation: inspection.OperationID(fmt.Sprintf("%s-claim", id)), TaskID: id, Generation: gen,
				TankSlot: tank, Well: well, Slides: slides,
			}); err != nil {
				t.Fatalf("ClaimLeases: %v", err)
			}
		case inspection.StateCountingPollen:
			if err := e.svc.CountPollen(ctx, CountPollenInput{
				Operation: inspection.OperationID(fmt.Sprintf("%s-pollen", id)), TaskID: id, Generation: gen,
				DeclaredTotal: 140, EnteredBy: "alice",
				Counts: []evidence.PollenCountInput{
					{Slide: slide, Class: catalog.PollenTarget, Count: 120},
					{Slide: slide, Class: catalog.PollenAccompanying, Count: 10},
					{Slide: slide, Class: catalog.PollenUnknown, Count: 5},
					{Slide: slide, Class: catalog.PollenContaminant, Count: 5},
				},
			}); err != nil {
				t.Fatalf("CountPollen: %v", err)
			}
		case inspection.StateVerifyingDNA:
			if err := e.svc.SubmitDNA(ctx, SubmitDNAInput{
				Operation: inspection.OperationID(fmt.Sprintf("%s-dna", id)), TaskID: id, Generation: gen,
				BlindCode: blind, Slide: slide, Well: well, Reading: "30.00",
			}); err != nil {
				t.Fatalf("SubmitDNA: %v", err)
			}
		case inspection.StateRetestingChemistry:
			if err := e.svc.SubmitChemistry(ctx, SubmitChemistryInput{
				Operation: inspection.OperationID(fmt.Sprintf("%s-chem", id)), TaskID: id, Generation: gen,
				BlindCode: blind, Slide: slide, Well: well,
				HMF: "20.0", Amylase: "12.0", Moisture: "16.0", Conductivity: "0.50", Acidity: "30.0",
			}); err != nil {
				t.Fatalf("SubmitChemistry: %v", err)
			}
		case inspection.StatePendingIndependentReview:
			if err := e.svc.AddReview(ctx, AddReviewInput{
				Operation: inspection.OperationID(fmt.Sprintf("%s-review-a", id)), TaskID: id, Generation: gen, Reviewer: "carol", Scope: "closed",
			}); err != nil {
				t.Fatalf("AddReview carol: %v", err)
			}
			if err := e.svc.AddReview(ctx, AddReviewInput{
				Operation: inspection.OperationID(fmt.Sprintf("%s-review-b", id)), TaskID: id, Generation: gen, Reviewer: "dave", Scope: "closed",
			}); err != nil {
				t.Fatalf("AddReview dave: %v", err)
			}
		default:
			t.Fatalf("advanceTo: cannot advance from %s", cur)
		}
		cur = e.currentState(t, id)
	}
}

func (e *testEnv) currentState(t *testing.T, id inspection.TaskID) inspection.State {
	t.Helper()
	task, err := e.svc.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return task.State
}
