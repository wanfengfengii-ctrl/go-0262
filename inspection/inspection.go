// Package inspection holds the raw-honey maturation intake task aggregate: the
// state machine, task generation, idempotent operation numbers, stage barriers,
// personnel-role constraints and the terminal single-write rule.
//
// Component: 原蜜入熟化任务聚合.
package inspection

import (
	"errors"

	"nectargate/raw-honey-maturation-intake/catalog"
)

// TaskID identifies an inspection task.
type TaskID string

// Generation is the monotonic task generation (任务代次). Late readings from an
// older generation must not affect the current arbitration.
type Generation int64

// LogicalTime is a logical clock tick; all time-driven decisions use it so
// tests can control ordering deterministically.
type LogicalTime int64

// OperationID is an idempotency key submitted by a caller.
type OperationID string

// State is the finite task state. Only the transitions returned by
// NextStates are allowed.
type State int

const (
	StatePendingLock              State = iota // 待锁定
	StatePendingSamplingConfirm                // 待抽样确认
	StateSealingSamples                        // 分管封存中
	StateClaimingResources                     // 资源占用中
	StateCountingPollen                        // 花粉计数中
	StateVerifyingDNA                          // DNA 核验中
	StateRetestingChemistry                    // 理化复测中
	StatePendingIndependentReview              // 待独立复核
	StateReadyForMaturation                    // 可入熟化
	StateMatured                               // 已入熟化
	StateQuarantined                           // 质量隔离
	StateCancelled                             // 已取消
)

// String returns the stable state name.
func (s State) String() string {
	if name, ok := stateNames[s]; ok {
		return name
	}
	return "unknown"
}

var stateNames = map[State]string{
	StatePendingLock:              "pending_lock",
	StatePendingSamplingConfirm:   "pending_sampling_confirm",
	StateSealingSamples:           "sealing_samples",
	StateClaimingResources:        "claiming_resources",
	StateCountingPollen:           "counting_pollen",
	StateVerifyingDNA:             "verifying_dna",
	StateRetestingChemistry:       "retesting_chemistry",
	StatePendingIndependentReview: "pending_independent_review",
	StateReadyForMaturation:       "ready_for_maturation",
	StateMatured:                  "matured",
	StateQuarantined:              "quarantined",
	StateCancelled:                "cancelled",
}

// IsTerminal reports whether s is one of the three final states.
func (s State) IsTerminal() bool {
	switch s {
	case StateMatured, StateQuarantined, StateCancelled:
		return true
	default:
		return false
	}
}

// NextStates returns the allowed next states from s. Every non-terminal state
// may also be cancelled.
func (s State) NextStates() []State {
	if s.IsTerminal() {
		return nil
	}
	next := append([]State{}, transitions[s]...)
	return append(next, StateCancelled)
}

var transitions = map[State][]State{
	StatePendingLock:              {StatePendingSamplingConfirm},
	StatePendingSamplingConfirm:   {StateSealingSamples},
	StateSealingSamples:           {StateClaimingResources},
	StateClaimingResources:        {StateCountingPollen},
	StateCountingPollen:           {StateVerifyingDNA},
	StateVerifyingDNA:             {StateRetestingChemistry},
	StateRetestingChemistry:       {StatePendingIndependentReview},
	StatePendingIndependentReview: {StateReadyForMaturation},
	StateReadyForMaturation:       {StateMatured, StateQuarantined},
}

// CanTransitionTo reports whether s may advance to next.
func (s State) CanTransitionTo(next State) bool {
	for _, n := range s.NextStates() {
		if n == next {
			return true
		}
	}
	return false
}

// InspectionTask is the task aggregate, frozen at lock time.
type InspectionTask struct {
	ID              TaskID
	Generation      Generation
	State           State
	Farm            catalog.FarmID
	Season          catalog.NectarSeason
	BatchSummary    catalog.BatchSummary
	Barrel          string // 原蜜桶号
	Seal            string // 封签编号
	Zone            catalog.TemporaryZone
	BlindCode       string   // planned sampling blind code (抽样盲码)
	Slides          []string // locked slide numbers (玻片编号)
	Well            string   // locked DNA plate well (板孔)
	TankSlot        string   // locked maturation tank slot (熟化罐时隙)
	DNAThreshold    catalog.DNAThreshold
	PollenThreshold catalog.PollenThreshold
	ChemThreshold   catalog.ChemistryThreshold
	Reviewers       []catalog.PersonnelID
	Samplers        []catalog.PersonnelID
	CreatedAt       LogicalTime
}

// LockRequest is the payload for the建检锁定 step.
type LockRequest struct {
	Operation   OperationID
	Farm        catalog.FarmID
	Season      catalog.NectarSeason
	Summary     catalog.BatchSummary
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

// OperationResult describes the outcome of an idempotent operation.
type OperationResult struct {
	Operation OperationID
	Applied   bool // false when a prior identical operation already applied
	Result    any
}

// Sentinel errors for the aggregate boundary.
var (
	ErrTerminalState          = errors.New("inspection: task is terminal")
	ErrInvalidTransition      = errors.New("inspection: invalid state transition")
	ErrGenerationMismatch     = errors.New("inspection: generation mismatch")
	ErrOperationConflict      = errors.New("inspection: operation conflict")
	ErrSamplerReviewerOverlap = errors.New("inspection: sampler and reviewer overlap")
	ErrSamplerCount           = errors.New("inspection: need exactly two distinct samplers")
	ErrReviewerCount          = errors.New("inspection: need exactly two distinct reviewers")
	ErrWrongState             = errors.New("inspection: task is not in the expected state")
)

// Clock supplies deterministic logical time; the HTTP API injects a test
// controllable implementation.
type Clock interface {
	Now() LogicalTime
}

// ValidateSeparation enforces the domain rule that samplers and reviewers must
// not overlap, both samplers must be distinct, and both reviewers must be
// distinct. It returns a descriptive error for any violation.
func ValidateSeparation(samplers, reviewers []catalog.PersonnelID) error {
	if len(samplers) != 2 || samplers[0] == samplers[1] {
		return ErrSamplerCount
	}
	if len(reviewers) != 2 || reviewers[0] == reviewers[1] {
		return ErrReviewerCount
	}
	seen := make(map[catalog.PersonnelID]bool, len(samplers))
	for _, s := range samplers {
		seen[s] = true
	}
	for _, r := range reviewers {
		if seen[r] {
			return ErrSamplerReviewerOverlap
		}
	}
	return nil
}

// MustBeState returns ErrWrongState unless the task is in the expected state.
func (t *InspectionTask) MustBeState(s State) error {
	if t.State != s {
		return ErrWrongState
	}
	return nil
}

// MustBeGeneration returns ErrGenerationMismatch unless the task's generation
// matches the supplied generation.
func (t *InspectionTask) MustBeGeneration(g Generation) error {
	if t.Generation != g {
		return ErrGenerationMismatch
	}
	return nil
}

// MustNotBeTerminal returns ErrTerminalState when the task has a final state.
func (t *InspectionTask) MustNotBeTerminal() error {
	if t.State.IsTerminal() {
		return ErrTerminalState
	}
	return nil
}
