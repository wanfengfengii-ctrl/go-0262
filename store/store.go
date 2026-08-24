// Package store defines the persistence boundary for the intake aggregate and
// its satellite entities. The production implementation is a SQLite WAL store
// with foreign keys and unique indexes; a deterministic in-memory store is
// provided for tests. Every write is applied in a single transaction so any
// failure rolls back to the state before the call.
//
// Component: 持久化 (SQLite WAL) 边界.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
)

// ErrNotFound is returned when an entity does not exist.
var ErrNotFound = errors.New("store: entity not found")

// ErrDuplicate is returned when a unique resource or identity is already held.
var ErrDuplicate = errors.New("store: resource already occupied")

// OperationRecord is the idempotency ledger entry for a caller operation. The
// content hash distinguishes a replay (same content) from a conflict.
type OperationRecord struct {
	Operation   inspection.OperationID
	ContentHash string
	TaskID      inspection.TaskID
	ResultJSON  string
	AppliedAt   inspection.LogicalTime
}

// Tx is a single transactional view of the store. All methods write to the
// same transaction and commit atomically when WithTx returns without error.
type Tx interface {
	CreateTask(ctx context.Context, req inspection.LockRequest, t inspection.InspectionTask) (inspection.InspectionTask, error)
	UpdateTaskState(ctx context.Context, id inspection.TaskID, gen inspection.Generation, from, to inspection.State) error
	SaveOperation(ctx context.Context, rec OperationRecord) error
	SaveBlindSample(ctx context.Context, m ledger.BlindSampleMap) error
	RevealBlind(ctx context.Context, id inspection.TaskID, gen inspection.Generation) error
	SaveLease(ctx context.Context, l ledger.ResourceLease) error
	ReleaseLeases(ctx context.Context, id inspection.TaskID, at inspection.LogicalTime) error
	SavePollenCells(ctx context.Context, cells []evidence.PollenCoverCell) error
	AppendEvidence(ctx context.Context, v evidence.EvidenceVersion) error
	SaveAttempt(ctx context.Context, a evidence.AdapterAttempt) error
	SaveReview(ctx context.Context, r arbiter.ReviewAndFinal) error
	SaveFinal(ctx context.Context, r arbiter.ReviewAndFinal) error
}

// Store is the persistence boundary. Reads are available standalone; writes
// are grouped into transactions through WithTx.
type Store interface {
	WithTx(ctx context.Context, fn func(Tx) error) error

	LoadTask(ctx context.Context, id inspection.TaskID) (inspection.InspectionTask, error)
	LoadOperation(ctx context.Context, op inspection.OperationID) (*OperationRecord, error)
	LoadBlindSample(ctx context.Context, id inspection.TaskID) (*ledger.BlindSampleMap, error)
	LoadLeases(ctx context.Context, id inspection.TaskID) ([]ledger.ResourceLease, error)
	LoadPollenCells(ctx context.Context, id inspection.TaskID, gen inspection.Generation) ([]evidence.PollenCoverCell, error)
	LoadEvidence(ctx context.Context, id inspection.TaskID, gen inspection.Generation) ([]evidence.EvidenceVersion, error)
	LoadAttempts(ctx context.Context, id inspection.TaskID) ([]evidence.AdapterAttempt, error)
	LoadReviews(ctx context.Context, id inspection.TaskID) ([]arbiter.ReviewAndFinal, error)
	LoadFinal(ctx context.Context, id inspection.TaskID) (*arbiter.ReviewAndFinal, error)
	ListTasks(ctx context.Context) ([]inspection.InspectionTask, error)

	// Recover rebuilds in-memory indexes and validates incomplete leases after
	// a process restart. It returns the occupied barrel/seal/blind/resource set
	// so the service can reject duplicates deterministically.
	Recover(ctx context.Context) error

	Close() error
}

// HashContent computes a stable content hash for idempotency comparison.
func HashContent(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Fall back to a zero hash; the caller should avoid un-marshalable
		// values in requests.
		b = []byte("unhashable")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
