package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/store"
)

func TestModel_ReleasedLeaseHistorySurvivesResourceReuse(t *testing.T) {
	ctx := context.Background()
	sharedTank := "TS-history-reuse"
	sharedWell := "W-history-reuse"
	sharedSlide := "SL-history-reuse"
	expected := []struct {
		typ ledger.ResourceType
		id  string
	}{
		{ledger.ResourceTankSlot, sharedTank},
		{ledger.ResourceWell, sharedWell},
		{ledger.ResourceSlide, sharedSlide},
	}

	cases := []struct {
		name string
		open func(*testing.T) store.Store
	}{
		{
			name: "memory",
			open: func(t *testing.T) store.Store {
				t.Helper()
				return store.NewMemory()
			},
		},
		{
			name: "sqlite",
			open: func(t *testing.T) store.Store {
				t.Helper()
				st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "nectargate.db"))
				if err != nil {
					t.Fatalf("OpenSQLite: %v", err)
				}
				return st
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.open(t)
			t.Cleanup(func() { _ = st.Close() })
			e := newEnvStore(t, st, nil)

			sharedInput := func(barrel, seal string) LockInput {
				in := defaultLockInput(barrel, seal)
				in.TankSlot = sharedTank
				in.Well = sharedWell
				in.Slides = []string{sharedSlide}
				return in
			}
			assertLeases := func(t *testing.T, taskID inspection.TaskID, gen inspection.Generation, status ledger.LeaseStatus) {
				t.Helper()
				leases, err := st.LoadLeases(ctx, taskID)
				if err != nil {
					t.Fatalf("LoadLeases(%s): %v", taskID, err)
				}
				if len(leases) != len(expected) {
					t.Fatalf("LoadLeases(%s) returned %d leases, want %d: %+v", taskID, len(leases), len(expected), leases)
				}

				byResource := make(map[string]ledger.ResourceLease, len(leases))
				for _, lease := range leases {
					if lease.TaskID != taskID {
						t.Fatalf("LoadLeases(%s) returned lease for task %s: %+v", taskID, lease.TaskID, lease)
					}
					if lease.Generation != gen {
						t.Fatalf("lease generation = %d, want %d: %+v", lease.Generation, gen, lease)
					}
					byResource[string(lease.ResourceType)+":"+lease.ResourceID] = lease
				}
				for _, want := range expected {
					lease, ok := byResource[string(want.typ)+":"+want.id]
					if !ok {
						t.Fatalf("missing %s lease %q in %+v", want.typ, want.id, leases)
					}
					if lease.Status != status {
						t.Fatalf("%s %q status = %v, want %v: %+v", want.typ, want.id, lease.Status, status, lease)
					}
					if status == ledger.LeaseReleased && lease.ReleasedAt == 0 {
						t.Fatalf("%s %q was released without a release time: %+v", want.typ, want.id, lease)
					}
					if status == ledger.LeaseActive && lease.ReleasedAt != 0 {
						t.Fatalf("%s %q active lease has release time: %+v", want.typ, want.id, lease)
					}
				}
			}
			claimShared := func(t *testing.T, op string, taskID inspection.TaskID, gen inspection.Generation) error {
				t.Helper()
				return e.svc.ClaimLeases(ctx, ClaimLeasesInput{
					Operation:  inspection.OperationID(op),
					TaskID:     taskID,
					Generation: gen,
					TankSlot:   sharedTank,
					Well:       sharedWell,
					Slides:     []string{sharedSlide},
				})
			}
			finalize := func(t *testing.T, op string, taskID inspection.TaskID, gen inspection.Generation) {
				t.Helper()
				if _, err := e.svc.Finalize(ctx, FinalizeInput{
					Operation:  inspection.OperationID(op),
					TaskID:     taskID,
					Generation: gen,
					Reviewer:   "carol",
				}); err != nil {
					t.Fatalf("Finalize(%s): %v", taskID, err)
				}
			}

			oldID, oldGen := e.lock(t, sharedInput("B-history-old", "S-history-old"))
			e.advanceTo(t, oldID, oldGen, inspection.StateReadyForMaturation)
			finalize(t, "final-old-"+tc.name, oldID, oldGen)
			assertLeases(t, oldID, oldGen, ledger.LeaseReleased)

			nextID, nextGen := e.lock(t, sharedInput("B-history-next", "S-history-next"))
			e.advanceTo(t, nextID, nextGen, inspection.StateClaimingResources)
			if err := claimShared(t, "claim-next-"+tc.name, nextID, nextGen); err != nil {
				t.Fatalf("reclaiming released resources for next task: %v", err)
			}
			assertLeases(t, nextID, nextGen, ledger.LeaseActive)
			assertLeases(t, oldID, oldGen, ledger.LeaseReleased)

			blockedID, blockedGen := e.lock(t, sharedInput("B-history-blocked", "S-history-blocked"))
			e.advanceTo(t, blockedID, blockedGen, inspection.StateClaimingResources)
			if err := claimShared(t, "claim-blocked-"+tc.name, blockedID, blockedGen); !errors.Is(err, ErrResourceOccupied) {
				t.Fatalf("claim while next task is active got %v, want ErrResourceOccupied", err)
			}
			assertLeases(t, nextID, nextGen, ledger.LeaseActive)

			e.advanceTo(t, nextID, nextGen, inspection.StateReadyForMaturation)
			finalize(t, "final-next-"+tc.name, nextID, nextGen)
			assertLeases(t, nextID, nextGen, ledger.LeaseReleased)
			assertLeases(t, oldID, oldGen, ledger.LeaseReleased)

			if err := claimShared(t, "claim-after-next-release-"+tc.name, blockedID, blockedGen); err != nil {
				t.Fatalf("claiming after next task released resources: %v", err)
			}
			assertLeases(t, blockedID, blockedGen, ledger.LeaseActive)
			assertLeases(t, oldID, oldGen, ledger.LeaseReleased)
			assertLeases(t, nextID, nextGen, ledger.LeaseReleased)
		})
	}
}
