// Package arbiter maintains the DNA fingerprint rejudgement, the independent
// review records and the unique terminal credential.
//
// Component: 花源指纹复判及终局仲裁器.
package arbiter

import (
	"errors"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
)

// FinalType is the unique terminal conclusion.
type FinalType string

const (
	FinalMatured     FinalType = "matured"     // 入熟化
	FinalQuarantined FinalType = "quarantined" // 质量隔离
	FinalCancelled   FinalType = "cancelled"   // 已取消
)

// ReviewAndFinal records an independent review and, when present, the single
// terminal credential produced by the write barrier.
type ReviewAndFinal struct {
	TaskID       inspection.TaskID
	Reviewer     catalog.PersonnelID
	ScopeSummary string
	RejudgeGen   inspection.Generation
	FinalType    FinalType
	CredentialID string
	// BarrierKey is the single-write barrier key (单写屏障键).
	BarrierKey string
}

// Sentinel errors for the arbitration boundary.
var (
	ErrNotEnoughReviewers = errors.New("arbiter: need two distinct qualified reviewers")
	ErrReviewerOverlap    = errors.New("arbiter: sampler and reviewer overlap")
	ErrReviewerDuplicate  = errors.New("arbiter: duplicate reviewer")
	ErrIncompleteEvidence = errors.New("arbiter: evidence not closed")
	ErrFinalAlreadySet    = errors.New("arbiter: terminal conclusion already set")
)
