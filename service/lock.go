package service

import (
	"context"
	"encoding/json"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/store"
)

// LockInput is the建检锁定 payload.
type LockInput struct {
	Operation   inspection.OperationID
	Farm        catalog.FarmID
	Season      catalog.NectarSeason
	BatchID     string
	Barrel      string
	Seal        string
	Zone        catalog.TemporaryZone
	BlindCode   string
	Slides      []string
	Well        string
	TankSlot    string
	Samplers    []catalog.PersonnelID
	Reviewers   []catalog.PersonnelID
	RuleVersion int64
}

// LockResult is the outcome of a successful lock.
type LockResult struct {
	TaskID     inspection.TaskID
	Generation inspection.Generation
	State      inspection.State
	Occupied   []string
}

// Lock validates the catalog, personnel roles and resource uniqueness, then
// persists a new task in pending-sampling-confirm state. The barrel and seal
// identifiers are atomically occupied; any duplicate fails the transaction.
func (s *Service) Lock(ctx context.Context, in LockInput) (LockResult, error) {
	hash := store.HashContent(struct {
		Farm, Season, BatchID, Barrel, Seal, Zone, BlindCode string
		Slides                                               []string
		Well, TankSlot                                       string
		Samplers, Reviewers                                  []catalog.PersonnelID
	}{string(in.Farm), string(in.Season), in.BatchID, in.Barrel, in.Seal, string(in.Zone), in.BlindCode, in.Slides, in.Well, in.TankSlot, in.Samplers, in.Reviewers})

	if replayed, resultJSON, err := s.checkReplay(ctx, in.Operation, hash); err != nil {
		return LockResult{}, err
	} else if replayed {
		var res LockResult
		if json.Unmarshal([]byte(resultJSON), &res) == nil {
			return res, nil
		}
		return LockResult{}, nil
	}

	r, err := s.rule(in.Farm)
	if err != nil {
		return LockResult{}, err
	}
	if !s.catalog.IsFresh(in.RuleVersion) {
		return LockResult{}, catalog.ErrStaleRule
	}

	now := s.clock.Now()
	if err := r.ValidateLock(in.Season, in.Zone, time.Now()); err != nil {
		return LockResult{}, err
	}
	if err := r.ValidatePersonnel(append(append([]catalog.PersonnelID{}, in.Samplers...), in.Reviewers...)...); err != nil {
		return LockResult{}, err
	}
	if err := inspection.ValidateSeparation(in.Samplers, in.Reviewers); err != nil {
		return LockResult{}, err
	}

	task := inspection.InspectionTask{
		Farm:            r.Farm,
		Season:          r.NectarSeason,
		BatchSummary:    r.Summary,
		Barrel:          in.Barrel,
		Seal:            in.Seal,
		Zone:            in.Zone,
		BlindCode:       in.BlindCode,
		Slides:          in.Slides,
		Well:            in.Well,
		TankSlot:        in.TankSlot,
		DNAThreshold:    r.DNA,
		PollenThreshold: r.Pollen,
		ChemThreshold:   r.Chemistry,
		Samplers:        in.Samplers,
		Reviewers:       in.Reviewers,
		State:           inspection.StatePendingSamplingConfirm,
		CreatedAt:       now,
	}

	lockReq := inspection.LockRequest{
		Operation:   in.Operation,
		Farm:        r.Farm,
		Season:      r.NectarSeason,
		Summary:     r.Summary,
		Barrel:      in.Barrel,
		Seal:        in.Seal,
		Zone:        in.Zone,
		BlindCode:   in.BlindCode,
		Slides:      in.Slides,
		Well:        in.Well,
		TankSlot:    in.TankSlot,
		Samplers:    in.Samplers,
		Reviewers:   in.Reviewers,
		RuleVersion: in.RuleVersion,
	}

	var created inspection.InspectionTask
	err = s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		var cerr error
		created, cerr = tx.CreateTask(ctx, lockReq, task)
		if cerr != nil {
			return mapStoreErr(cerr)
		}
		return nil
	})
	if err != nil {
		return LockResult{}, err
	}

	return LockResult{
		TaskID:     created.ID,
		Generation: created.Generation,
		State:      created.State,
		Occupied:   []string{in.Barrel, in.Seal},
	}, nil
}

func mapStoreErr(err error) error {
	if err == store.ErrDuplicate {
		return ErrResourceOccupied
	}
	return err
}
