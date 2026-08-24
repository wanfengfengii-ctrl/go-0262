package store

import (
	"context"
	"errors"
	"sync"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
)

// memState is the mutable data held by a Memory store.
type memState struct {
	seq        int64
	tasks      map[inspection.TaskID]inspection.InspectionTask
	operations map[inspection.OperationID]OperationRecord
	blinds     map[inspection.TaskID]ledger.BlindSampleMap
	blindCodes map[ledger.BlindCode]inspection.TaskID
	leases     map[string]ledger.ResourceLease
	pollen     map[inspection.TaskID][]evidence.PollenCoverCell
	evidence   map[inspection.TaskID][]evidence.EvidenceVersion
	attempts   map[inspection.TaskID][]evidence.AdapterAttempt
	reviews    map[inspection.TaskID][]arbiter.ReviewAndFinal
	finals     map[inspection.TaskID]arbiter.ReviewAndFinal
	barrels    map[string]inspection.TaskID
	seals      map[string]inspection.TaskID
}

func newMemState() *memState {
	return &memState{
		tasks:      make(map[inspection.TaskID]inspection.InspectionTask),
		operations: make(map[inspection.OperationID]OperationRecord),
		blinds:     make(map[inspection.TaskID]ledger.BlindSampleMap),
		blindCodes: make(map[ledger.BlindCode]inspection.TaskID),
		leases:     make(map[string]ledger.ResourceLease),
		pollen:     make(map[inspection.TaskID][]evidence.PollenCoverCell),
		evidence:   make(map[inspection.TaskID][]evidence.EvidenceVersion),
		attempts:   make(map[inspection.TaskID][]evidence.AdapterAttempt),
		reviews:    make(map[inspection.TaskID][]arbiter.ReviewAndFinal),
		finals:     make(map[inspection.TaskID]arbiter.ReviewAndFinal),
		barrels:    make(map[string]inspection.TaskID),
		seals:      make(map[string]inspection.TaskID),
	}
}

func leaseKey(typ ledger.ResourceType, id string) string {
	return string(typ) + ":" + id
}

// Memory is a concurrency-safe in-memory Store. WithTx clones the current
// state, applies fn to the clone, and only replaces the live state on success,
// giving true rollback semantics for tests.
type Memory struct {
	mu     sync.RWMutex
	state  *memState
	closed bool
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{state: newMemState()}
}

// WithTx implements Store by applying fn against a cloned state.
func (m *Memory) WithTx(ctx context.Context, fn func(Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("store: closed")
	}
	clone := cloneState(m.state)
	tx := &memTx{state: clone}
	if err := fn(tx); err != nil {
		return err
	}
	m.state = clone
	return nil
}

// Recover implements Store; the in-memory store is already consistent.
func (m *Memory) Recover(_ context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return errors.New("store: closed")
	}
	return nil
}

// Close implements Store.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *Memory) LoadTask(_ context.Context, id inspection.TaskID) (inspection.InspectionTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.state.tasks[id]
	if !ok {
		return inspection.InspectionTask{}, ErrNotFound
	}
	return t, nil
}

func (m *Memory) LoadOperation(_ context.Context, op inspection.OperationID) (*OperationRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.state.operations[op]
	if !ok {
		return nil, ErrNotFound
	}
	cp := rec
	return &cp, nil
}

func (m *Memory) LoadBlindSample(_ context.Context, id inspection.TaskID) (*ledger.BlindSampleMap, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.state.blinds[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := b
	return &cp, nil
}

func (m *Memory) LoadLeases(_ context.Context, id inspection.TaskID) ([]ledger.ResourceLease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []ledger.ResourceLease{}
	for _, l := range m.state.leases {
		if l.TaskID == id {
			out = append(out, l)
		}
	}
	return out, nil
}

func (m *Memory) LoadPollenCells(_ context.Context, id inspection.TaskID, gen inspection.Generation) ([]evidence.PollenCoverCell, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []evidence.PollenCoverCell{}
	for _, c := range m.state.pollen[id] {
		if c.Generation == gen {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *Memory) LoadEvidence(_ context.Context, id inspection.TaskID, gen inspection.Generation) ([]evidence.EvidenceVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []evidence.EvidenceVersion{}
	for _, e := range m.state.evidence[id] {
		if e.Generation == gen {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *Memory) LoadAttempts(_ context.Context, id inspection.TaskID) ([]evidence.AdapterAttempt, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]evidence.AdapterAttempt{}, m.state.attempts[id]...)
	return out, nil
}

func (m *Memory) LoadReviews(_ context.Context, id inspection.TaskID) ([]arbiter.ReviewAndFinal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]arbiter.ReviewAndFinal{}, m.state.reviews[id]...)
	return out, nil
}

func (m *Memory) LoadFinal(_ context.Context, id inspection.TaskID) (*arbiter.ReviewAndFinal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.state.finals[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := f
	return &cp, nil
}

func (m *Memory) ListTasks(_ context.Context) ([]inspection.InspectionTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]inspection.InspectionTask, 0, len(m.state.tasks))
	for _, t := range m.state.tasks {
		out = append(out, t)
	}
	return out, nil
}

// memTx applies writes to a cloned memState.
type memTx struct {
	state *memState
}

func (t *memTx) CreateTask(_ context.Context, _ inspection.LockRequest, task inspection.InspectionTask) (inspection.InspectionTask, error) {
	if task.Barrel != "" {
		if _, ok := t.state.barrels[task.Barrel]; ok {
			return inspection.InspectionTask{}, ErrDuplicate
		}
	}
	if task.Seal != "" {
		if _, ok := t.state.seals[task.Seal]; ok {
			return inspection.InspectionTask{}, ErrDuplicate
		}
	}
	if task.ID == "" {
		t.state.seq++
		task.ID = inspection.TaskID(formatID(t.state.seq))
	}
	if task.Generation == 0 {
		task.Generation = 1
	}
	if task.CreatedAt == 0 {
		task.CreatedAt = inspection.LogicalTime(t.state.seq)
	}
	t.state.tasks[task.ID] = task
	if task.Barrel != "" {
		t.state.barrels[task.Barrel] = task.ID
	}
	if task.Seal != "" {
		t.state.seals[task.Seal] = task.ID
	}
	return task, nil
}

func (t *memTx) UpdateTaskState(_ context.Context, id inspection.TaskID, gen inspection.Generation, from, to inspection.State) error {
	task, ok := t.state.tasks[id]
	if !ok {
		return ErrNotFound
	}
	if task.Generation != gen {
		return inspection.ErrGenerationMismatch
	}
	if task.State != from {
		return inspection.ErrWrongState
	}
	task.State = to
	t.state.tasks[id] = task
	// Terminal transition releases the barrel and seal identifiers.
	if to.IsTerminal() {
		delete(t.state.barrels, task.Barrel)
		delete(t.state.seals, task.Seal)
	}
	return nil
}

func (t *memTx) SaveOperation(_ context.Context, rec OperationRecord) error {
	if _, ok := t.state.operations[rec.Operation]; ok {
		return ErrDuplicate
	}
	t.state.operations[rec.Operation] = rec
	return nil
}

func (t *memTx) SaveBlindSample(_ context.Context, m ledger.BlindSampleMap) error {
	if prior, ok := t.state.blindCodes[m.BlindCode]; ok && prior != m.TaskID {
		return ErrDuplicate
	}
	t.state.blinds[m.TaskID] = m
	t.state.blindCodes[m.BlindCode] = m.TaskID
	return nil
}

func (t *memTx) RevealBlind(_ context.Context, id inspection.TaskID, gen inspection.Generation) error {
	b, ok := t.state.blinds[id]
	if !ok {
		return ErrNotFound
	}
	task, ok := t.state.tasks[id]
	if !ok || task.Generation != gen {
		return inspection.ErrGenerationMismatch
	}
	if b.RevealState == ledger.RevealOpen {
		return ledger.ErrAlreadyRevealed
	}
	b.RevealState = ledger.RevealOpen
	t.state.blinds[id] = b
	return nil
}

func (t *memTx) SaveLease(_ context.Context, l ledger.ResourceLease) error {
	key := leaseKey(l.ResourceType, l.ResourceID)
	if prior, ok := t.state.leases[key]; ok && prior.Status == ledger.LeaseActive && prior.TaskID != l.TaskID {
		return ErrDuplicate
	}
	t.state.leases[key] = l
	return nil
}

func (t *memTx) ReleaseLeases(_ context.Context, id inspection.TaskID, at inspection.LogicalTime) error {
	for key, l := range t.state.leases {
		if l.TaskID == id && l.Status == ledger.LeaseActive {
			l.Status = ledger.LeaseReleased
			l.ReleasedAt = at
			t.state.leases[key] = l
		}
	}
	return nil
}

func (t *memTx) SavePollenCells(_ context.Context, cells []evidence.PollenCoverCell) error {
	for _, c := range cells {
		t.state.pollen[c.TaskID] = append(t.state.pollen[c.TaskID], c)
	}
	return nil
}

func (t *memTx) AppendEvidence(_ context.Context, v evidence.EvidenceVersion) error {
	t.state.evidence[v.TaskID] = append(t.state.evidence[v.TaskID], v)
	return nil
}

func (t *memTx) SaveAttempt(_ context.Context, a evidence.AdapterAttempt) error {
	t.state.attempts[a.TaskID] = append(t.state.attempts[a.TaskID], a)
	return nil
}

func (t *memTx) SaveReview(_ context.Context, r arbiter.ReviewAndFinal) error {
	for _, e := range t.state.reviews[r.TaskID] {
		if e.Reviewer == r.Reviewer {
			return ErrDuplicate
		}
	}
	t.state.reviews[r.TaskID] = append(t.state.reviews[r.TaskID], r)
	return nil
}

func (t *memTx) SaveFinal(_ context.Context, r arbiter.ReviewAndFinal) error {
	if _, ok := t.state.finals[r.TaskID]; ok {
		return arbiter.ErrFinalAlreadySet
	}
	t.state.finals[r.TaskID] = r
	return nil
}

func cloneState(src *memState) *memState {
	dst := newMemState()
	dst.seq = src.seq
	for k, v := range src.tasks {
		dst.tasks[k] = v
	}
	for k, v := range src.operations {
		dst.operations[k] = v
	}
	for k, v := range src.blinds {
		dst.blinds[k] = v
	}
	for k, v := range src.blindCodes {
		dst.blindCodes[k] = v
	}
	for k, v := range src.leases {
		dst.leases[k] = v
	}
	for k, v := range src.pollen {
		dst.pollen[k] = append([]evidence.PollenCoverCell{}, v...)
	}
	for k, v := range src.evidence {
		dst.evidence[k] = append([]evidence.EvidenceVersion{}, v...)
	}
	for k, v := range src.attempts {
		dst.attempts[k] = append([]evidence.AdapterAttempt{}, v...)
	}
	for k, v := range src.reviews {
		dst.reviews[k] = append([]arbiter.ReviewAndFinal{}, v...)
	}
	for k, v := range src.finals {
		dst.finals[k] = v
	}
	for k, v := range src.barrels {
		dst.barrels[k] = v
	}
	for k, v := range src.seals {
		dst.seals[k] = v
	}
	return dst
}

func formatID(seq int64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [16]byte
	i := len(buf)
	n := seq
	for {
		i--
		buf[i] = digits[n%36]
		n /= 36
		if n == 0 {
			break
		}
	}
	return "task_" + string(buf[i:])
}
