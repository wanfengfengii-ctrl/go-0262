package service

import (
	"context"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/store"
)

// ConfirmSamplingInput is the双人抽样确认 payload.
type ConfirmSamplingInput struct {
	Operation  inspection.OperationID
	TaskID     inspection.TaskID
	Generation inspection.Generation
	Samplers   []catalog.PersonnelID
	Barrel     string
	Seal       string
}

// ConfirmSampling advances a task from pending-sampling-confirm to sealing.
// Two distinct samplers must match the frozen sampler list and the barrel and
// seal they confirm must equal the frozen identifiers.
func (s *Service) ConfirmSampling(ctx context.Context, in ConfirmSamplingInput) error {
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
	if err := t.MustBeState(inspection.StatePendingSamplingConfirm); err != nil {
		return err
	}
	if len(in.Samplers) != 2 || in.Samplers[0] == in.Samplers[1] {
		return inspection.ErrSamplerCount
	}
	for _, sub := range in.Samplers {
		if !containsPerson(t.Samplers, sub) {
			return ErrUnqualified
		}
	}
	if in.Barrel != t.Barrel || in.Seal != t.Seal {
		return ErrBadRequest
	}

	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		return tx.UpdateTaskState(ctx, in.TaskID, in.Generation, inspection.StatePendingSamplingConfirm, inspection.StateSealingSamples)
	})
}

// SealSamplesInput is the三联分管封存 payload.
type SealSamplesInput struct {
	Operation   inspection.OperationID
	TaskID      inspection.TaskID
	Generation  inspection.Generation
	BlindCode   ledger.BlindCode
	Triplicates []string
	SealedBy    catalog.PersonnelID
}

// SealSamples establishes the one-time blind-code mapping and the triplicate
// consistency matrix, advancing the task to claiming-resources.
func (s *Service) SealSamples(ctx context.Context, in SealSamplesInput) error {
	hash := store.HashContent(in)
	if replayed, _, err := s.checkReplay(ctx, in.Operation, hash); err != nil {
		return err
	} else if replayed {
		return nil
	}

	t, rule, err := s.ruleForTask(ctx, in.TaskID)
	if err != nil {
		return err
	}
	if err := t.MustBeGeneration(in.Generation); err != nil {
		return err
	}
	if err := t.MustNotBeTerminal(); err != nil {
		return err
	}
	if err := t.MustBeState(inspection.StateSealingSamples); err != nil {
		return err
	}
	if in.BlindCode == "" {
		return ErrBadRequest
	}
	if t.BlindCode != "" && in.BlindCode != ledger.BlindCode(t.BlindCode) {
		return ledger.ErrBlindCodeMismatch
	}
	if err := ledger.ValidateTriplicates(in.Triplicates); err != nil {
		return err
	}
	if err := rule.ValidatePersonnel(in.SealedBy); err != nil {
		return ErrUnqualified
	}

	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		if err := tx.SaveBlindSample(ctx, ledger.BlindSampleMap{
			TaskID:        in.TaskID,
			Barrel:        t.Barrel,
			BlindCode:     in.BlindCode,
			RevealState:   ledger.RevealSealed,
			TriplicateIDs: in.Triplicates,
			SealedBy:      in.SealedBy,
			SealedAt:      s.clock.Now(),
		}); err != nil {
			return mapStoreErr(err)
		}
		return tx.UpdateTaskState(ctx, in.TaskID, in.Generation, inspection.StateSealingSamples, inspection.StateClaimingResources)
	})
}

// RevealInput triggers a blind-code reveal (揭盲).
type RevealInput struct {
	TaskID     inspection.TaskID
	Generation inspection.Generation
}

// Reveal flips a sealed blind code to revealed, but only once the task has
// advanced to a stage where attribution is valid (DNA verification onward).
// A reveal before that stage is rejected as premature.
func (s *Service) Reveal(ctx context.Context, in RevealInput) error {
	t, _, err := s.ruleForTask(ctx, in.TaskID)
	if err != nil {
		return err
	}
	if err := t.MustBeGeneration(in.Generation); err != nil {
		return err
	}
	switch t.State {
	case inspection.StateVerifyingDNA,
		inspection.StateRetestingChemistry,
		inspection.StatePendingIndependentReview,
		inspection.StateReadyForMaturation:
	default:
		return ledger.ErrPrematureReveal
	}
	return s.store.WithTx(ctx, func(tx store.Tx) error {
		return tx.RevealBlind(ctx, in.TaskID, in.Generation)
	})
}

func containsPerson(list []catalog.PersonnelID, p catalog.PersonnelID) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}
