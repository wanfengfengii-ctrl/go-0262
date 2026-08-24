package service

import (
	"context"

	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/store"
)

// ClaimLeasesInput is the资源占用 payload.
type ClaimLeasesInput struct {
	Operation  inspection.OperationID
	TaskID     inspection.TaskID
	Generation inspection.Generation
	TankSlot   string
	Well       string
	Slides     []string
}

// ClaimLeases claims the maturation-tank slot, DNA plate well and slide
// numbers for a task. The resources must match the identifiers frozen at lock
// time, and each must be free; the whole claim is atomic so a conflict on any
// single resource leaves no partial lease behind.
func (s *Service) ClaimLeases(ctx context.Context, in ClaimLeasesInput) error {
	hash := store.HashContent(in)
	if replayed, _, err := s.checkReplay(ctx, in.Operation, hash); err != nil {
		return err
	} else if replayed {
		return nil
	}

	t, _, err := s.ruleForTask(ctx, in.TaskID)
	if err != nil {
		return err
	}
	if err := t.MustBeGeneration(in.Generation); err != nil {
		return err
	}
	if err := t.MustNotBeTerminal(); err != nil {
		return err
	}
	if err := t.MustBeState(inspection.StateClaimingResources); err != nil {
		return err
	}
	if in.TankSlot != t.TankSlot || in.Well != t.Well || !equalStrings(in.Slides, t.Slides) {
		return ErrBadRequest
	}

	now := s.clock.Now()
	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		leases := []ledger.ResourceLease{
			{ResourceType: ledger.ResourceTankSlot, ResourceID: in.TankSlot, TaskID: in.TaskID, Generation: in.Generation, Status: ledger.LeaseActive, AcquiredAt: now},
			{ResourceType: ledger.ResourceWell, ResourceID: in.Well, TaskID: in.TaskID, Generation: in.Generation, Status: ledger.LeaseActive, AcquiredAt: now},
		}
		for _, slide := range in.Slides {
			leases = append(leases, ledger.ResourceLease{ResourceType: ledger.ResourceSlide, ResourceID: slide, TaskID: in.TaskID, Generation: in.Generation, Status: ledger.LeaseActive, AcquiredAt: now})
		}
		for _, l := range leases {
			if err := tx.SaveLease(ctx, l); err != nil {
				return mapStoreErr(err)
			}
		}
		return tx.UpdateTaskState(ctx, in.TaskID, in.Generation, inspection.StateClaimingResources, inspection.StateCountingPollen)
	})
}

// SwapWellInput is the换孔 payload.
type SwapWellInput struct {
	Operation  inspection.OperationID
	TaskID     inspection.TaskID
	Generation inspection.Generation
	OldWell    string
	NewWell    string
}

// SwapWell atomically replaces the DNA plate well lease. If the new well is
// already occupied the transaction rolls back and the old lease is untouched.
func (s *Service) SwapWell(ctx context.Context, in SwapWellInput) error {
	hash := store.HashContent(in)
	if replayed, _, err := s.checkReplay(ctx, in.Operation, hash); err != nil {
		return err
	} else if replayed {
		return nil
	}

	t, _, err := s.ruleForTask(ctx, in.TaskID)
	if err != nil {
		return err
	}
	if err := t.MustBeGeneration(in.Generation); err != nil {
		return err
	}
	if err := t.MustNotBeTerminal(); err != nil {
		return err
	}
	if in.OldWell == "" || in.NewWell == "" || in.OldWell == in.NewWell {
		return ErrBadRequest
	}

	now := s.clock.Now()
	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		if err := tx.SaveLease(ctx, ledger.ResourceLease{
			ResourceType: ledger.ResourceWell, ResourceID: in.NewWell, TaskID: in.TaskID,
			Generation: in.Generation, Status: ledger.LeaseActive, AcquiredAt: now,
		}); err != nil {
			return mapStoreErr(err)
		}
		if err := tx.SaveLease(ctx, ledger.ResourceLease{
			ResourceType: ledger.ResourceWell, ResourceID: in.OldWell, TaskID: in.TaskID,
			Generation: in.Generation, Status: ledger.LeaseReleased, AcquiredAt: now, ReleasedAt: now,
		}); err != nil {
			return err
		}
		return nil
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
