// Package service orchestrates the NectarGate intake closed loop. It wires the
// catalog, the persistence store, the logical clock and the instrument
// adapters into the business flows: lock, dual sampling, triplicate sealing,
// resource leasing, pollen counting, DNA/chemistry evidence, rejudgement,
// independent review and the terminal single-write finalize.
//
// Component: 业务编排 (aggregate application service).
package service

import (
	"context"
	"errors"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/store"
)

// Sentinel service errors mapped to stable HTTP codes by the API layer.
var (
	ErrBadRequest        = errors.New("service: bad request")
	ErrNotFound          = errors.New("service: task not found")
	ErrTerminalState     = errors.New("service: task is terminal")
	ErrOperationConflict = errors.New("service: operation conflict")
	ErrResourceOccupied  = errors.New("service: resource occupied")
	ErrUnqualified       = errors.New("service: unqualified personnel")
	ErrOverlap           = errors.New("service: personnel overlap")
	ErrInvalidReading    = errors.New("service: invalid reading")
	ErrAdapterRetry      = errors.New("service: adapter retry pending")
	ErrUnboundEvidence   = errors.New("service: evidence binding does not match locked resources")
)

// Service coordinates the intake flows.
type Service struct {
	store    store.Store
	catalog  catalog.RuleCatalog
	clock    inspection.Clock
	adapters map[evidence.InstrumentType]evidence.InstrumentAdapter
}

// New constructs a Service. adapters may be nil to disable instrument calls.
func New(st store.Store, c catalog.RuleCatalog, clk inspection.Clock, adapters map[evidence.InstrumentType]evidence.InstrumentAdapter) *Service {
	if clk == nil {
		clk = systemClock{}
	}
	if adapters == nil {
		adapters = map[evidence.InstrumentType]evidence.InstrumentAdapter{}
	}
	return &Service{store: st, catalog: c, clock: clk, adapters: adapters}
}

type systemClock struct{}

func (systemClock) Now() inspection.LogicalTime {
	return inspection.LogicalTime(time.Now().UnixNano())
}

// Catalog returns the configured rule catalog.
func (s *Service) Catalog() catalog.RuleCatalog { return s.catalog }

// Store exposes the underlying store for startup recovery wiring.
func (s *Service) Store() store.Store { return s.store }

// GetTask loads a task by ID.
func (s *Service) GetTask(ctx context.Context, id inspection.TaskID) (inspection.InspectionTask, error) {
	t, err := s.store.LoadTask(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return inspection.InspectionTask{}, ErrNotFound
	}
	return t, err
}

// rule returns the rule for a farm or a farm-mismatch error.
func (s *Service) rule(farm catalog.FarmID) (*catalog.FarmSourceRule, error) {
	r := s.catalog.Rule(farm)
	if r == nil {
		return nil, catalog.ErrFarmMismatch
	}
	return r, nil
}

// checkReplay reports whether an operation id was already applied. It returns
// the stored result JSON on a replay, ErrOperationConflict on a content
// mismatch, and (false, "", nil) when the operation is new.
func (s *Service) checkReplay(ctx context.Context, op inspection.OperationID, hash string) (bool, string, error) {
	if op == "" {
		return false, "", nil
	}
	existing, err := s.store.LoadOperation(ctx, op)
	if errors.Is(err, store.ErrNotFound) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if existing.ContentHash != hash {
		return false, "", ErrOperationConflict
	}
	return true, existing.ResultJSON, nil
}

// applyIdempotent wraps a transaction so an operation id is enforced. Callers
// must have already resolved replays via checkReplay; this records the
// operation atomically with the business write.
func (s *Service) applyIdempotent(
	ctx context.Context,
	op inspection.OperationID,
	hash string,
	resultJSON string,
	apply func(tx store.Tx) error,
) error {
	err := s.store.WithTx(ctx, func(tx store.Tx) error {
		if err := apply(tx); err != nil {
			return err
		}
		if op != "" {
			return tx.SaveOperation(ctx, store.OperationRecord{
				Operation:   op,
				ContentHash: hash,
				ResultJSON:  resultJSON,
				AppliedAt:   s.clock.Now(),
			})
		}
		return nil
	})
	if errors.Is(err, store.ErrDuplicate) {
		return ErrOperationConflict
	}
	return err
}

// adapter returns the configured adapter for an instrument, or nil.
func (s *Service) adapter(inst evidence.InstrumentType) evidence.InstrumentAdapter {
	return s.adapters[inst]
}

// validateEvidenceBinding enforces that a submitted evidence record's blind code,
// slide and plate well belong to the resources this task locked at lock time.
// Evidence may only ever bind to the current task's own samples and resources,
// never to another task's blind code, slide or well. This keeps the quality
// record's evidence identifiers aligned with the locked resources.
func validateEvidenceBinding(t inspection.InspectionTask, blindCode, slide, well string) error {
	if blindCode != t.BlindCode {
		return ErrUnboundEvidence
	}
	if well != t.Well {
		return ErrUnboundEvidence
	}
	if !containsSlide(t.Slides, slide) {
		return ErrUnboundEvidence
	}
	return nil
}

// containsSlide reports whether slide is among the locked slide numbers.
func containsSlide(slides []string, slide string) bool {
	for _, s := range slides {
		if s == slide {
			return true
		}
	}
	return false
}

// ruleForTask loads a task and resolves its farm rule, mapping a missing task
// to ErrNotFound.
func (s *Service) ruleForTask(ctx context.Context, id inspection.TaskID) (inspection.InspectionTask, *catalog.FarmSourceRule, error) {
	t, err := s.store.LoadTask(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return inspection.InspectionTask{}, nil, ErrNotFound
	}
	if err != nil {
		return inspection.InspectionTask{}, nil, err
	}
	r := s.catalog.Rule(t.Farm)
	return t, r, nil
}
