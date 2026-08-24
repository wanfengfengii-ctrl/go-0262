package service

import (
	"context"
	"encoding/json"
	"fmt"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/store"
)

// AddReviewInput is the独立复核 payload.
type AddReviewInput struct {
	Operation  inspection.OperationID
	TaskID     inspection.TaskID
	Generation inspection.Generation
	Reviewer   catalog.PersonnelID
	Scope      string
}

// AddReview records an independent review. When two distinct qualified
// reviewers have reviewed, the task advances to ready-for-maturation.
func (s *Service) AddReview(ctx context.Context, in AddReviewInput) error {
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
	if t.State != inspection.StatePendingIndependentReview && t.State != inspection.StateReadyForMaturation {
		return inspection.ErrWrongState
	}

	existing, err := s.store.LoadReviews(ctx, in.TaskID)
	if err != nil {
		return err
	}
	if err := arbiter.ValidateReview(t.Samplers, t.Reviewers, existing, in.Reviewer); err != nil {
		return err
	}

	newReview := arbiter.NewReview(in.TaskID, in.Reviewer, in.Scope, in.Generation)
	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		if err := tx.SaveReview(ctx, newReview); err != nil {
			return mapStoreErr(err)
		}
		if len(arbiter.DistinctReviewers(append(existing, newReview))) >= 2 &&
			t.State == inspection.StatePendingIndependentReview {
			return tx.UpdateTaskState(ctx, in.TaskID, in.Generation, inspection.StatePendingIndependentReview, inspection.StateReadyForMaturation)
		}
		return nil
	})
}

// FinalizeInput is the终局 payload. Decision may be empty to auto-compute.
type FinalizeInput struct {
	Operation  inspection.OperationID
	TaskID     inspection.TaskID
	Generation inspection.Generation
	Reviewer   catalog.PersonnelID
	Decision   arbiter.Decision
}

// FinalizeResult is the single terminal conclusion.
type FinalizeResult struct {
	FinalType    arbiter.FinalType
	CredentialID string
}

// Finalize competes to produce the unique terminal conclusion. It requires two
// distinct independent reviews, then writes the terminal credential under a
// single-write barrier and transitions the task to its final state.
func (s *Service) Finalize(ctx context.Context, in FinalizeInput) (FinalizeResult, error) {
	hash := store.HashContent(in)
	if replayed, resultJSON, err := s.checkReplay(ctx, in.Operation, hash); err != nil {
		return FinalizeResult{}, err
	} else if replayed {
		var res FinalizeResult
		if json.Unmarshal([]byte(resultJSON), &res) == nil {
			return res, nil
		}
	}

	t, _, err := s.ruleForTask(ctx, in.TaskID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := t.MustBeGeneration(in.Generation); err != nil {
		return FinalizeResult{}, err
	}
	if err := t.MustNotBeTerminal(); err != nil {
		return FinalizeResult{}, err
	}
	if t.State != inspection.StateReadyForMaturation {
		return FinalizeResult{}, inspection.ErrWrongState
	}

	reviews, err := s.store.LoadReviews(ctx, in.TaskID)
	if err != nil {
		return FinalizeResult{}, err
	}
	reviewers := arbiter.DistinctReviewers(reviews)
	if len(reviewers) < 2 {
		return FinalizeResult{}, arbiter.ErrNotEnoughReviewers
	}
	if !containsPerson(reviewers, in.Reviewer) {
		return FinalizeResult{}, arbiter.ErrReviewerNotInList
	}

	chain, err := s.store.LoadEvidence(ctx, in.TaskID, in.Generation)
	if err != nil {
		return FinalizeResult{}, err
	}
	anomalous, _ := evidence.Conclusive(chain, in.Generation)

	ft, err := arbiter.ResolveConclusion(in.Decision, anomalous)
	if err != nil {
		return FinalizeResult{}, err
	}
	finalState := finalStateFor(ft)
	barrier := arbiter.BarrierKey(in.TaskID, in.Generation)
	credential := fmt.Sprintf("cred-%s", barrier)

	result := FinalizeResult{FinalType: ft, CredentialID: credential}
	resultJSON, _ := json.Marshal(result)
	now := s.clock.Now()
	err = s.applyIdempotent(ctx, in.Operation, hash, string(resultJSON), func(tx store.Tx) error {
		if err := tx.SaveFinal(ctx, arbiter.NewFinal(in.TaskID, in.Reviewer, "all-evidence", in.Generation, ft, credential, barrier)); err != nil {
			return err
		}
		if err := tx.UpdateTaskState(ctx, in.TaskID, in.Generation, inspection.StateReadyForMaturation, finalState); err != nil {
			return err
		}
		// A terminal conclusion archives the task: the maturation-tank slot,
		// DNA plate well and slide leases it held are released so a later batch
		// may occupy the same resources. This mirrors the orphan-lease release
		// performed at startup recovery and is the release-on-completion rule.
		return tx.ReleaseLeases(ctx, in.TaskID, now)
	})
	if err != nil {
		return FinalizeResult{}, err
	}
	return result, nil
}

func finalStateFor(ft arbiter.FinalType) inspection.State {
	switch ft {
	case arbiter.FinalMatured:
		return inspection.StateMatured
	case arbiter.FinalQuarantined:
		return inspection.StateQuarantined
	case arbiter.FinalCancelled:
		return inspection.StateCancelled
	default:
		return inspection.StateCancelled
	}
}
