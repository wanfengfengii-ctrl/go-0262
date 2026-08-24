package arbiter

import (
	"errors"
	"fmt"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
)

// Decision is the caller's requested terminal conclusion. An empty decision
// asks the arbiter to compute the conclusion from the evidence.
type Decision string

const (
	DecisionAuto       Decision = ""           // compute from evidence
	DecisionMature     Decision = "mature"     // 入熟化
	DecisionQuarantine Decision = "quarantine" // 质量隔离
	DecisionCancel     Decision = "cancel"     // 取消
)

// ErrUnknownDecision reports an unsupported finalize decision.
var ErrUnknownDecision = errors.New("arbiter: unknown decision")

// ErrReviewerNotInList reports a reviewer who is not on the task's list.
var ErrReviewerNotInList = errors.New("arbiter: reviewer not in task list")

// ValidateReview enforces the independent-review rules for a new review:
// the reviewer must be a qualified task reviewer, must not be a sampler, and
// must not have already reviewed the task.
func ValidateReview(
	samplers []catalog.PersonnelID,
	taskReviewers []catalog.PersonnelID,
	existing []ReviewAndFinal,
	reviewer catalog.PersonnelID,
) error {
	inList := false
	for _, r := range taskReviewers {
		if r == reviewer {
			inList = true
			break
		}
	}
	if !inList {
		return ErrReviewerNotInList
	}
	for _, s := range samplers {
		if s == reviewer {
			return ErrReviewerOverlap
		}
	}
	for _, e := range existing {
		if e.Reviewer == reviewer {
			return ErrReviewerDuplicate
		}
	}
	return nil
}

// DistinctReviewers returns the distinct reviewer identities already recorded.
func DistinctReviewers(existing []ReviewAndFinal) []catalog.PersonnelID {
	seen := make(map[catalog.PersonnelID]bool)
	out := make([]catalog.PersonnelID, 0, len(existing))
	for _, e := range existing {
		if e.Reviewer != "" && !seen[e.Reviewer] {
			seen[e.Reviewer] = true
			out = append(out, e.Reviewer)
		}
	}
	return out
}

// ResolveConclusion maps a requested decision and the anomaly flag to a unique
// terminal conclusion. Cancellation is always allowed; an explicit quarantine
// or mature decision is honoured; the auto decision uses the anomaly flag.
func ResolveConclusion(decision Decision, anomalous bool) (FinalType, error) {
	switch decision {
	case DecisionCancel:
		return FinalCancelled, nil
	case DecisionQuarantine:
		return FinalQuarantined, nil
	case DecisionMature:
		if anomalous {
			return "", ErrIncompleteEvidence
		}
		return FinalMatured, nil
	case DecisionAuto:
		if anomalous {
			return FinalQuarantined, nil
		}
		return FinalMatured, nil
	default:
		return "", ErrUnknownDecision
	}
}

// BarrierKey builds the single-write barrier key for a task generation. The
// database unique index plus this deterministic key guarantee that concurrent
// finalize calls produce exactly one terminal conclusion.
func BarrierKey(taskID inspection.TaskID, gen inspection.Generation) string {
	return fmt.Sprintf("%s:%d", taskID, gen)
}

// NewReview assembles an independent review record scoped to a task.
func NewReview(taskID inspection.TaskID, reviewer catalog.PersonnelID, scope string, rejudgeGen inspection.Generation) ReviewAndFinal {
	return ReviewAndFinal{
		TaskID:       taskID,
		Reviewer:     reviewer,
		ScopeSummary: scope,
		RejudgeGen:   rejudgeGen,
	}
}

// NewFinal assembles the terminal ReviewAndFinal record with a credential id.
func NewFinal(
	taskID inspection.TaskID,
	reviewer catalog.PersonnelID,
	scope string,
	rejudgeGen inspection.Generation,
	ft FinalType,
	credentialID string,
	barrierKey string,
) ReviewAndFinal {
	return ReviewAndFinal{
		TaskID:       taskID,
		Reviewer:     reviewer,
		ScopeSummary: scope,
		RejudgeGen:   rejudgeGen,
		FinalType:    ft,
		CredentialID: credentialID,
		BarrierKey:   barrierKey,
	}
}
