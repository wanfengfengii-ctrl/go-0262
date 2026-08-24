package service

import (
	"context"
	"errors"
	"testing"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
)

func TestModel_FinalizeLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name               string
		finalizeFirst      bool
		decision           arbiter.Decision
		wantFinal          arbiter.FinalType
		wantState          inspection.State
		wantSecondClaimErr error
	}{
		{
			name:          "matured_final_releases_resources",
			finalizeFirst: true,
			decision:      arbiter.DecisionMature,
			wantFinal:     arbiter.FinalMatured,
			wantState:     inspection.StateMatured,
		},
		{
			name:          "quarantined_final_releases_resources",
			finalizeFirst: true,
			decision:      arbiter.DecisionQuarantine,
			wantFinal:     arbiter.FinalQuarantined,
			wantState:     inspection.StateQuarantined,
		},
		{
			name:          "cancelled_final_releases_resources",
			finalizeFirst: true,
			decision:      arbiter.DecisionCancel,
			wantFinal:     arbiter.FinalCancelled,
			wantState:     inspection.StateCancelled,
		},
		{
			name:               "active_ready_task_keeps_resources_exclusive",
			wantSecondClaimErr: ErrResourceOccupied,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			tankSlot := "TS-reused"
			well := "W-reused"
			slides := []string{"SL-reused"}

			first := defaultLockInput("B-"+tc.name+"-1", "S-"+tc.name+"-1")
			first.TankSlot = tankSlot
			first.Well = well
			first.Slides = slides
			firstID, firstGen := e.lock(t, first)
			e.advanceTo(t, firstID, firstGen, inspection.StateReadyForMaturation)

			leases, err := e.store.LoadLeases(ctx, firstID)
			if err != nil {
				t.Fatalf("LoadLeases before finalize: %v", err)
			}
			activeBefore := 0
			for _, lease := range leases {
				if lease.Status == ledger.LeaseActive {
					activeBefore++
				}
			}
			if activeBefore != 3 {
				t.Fatalf("active leases before finalize = %d, want 3", activeBefore)
			}

			if tc.finalizeFirst {
				got, err := e.svc.Finalize(ctx, FinalizeInput{
					Operation:  inspection.OperationID("final-" + tc.name),
					TaskID:     firstID,
					Generation: firstGen,
					Reviewer:   "carol",
					Decision:   tc.decision,
				})
				if err != nil {
					t.Fatalf("Finalize: %v", err)
				}
				if got.FinalType != tc.wantFinal {
					t.Fatalf("final type = %s, want %s", got.FinalType, tc.wantFinal)
				}
				if got.CredentialID == "" {
					t.Fatal("Finalize returned empty credential")
				}
				if state := e.currentState(t, firstID); state != tc.wantState {
					t.Fatalf("state = %s, want %s", state, tc.wantState)
				}

				leases, err = e.store.LoadLeases(ctx, firstID)
				if err != nil {
					t.Fatalf("LoadLeases after finalize: %v", err)
				}
				for _, lease := range leases {
					if lease.Status == ledger.LeaseActive {
						t.Fatalf("lease remained active after terminal finalize: %+v", lease)
					}
					if lease.ReleasedAt == 0 {
						t.Fatalf("released lease has no release time: %+v", lease)
					}
				}
			}

			second := defaultLockInput("B-"+tc.name+"-2", "S-"+tc.name+"-2")
			second.TankSlot = tankSlot
			second.Well = well
			second.Slides = slides
			secondID, secondGen := e.lock(t, second)
			e.advanceTo(t, secondID, secondGen, inspection.StateClaimingResources)

			err = e.svc.ClaimLeases(ctx, ClaimLeasesInput{
				Operation:  inspection.OperationID("second-claim-" + tc.name),
				TaskID:     secondID,
				Generation: secondGen,
				TankSlot:   tankSlot,
				Well:       well,
				Slides:     slides,
			})
			if tc.wantSecondClaimErr == nil {
				if err != nil {
					t.Fatalf("second ClaimLeases after terminal finalize: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantSecondClaimErr) {
				t.Fatalf("second ClaimLeases got %v, want %v", err, tc.wantSecondClaimErr)
			}
		})
	}
}
