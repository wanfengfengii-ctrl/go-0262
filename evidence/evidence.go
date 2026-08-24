// Package evidence holds the pollen-count coverage grid, the fixed-point
// chemistry readings, the derived threshold evidence, the DNA fingerprint
// version chain, and the instrument adapter attempt records.
//
// Components: 花粉谱及理化采集账簿 and 花源指纹复判及终局仲裁器 (evidence side).
package evidence

import (
	"errors"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/fixed"
	"nectargate/raw-honey-maturation-intake/inspection"
)

// PollenCoverCell is one pollen count cell in the coverage grid (花粉覆盖格).
type PollenCoverCell struct {
	TaskID       inspection.TaskID
	Generation   inspection.Generation
	Slide        string
	Class        catalog.PollenClass
	Count        int
	CoverVersion int
	EnteredBy    catalog.PersonnelID
	Valid        bool
}

// EvidenceType enumerates the evidence kinds.
type EvidenceType string

const (
	EvidenceDNA       EvidenceType = "dna"
	EvidenceChemistry EvidenceType = "chemistry"
	EvidenceRejudge   EvidenceType = "rejudge"
)

// EvidenceVersion is an append-only evidence version (证据版本链). Versions are
// never overwritten; late readings from an older generation are retained but
// do not participate in the current arbitration.
type EvidenceVersion struct {
	TaskID     inspection.TaskID
	Generation inspection.Generation
	Type       EvidenceType
	BlindCode  string
	Slide      string
	Well       string
	Reading    fixed.Decimal
	Conclusion string
	Version    int
	Immutable  bool
}

// InstrumentType enumerates the instrument adapters.
type InstrumentType string

const (
	InstrumentQPCR     InstrumentType = "qpcr"
	InstrumentSpectro  InstrumentType = "spectrophotometer"
	InstrumentMoisture InstrumentType = "moisture_meter"
)

// AttemptResult classifies an instrument adapter attempt.
type AttemptResult string

const (
	AttemptOK           AttemptResult = "ok"
	AttemptRejected     AttemptResult = "rejected"
	AttemptDisconnected AttemptResult = "disconnected"
	AttemptTimeout      AttemptResult = "timeout"
	AttemptMalformed    AttemptResult = "malformed"
)

// AdapterAttempt is an auditable retry record for an instrument call. Failures
// only record a pending-retry attempt; they never produce derived evidence.
type AdapterAttempt struct {
	Instrument InstrumentType
	CallKey    string
	TaskID     inspection.TaskID
	Request    string
	Result     AttemptResult
	RetryCount int
	At         inspection.LogicalTime
	RawError   string
}

// InstrumentAdapter is injected by the HTTP API; tests use scripted rejections,
// disconnects, timeouts and malformed responses instead of real hardware.
type InstrumentAdapter interface {
	// Instrument returns the instrument type this adapter drives.
	Instrument() InstrumentType
	// Call performs one instrument call and returns a raw result plus a
	// non-empty ErrorCode when the call is not a clean success.
	Call(req string) (raw string, errorCode string)
}

// Sentinel errors for the evidence boundary.
var (
	ErrNotLockedClass    = errors.New("evidence: pollen class not in locked set")
	ErrCountNotConserved = errors.New("evidence: pollen count not conserved")
	ErrCoverIncomplete   = errors.New("evidence: slide coverage incomplete")
	ErrDuplicateRejudge  = errors.New("evidence: duplicate rejudge for generation")
)
