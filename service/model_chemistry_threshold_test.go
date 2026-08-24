package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
)

func TestModel_ChemistryThresholdViolationsBecomeFailedEvidence(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name               string
		hmf                string
		wantSubmitErr      error
		wantChemConclusion string
		wantChemReading    string
		wantFinal          arbiter.FinalType
	}{
		{
			name:               "clean chemistry reading passes and matures",
			hmf:                "40.0",
			wantChemConclusion: "pass",
			wantChemReading:    "40.0",
			wantFinal:          arbiter.FinalMatured,
		},
		{
			name:               "valid hmf threshold violation enters review and quarantines",
			hmf:                "40.1",
			wantChemConclusion: "fail",
			wantChemReading:    "40.1",
			wantFinal:          arbiter.FinalQuarantined,
		},
		{
			name:          "bad hmf fixed decimal is rejected without evidence",
			hmf:           "40.01",
			wantSubmitErr: ErrInvalidReading,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			id, gen := e.lock(t, defaultLockInput(fmt.Sprintf("B-MODEL-%d", i), fmt.Sprintf("S-MODEL-%d", i)))
			e.advanceTo(t, id, gen, inspection.StateRetestingChemistry)

			err := e.svc.SubmitChemistry(ctx, SubmitChemistryInput{
				Operation:    inspection.OperationID(fmt.Sprintf("model-chem-%d", i)),
				TaskID:       id,
				Generation:   gen,
				BlindCode:    fmt.Sprintf("BC-B-MODEL-%d", i),
				Slide:        "SL-1",
				Well:         fmt.Sprintf("W-B-MODEL-%d", i),
				HMF:          tc.hmf,
				Amylase:      "12.0",
				Moisture:     "16.0",
				Conductivity: "0.50",
				Acidity:      "30.0",
			})

			if tc.wantSubmitErr != nil {
				if !errors.Is(err, tc.wantSubmitErr) {
					t.Fatalf("SubmitChemistry error = %v, want %v", err, tc.wantSubmitErr)
				}
				if got := e.currentState(t, id); got != inspection.StateRetestingChemistry {
					t.Fatalf("state after rejected chemistry = %s, want %s", got, inspection.StateRetestingChemistry)
				}
				chain, err := e.store.LoadEvidence(ctx, id, gen)
				if err != nil {
					t.Fatalf("LoadEvidence: %v", err)
				}
				for _, ev := range chain {
					if ev.Type == evidence.EvidenceChemistry {
						t.Fatalf("rejected chemistry reading wrote evidence: %+v", ev)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("SubmitChemistry: %v", err)
			}
			if got := e.currentState(t, id); got != inspection.StatePendingIndependentReview {
				t.Fatalf("state after chemistry = %s, want %s", got, inspection.StatePendingIndependentReview)
			}

			chain, err := e.store.LoadEvidence(ctx, id, gen)
			if err != nil {
				t.Fatalf("LoadEvidence: %v", err)
			}
			var chem *evidence.EvidenceVersion
			for i := range chain {
				if chain[i].Type == evidence.EvidenceChemistry {
					chem = &chain[i]
				}
			}
			if chem == nil {
				t.Fatal("chemistry evidence was not written")
			}
			if chem.Conclusion != tc.wantChemConclusion {
				t.Fatalf("chemistry conclusion = %q, want %q", chem.Conclusion, tc.wantChemConclusion)
			}
			if !chem.Immutable {
				t.Fatal("chemistry evidence is mutable")
			}
			if got := chem.Reading.String(); got != tc.wantChemReading {
				t.Fatalf("chemistry reading = %s, want %s", got, tc.wantChemReading)
			}

			e.advanceTo(t, id, gen, inspection.StateReadyForMaturation)
			res, err := e.svc.Finalize(ctx, FinalizeInput{
				Operation:  inspection.OperationID(fmt.Sprintf("model-final-%d", i)),
				TaskID:     id,
				Generation: gen,
				Reviewer:   "carol",
			})
			if err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			if res.FinalType != tc.wantFinal {
				t.Fatalf("final = %s, want %s", res.FinalType, tc.wantFinal)
			}
		})
	}
}
