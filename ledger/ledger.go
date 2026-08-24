// Package ledger manages the one-time blind-code sample map, the triplicate
// split consistency matrix, and the concurrent leases for maturation-tank time
// slots, DNA plate wells and slide numbers.
//
// Component: 盲码样本与资源占用账簿.
package ledger

import (
	"errors"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
)

// Sentinel errors for the blind-code and lease boundary.
var (
	ErrBlindCodeMismatch = errors.New("ledger: blind code does not match sealed sample")
	ErrAlreadyRevealed   = errors.New("ledger: blind code already revealed")
	ErrPrematureReveal   = errors.New("ledger: reveal not allowed at this stage")
	ErrTriplicateCount   = errors.New("ledger: triplicate must have exactly three distinct samples")
)

// TriplicateCount is the required number of split samples (三联分管数量).
const TriplicateCount = 3

// ValidateTriplicates enforces the triplicate consistency rule: exactly three
// distinct split-sample identifiers that all belong to the same blind code.
func ValidateTriplicates(ids []string) error {
	if len(ids) != TriplicateCount {
		return ErrTriplicateCount
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return ErrTriplicateCount
		}
		seen[id] = true
	}
	return nil
}

// CanReveal reports whether a reveal is permitted: the blind code must still
// be sealed and the reveal must be triggered by a valid stage (non-terminal,
// after sampling confirm). A sealed code may only be revealed once.
func CanReveal(state RevealState) error {
	if state == RevealOpen {
		return ErrAlreadyRevealed
	}
	return nil
}

// Reveal flips a sealed blind code to revealed. It refuses to reveal twice.
func (b *BlindSampleMap) Reveal() error {
	if err := CanReveal(b.RevealState); err != nil {
		return err
	}
	b.RevealState = RevealOpen
	return nil
}

// BlindCode is a sampling blind code (抽样盲码).
type BlindCode string

// RevealState tracks whether a blind code has been revealed.
type RevealState int

const (
	RevealSealed RevealState = iota // 尚未揭示
	RevealOpen                      // 已揭示
)

// BlindSampleMap is the one-time barrel-to-blind-code mapping (盲码映射).
type BlindSampleMap struct {
	TaskID      inspection.TaskID
	Barrel      string
	BlindCode   BlindCode
	RevealState RevealState
	// TriplicateIDs are the three split sample identifiers (三联分管).
	TriplicateIDs []string
	// SealedBy is the personnel who sealed the triplicate.
	SealedBy catalog.PersonnelID
	SealedAt inspection.LogicalTime
}

// ResourceType enumerates the leaseable resource kinds.
type ResourceType string

const (
	ResourceTankSlot ResourceType = "tank_slot" // 熟化罐时隙
	ResourceWell     ResourceType = "well"      // DNA 板孔
	ResourceSlide    ResourceType = "slide"     // 玻片编号
)

// LeaseStatus is the state of a resource lease.
type LeaseStatus int

const (
	LeaseActive   LeaseStatus = iota // 占用中
	LeaseReleased                    // 已释放
)

// ResourceLease is a logical-time stamped resource lease (资源租约).
type ResourceLease struct {
	ResourceType ResourceType
	ResourceID   string
	TaskID       inspection.TaskID
	Generation   inspection.Generation
	Status       LeaseStatus
	AcquiredAt   inspection.LogicalTime
	ReleasedAt   inspection.LogicalTime
}
