package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO, multi-arch)
	sqlite "modernc.org/sqlite"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/fixed"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
)

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  generation INTEGER NOT NULL,
  state TEXT NOT NULL,
  farm TEXT NOT NULL,
  season TEXT NOT NULL,
  batch_id TEXT NOT NULL,
  batch_extracted_at INTEGER NOT NULL,
  batch_valid_for INTEGER NOT NULL,
  barrel TEXT NOT NULL,
  seal TEXT NOT NULL,
  zone TEXT NOT NULL,
  blind_code TEXT NOT NULL,
  slides TEXT NOT NULL,
  well TEXT NOT NULL,
  tank_slot TEXT NOT NULL,
  dna_max_ct_raw INTEGER NOT NULL,
  dna_max_ct_scale INTEGER NOT NULL,
  pollen_min_target INTEGER NOT NULL,
  chem_max_hmf_raw INTEGER NOT NULL,
  chem_max_hmf_scale INTEGER NOT NULL,
  chem_min_amylase_raw INTEGER NOT NULL,
  chem_min_amylase_scale INTEGER NOT NULL,
  chem_max_moisture_raw INTEGER NOT NULL,
  chem_max_moisture_scale INTEGER NOT NULL,
  chem_max_conductivity_raw INTEGER NOT NULL,
  chem_max_conductivity_scale INTEGER NOT NULL,
  chem_max_acidity_raw INTEGER NOT NULL,
  chem_max_acidity_scale INTEGER NOT NULL,
  samplers TEXT NOT NULL,
  reviewers TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_barrel_open
  ON tasks(barrel) WHERE state NOT IN ('matured','quarantined','cancelled');
CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_seal_open
  ON tasks(seal) WHERE state NOT IN ('matured','quarantined','cancelled');

CREATE TABLE IF NOT EXISTS operations (
  operation TEXT PRIMARY KEY,
  content_hash TEXT NOT NULL,
  task_id TEXT NOT NULL,
  result_json TEXT NOT NULL,
  applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS blind_samples (
  task_id TEXT PRIMARY KEY,
  barrel TEXT NOT NULL,
  blind_code TEXT NOT NULL UNIQUE,
  reveal_state INTEGER NOT NULL,
  triplicates TEXT NOT NULL,
  sealed_by TEXT NOT NULL,
  sealed_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS leases (
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  generation INTEGER NOT NULL,
  status TEXT NOT NULL,
  acquired_at INTEGER NOT NULL,
  released_at INTEGER NOT NULL,
  PRIMARY KEY (resource_type, resource_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_leases_active
  ON leases(resource_type, resource_id) WHERE status='active';

CREATE TABLE IF NOT EXISTS pollen_cells (
  task_id TEXT NOT NULL,
  generation INTEGER NOT NULL,
  slide TEXT NOT NULL,
  class TEXT NOT NULL,
  count INTEGER NOT NULL,
  cover_version INTEGER NOT NULL,
  entered_by TEXT NOT NULL,
  valid INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS evidence (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  generation INTEGER NOT NULL,
  type TEXT NOT NULL,
  blind_code TEXT NOT NULL,
  slide TEXT NOT NULL,
  well TEXT NOT NULL,
  reading_raw INTEGER NOT NULL,
  reading_scale INTEGER NOT NULL,
  conclusion TEXT NOT NULL,
  version INTEGER NOT NULL,
  immutable INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  instrument TEXT NOT NULL,
  call_key TEXT NOT NULL,
  task_id TEXT NOT NULL,
  request TEXT NOT NULL,
  result TEXT NOT NULL,
  retry_count INTEGER NOT NULL,
  at INTEGER NOT NULL,
  raw_error TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS reviews (
  task_id TEXT NOT NULL,
  reviewer TEXT NOT NULL,
  scope TEXT NOT NULL,
  rejudge_gen INTEGER NOT NULL,
  final_type TEXT NOT NULL,
  credential_id TEXT NOT NULL,
  barrier_key TEXT NOT NULL,
  PRIMARY KEY (task_id, reviewer)
);

CREATE TABLE IF NOT EXISTS finals (
  task_id TEXT PRIMARY KEY,
  reviewer TEXT NOT NULL,
  scope TEXT NOT NULL,
  rejudge_gen INTEGER NOT NULL,
  final_type TEXT NOT NULL,
  credential_id TEXT NOT NULL,
  barrier_key TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_finals_barrier ON finals(barrier_key);
`

// SQLite is the production Store backed by a SQLite WAL database.
type SQLite struct {
	db *sql.DB
	// txMu serializes transactions so the single connection model remains
	// deterministic and free of nested-transaction races.
	txMu sync.Mutex
	// occupied is the in-memory occupancy index rebuilt at startup and kept in
	// sync on writes. Keys encode barrel/seal/blind/lease identifiers.
	occupied map[string]inspection.TaskID
}

// OpenSQLite opens (or creates) a SQLite WAL database at path and applies the
// schema. It configures WAL journaling, foreign keys and a busy timeout.
func OpenSQLite(path string) (*SQLite, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	s := &SQLite{db: db, occupied: make(map[string]inspection.TaskID)}
	if err := s.Recover(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close implements Store.
func (s *SQLite) Close() error { return s.db.Close() }

// Recover rebuilds the in-memory occupancy index and validates incomplete
// leases: an active lease whose task is terminal or missing is released.
func (s *SQLite) Recover(ctx context.Context) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()

	occupied := make(map[string]inspection.TaskID)

	rows, err := s.db.QueryContext(ctx, `SELECT id, barrel, seal, state FROM tasks WHERE state NOT IN ('matured','quarantined','cancelled')`)
	if err != nil {
		return err
	}
	type openTask struct {
		id     inspection.TaskID
		barrel string
		seal   string
	}
	var open []openTask
	for rows.Next() {
		var t openTask
		var state string
		if err := rows.Scan(&t.id, &t.barrel, &t.seal, &state); err != nil {
			rows.Close()
			return err
		}
		open = append(open, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range open {
		occupied["barrel:"+t.barrel] = t.id
		occupied["seal:"+t.seal] = t.id
	}

	// Validate leases: release any active lease bound to a terminal or missing
	// task so restart recovery is deterministic.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE leases SET status='released'
		WHERE status='active' AND task_id NOT IN (
			SELECT id FROM tasks WHERE state NOT IN ('matured','quarantined','cancelled')
		)`); err != nil {
		return err
	}

	lrows, err := s.db.QueryContext(ctx, `SELECT resource_type, resource_id, task_id FROM leases WHERE status='active'`)
	if err != nil {
		return err
	}
	for lrows.Next() {
		var rt, rid string
		var tid inspection.TaskID
		if err := lrows.Scan(&rt, &rid, &tid); err != nil {
			lrows.Close()
			return err
		}
		occupied["lease:"+rt+":"+rid] = tid
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return err
	}

	brows, err := s.db.QueryContext(ctx, `SELECT blind_code, task_id FROM blind_samples`)
	if err != nil {
		return err
	}
	for brows.Next() {
		var bc string
		var tid inspection.TaskID
		if err := brows.Scan(&bc, &tid); err != nil {
			brows.Close()
			return err
		}
		occupied["blind:"+bc] = tid
	}
	brows.Close()
	if err := brows.Err(); err != nil {
		return err
	}

	s.occupied = occupied
	return nil
}

func isConstraint(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case 19, 787, 1555, 2067: // SQLITE_CONSTRAINT family
			return true
		}
	}
	return false
}

// WithTx runs fn inside a single transaction.
func (s *SQLite) WithTx(ctx context.Context, fn func(Tx) error) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	st := &sqlTx{tx: tx, owner: s}
	if err := fn(st); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// sqlTx implements Tx against a *sql.Tx.
type sqlTx struct {
	tx    *sql.Tx
	owner *SQLite
}

func (t *sqlTx) CreateTask(ctx context.Context, _ inspection.LockRequest, task inspection.InspectionTask) (inspection.InspectionTask, error) {
	if task.ID == "" {
		task.ID = inspection.TaskID(newTaskID())
	}
	if task.Generation == 0 {
		task.Generation = 1
	}
	slides, _ := json.Marshal(task.Slides)
	samplers, _ := json.Marshal(task.Samplers)
	reviewers, _ := json.Marshal(task.Reviewers)

	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO tasks (
			id, generation, state, farm, season, batch_id, batch_extracted_at, batch_valid_for,
			barrel, seal, zone, blind_code, slides, well, tank_slot,
			dna_max_ct_raw, dna_max_ct_scale, pollen_min_target,
			chem_max_hmf_raw, chem_max_hmf_scale,
			chem_min_amylase_raw, chem_min_amylase_scale,
			chem_max_moisture_raw, chem_max_moisture_scale,
			chem_max_conductivity_raw, chem_max_conductivity_scale,
			chem_max_acidity_raw, chem_max_acidity_scale,
			samplers, reviewers, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.ID, int64(task.Generation), task.State.String(), string(task.Farm), string(task.Season),
		task.BatchSummary.BatchID, task.BatchSummary.ExtractedAt.UnixNano(), int64(task.BatchSummary.ValidFor),
		task.Barrel, task.Seal, string(task.Zone), task.BlindCode, string(slides), task.Well, task.TankSlot,
		task.DNAThreshold.MaxCt.Raw, task.DNAThreshold.MaxCt.Scale, task.PollenThreshold.MinTargetCount,
		task.ChemThreshold.MaxHMF.Raw, task.ChemThreshold.MaxHMF.Scale,
		task.ChemThreshold.MinAmylase.Raw, task.ChemThreshold.MinAmylase.Scale,
		task.ChemThreshold.MaxMoisture.Raw, task.ChemThreshold.MaxMoisture.Scale,
		task.ChemThreshold.MaxConductivity.Raw, task.ChemThreshold.MaxConductivity.Scale,
		task.ChemThreshold.MaxAcidity.Raw, task.ChemThreshold.MaxAcidity.Scale,
		string(samplers), string(reviewers), int64(task.CreatedAt),
	)
	if err != nil {
		if isConstraint(err) {
			return inspection.InspectionTask{}, ErrDuplicate
		}
		return inspection.InspectionTask{}, err
	}
	return task, nil
}

func (t *sqlTx) UpdateTaskState(ctx context.Context, id inspection.TaskID, gen inspection.Generation, from, to inspection.State) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE tasks SET state=? WHERE id=? AND generation=?`,
		to.String(), string(id), int64(gen))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var state string
		var g int64
		err := t.tx.QueryRowContext(ctx, `SELECT state, generation FROM tasks WHERE id=?`, string(id)).Scan(&state, &g)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if g != int64(gen) {
			return inspection.ErrGenerationMismatch
		}
		if state != from.String() {
			return inspection.ErrWrongState
		}
		return inspection.ErrWrongState
	}
	return nil
}

func (t *sqlTx) SaveOperation(ctx context.Context, rec OperationRecord) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO operations (operation, content_hash, task_id, result_json, applied_at) VALUES (?,?,?,?,?)`,
		string(rec.Operation), rec.ContentHash, string(rec.TaskID), rec.ResultJSON, int64(rec.AppliedAt))
	if err != nil {
		if isConstraint(err) {
			return ErrDuplicate
		}
		return err
	}
	return nil
}

func (t *sqlTx) SaveBlindSample(ctx context.Context, m ledger.BlindSampleMap) error {
	trips, _ := json.Marshal(m.TriplicateIDs)
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO blind_samples (task_id, barrel, blind_code, reveal_state, triplicates, sealed_by, sealed_at)
		VALUES (?,?,?,?,?,?,?)`,
		string(m.TaskID), m.Barrel, string(m.BlindCode), int(m.RevealState), string(trips), string(m.SealedBy), int64(m.SealedAt))
	if err != nil {
		if isConstraint(err) {
			return ErrDuplicate
		}
		return err
	}
	return nil
}

func (t *sqlTx) RevealBlind(ctx context.Context, id inspection.TaskID, gen inspection.Generation) error {
	var reveal int
	var g int64
	err := t.tx.QueryRowContext(ctx, `SELECT reveal_state, (SELECT generation FROM tasks WHERE id=?) FROM blind_samples WHERE task_id=?`, string(id), string(id)).Scan(&reveal, &g)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if g != int64(gen) {
		return inspection.ErrGenerationMismatch
	}
	if reveal == int(ledger.RevealOpen) {
		return ledger.ErrAlreadyRevealed
	}
	_, err = t.tx.ExecContext(ctx, `UPDATE blind_samples SET reveal_state=? WHERE task_id=?`, int(ledger.RevealOpen), string(id))
	return err
}

func (t *sqlTx) SaveLease(ctx context.Context, l ledger.ResourceLease) error {
	var existingTask string
	var status string
	err := t.tx.QueryRowContext(ctx, `SELECT task_id, status FROM leases WHERE resource_type=? AND resource_id=?`,
		string(l.ResourceType), l.ResourceID).Scan(&existingTask, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = t.tx.ExecContext(ctx, `
			INSERT INTO leases (resource_type, resource_id, task_id, generation, status, acquired_at, released_at)
			VALUES (?,?,?,?,?,?,?)`,
			string(l.ResourceType), l.ResourceID, string(l.TaskID), int64(l.Generation), leaseStatusString(l.Status), int64(l.AcquiredAt), int64(l.ReleasedAt))
		if err != nil && isConstraint(err) {
			return ErrDuplicate
		}
		return err
	case err != nil:
		return err
	}

	if status == "active" && existingTask != string(l.TaskID) {
		return ErrDuplicate
	}
	_, err = t.tx.ExecContext(ctx, `
		UPDATE leases SET task_id=?, generation=?, status=?, acquired_at=?, released_at=?
		WHERE resource_type=? AND resource_id=?`,
		string(l.TaskID), int64(l.Generation), leaseStatusString(l.Status), int64(l.AcquiredAt), int64(l.ReleasedAt),
		string(l.ResourceType), l.ResourceID)
	return err
}

func (t *sqlTx) ReleaseLeases(ctx context.Context, id inspection.TaskID, at inspection.LogicalTime) error {
	_, err := t.tx.ExecContext(ctx, `UPDATE leases SET status='released', released_at=? WHERE task_id=? AND status='active'`, int64(at), string(id))
	return err
}

func (t *sqlTx) SavePollenCells(ctx context.Context, cells []evidence.PollenCoverCell) error {
	for _, c := range cells {
		valid := 0
		if c.Valid {
			valid = 1
		}
		if _, err := t.tx.ExecContext(ctx, `
			INSERT INTO pollen_cells (task_id, generation, slide, class, count, cover_version, entered_by, valid)
			VALUES (?,?,?,?,?,?,?,?)`,
			string(c.TaskID), int64(c.Generation), c.Slide, string(c.Class), c.Count, c.CoverVersion, string(c.EnteredBy), valid); err != nil {
			return err
		}
	}
	return nil
}

func (t *sqlTx) AppendEvidence(ctx context.Context, v evidence.EvidenceVersion) error {
	immutable := 0
	if v.Immutable {
		immutable = 1
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO evidence (task_id, generation, type, blind_code, slide, well, reading_raw, reading_scale, conclusion, version, immutable)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		string(v.TaskID), int64(v.Generation), string(v.Type), v.BlindCode, v.Slide, v.Well, v.Reading.Raw, v.Reading.Scale, v.Conclusion, v.Version, immutable)
	return err
}

func (t *sqlTx) SaveAttempt(ctx context.Context, a evidence.AdapterAttempt) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO attempts (instrument, call_key, task_id, request, result, retry_count, at, raw_error)
		VALUES (?,?,?,?,?,?,?,?)`,
		string(a.Instrument), a.CallKey, string(a.TaskID), a.Request, string(a.Result), a.RetryCount, int64(a.At), a.RawError)
	return err
}

func (t *sqlTx) SaveReview(ctx context.Context, r arbiter.ReviewAndFinal) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO reviews (task_id, reviewer, scope, rejudge_gen, final_type, credential_id, barrier_key)
		VALUES (?,?,?,?,?,?,?)`,
		string(r.TaskID), string(r.Reviewer), r.ScopeSummary, int64(r.RejudgeGen), string(r.FinalType), r.CredentialID, r.BarrierKey)
	if err != nil {
		if isConstraint(err) {
			return ErrDuplicate
		}
		return err
	}
	return nil
}

func (t *sqlTx) SaveFinal(ctx context.Context, r arbiter.ReviewAndFinal) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO finals (task_id, reviewer, scope, rejudge_gen, final_type, credential_id, barrier_key)
		VALUES (?,?,?,?,?,?,?)`,
		string(r.TaskID), string(r.Reviewer), r.ScopeSummary, int64(r.RejudgeGen), string(r.FinalType), r.CredentialID, r.BarrierKey)
	if err != nil {
		if isConstraint(err) {
			return arbiter.ErrFinalAlreadySet
		}
		return err
	}
	return nil
}

func leaseStatusString(s ledger.LeaseStatus) string {
	if s == ledger.LeaseReleased {
		return "released"
	}
	return "active"
}

// --- read methods ---

func (s *SQLite) LoadTask(ctx context.Context, id inspection.TaskID) (inspection.InspectionTask, error) {
	return s.scanTask(s.db.QueryRowContext(ctx, taskSelect+" WHERE id=?", string(id)))
}

const taskSelect = `SELECT id, generation, state, farm, season, batch_id, batch_extracted_at, batch_valid_for,
	barrel, seal, zone, blind_code, slides, well, tank_slot,
	dna_max_ct_raw, dna_max_ct_scale, pollen_min_target,
	chem_max_hmf_raw, chem_max_hmf_scale, chem_min_amylase_raw, chem_min_amylase_scale,
	chem_max_moisture_raw, chem_max_moisture_scale, chem_max_conductivity_raw, chem_max_conductivity_scale,
	chem_max_acidity_raw, chem_max_acidity_scale, samplers, reviewers, created_at FROM tasks`

type rowScanner interface{ Scan(dest ...any) error }

func (s *SQLite) scanTask(row rowScanner) (inspection.InspectionTask, error) {
	var t inspection.InspectionTask
	var state, farm, season, batchID, barrel, seal, zone, blind, slides, well, tank, samplers, reviewers string
	var gen, extracted, validFor, created int64
	var dnaRaw, dnaScale, pollenMin, hmfRaw, hmfScale, amyRaw, amyScale, moiRaw, moiScale, condRaw, condScale, acidRaw, acidScale int64

	err := row.Scan(&t.ID, &gen, &state, &farm, &season, &batchID, &extracted, &validFor,
		&barrel, &seal, &zone, &blind, &slides, &well, &tank,
		&dnaRaw, &dnaScale, &pollenMin,
		&hmfRaw, &hmfScale, &amyRaw, &amyScale, &moiRaw, &moiScale, &condRaw, &condScale, &acidRaw, &acidScale,
		&samplers, &reviewers, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return inspection.InspectionTask{}, ErrNotFound
	}
	if err != nil {
		return inspection.InspectionTask{}, err
	}

	t.Generation = inspection.Generation(gen)
	t.State = parseState(state)
	t.Farm = catalog.FarmID(farm)
	t.Season = catalog.NectarSeason(season)
	t.BatchSummary = catalog.BatchSummary{
		BatchID:     batchID,
		ExtractedAt: time.Unix(0, extracted),
		ValidFor:    time.Duration(validFor),
	}
	t.Barrel = barrel
	t.Seal = seal
	t.Zone = catalog.TemporaryZone(zone)
	t.BlindCode = blind
	t.Well = well
	t.TankSlot = tank
	t.DNAThreshold = catalog.DNAThreshold{MaxCt: fixed.Decimal{Raw: dnaRaw, Scale: int(dnaScale)}}
	t.PollenThreshold = catalog.PollenThreshold{MinTargetCount: int(pollenMin)}
	t.ChemThreshold = catalog.ChemistryThreshold{
		MaxHMF:          fixed.Decimal{Raw: hmfRaw, Scale: int(hmfScale)},
		MinAmylase:      fixed.Decimal{Raw: amyRaw, Scale: int(amyScale)},
		MaxMoisture:     fixed.Decimal{Raw: moiRaw, Scale: int(moiScale)},
		MaxConductivity: fixed.Decimal{Raw: condRaw, Scale: int(condScale)},
		MaxAcidity:      fixed.Decimal{Raw: acidRaw, Scale: int(acidScale)},
	}
	t.CreatedAt = inspection.LogicalTime(created)
	_ = json.Unmarshal([]byte(slides), &t.Slides)
	_ = json.Unmarshal([]byte(samplers), &t.Samplers)
	_ = json.Unmarshal([]byte(reviewers), &t.Reviewers)
	return t, nil
}

func parseState(s string) inspection.State {
	for st, name := range stateNames {
		if name == s {
			return st
		}
	}
	return inspection.State(-1)
}

var stateNames = map[inspection.State]string{
	inspection.StatePendingLock:              "pending_lock",
	inspection.StatePendingSamplingConfirm:   "pending_sampling_confirm",
	inspection.StateSealingSamples:           "sealing_samples",
	inspection.StateClaimingResources:        "claiming_resources",
	inspection.StateCountingPollen:           "counting_pollen",
	inspection.StateVerifyingDNA:             "verifying_dna",
	inspection.StateRetestingChemistry:       "retesting_chemistry",
	inspection.StatePendingIndependentReview: "pending_independent_review",
	inspection.StateReadyForMaturation:       "ready_for_maturation",
	inspection.StateMatured:                  "matured",
	inspection.StateQuarantined:              "quarantined",
	inspection.StateCancelled:                "cancelled",
}

func (s *SQLite) LoadOperation(ctx context.Context, op inspection.OperationID) (*OperationRecord, error) {
	var rec OperationRecord
	var opStr, hash, taskID, result string
	var applied int64
	err := s.db.QueryRowContext(ctx, `SELECT operation, content_hash, task_id, result_json, applied_at FROM operations WHERE operation=?`, string(op)).
		Scan(&opStr, &hash, &taskID, &result, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rec.Operation = inspection.OperationID(opStr)
	rec.ContentHash = hash
	rec.TaskID = inspection.TaskID(taskID)
	rec.ResultJSON = result
	rec.AppliedAt = inspection.LogicalTime(applied)
	return &rec, nil
}

func (s *SQLite) LoadBlindSample(ctx context.Context, id inspection.TaskID) (*ledger.BlindSampleMap, error) {
	var m ledger.BlindSampleMap
	var reveal int
	var trips string
	var sealedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT task_id, barrel, blind_code, reveal_state, triplicates, sealed_by, sealed_at FROM blind_samples WHERE task_id=?`, string(id)).
		Scan(&m.TaskID, &m.Barrel, &m.BlindCode, &reveal, &trips, &m.SealedBy, &sealedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.RevealState = ledger.RevealState(reveal)
	m.SealedAt = inspection.LogicalTime(sealedAt)
	_ = json.Unmarshal([]byte(trips), &m.TriplicateIDs)
	return &m, nil
}

func (s *SQLite) LoadLeases(ctx context.Context, id inspection.TaskID) ([]ledger.ResourceLease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT resource_type, resource_id, task_id, generation, status, acquired_at, released_at FROM leases WHERE task_id=?`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.ResourceLease
	for rows.Next() {
		var l ledger.ResourceLease
		var status string
		var gen, acq, rel int64
		if err := rows.Scan(&l.ResourceType, &l.ResourceID, &l.TaskID, &gen, &status, &acq, &rel); err != nil {
			return nil, err
		}
		l.Generation = inspection.Generation(gen)
		l.Status = ledger.LeaseActive
		if status == "released" {
			l.Status = ledger.LeaseReleased
		}
		l.AcquiredAt = inspection.LogicalTime(acq)
		l.ReleasedAt = inspection.LogicalTime(rel)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *SQLite) LoadPollenCells(ctx context.Context, id inspection.TaskID, gen inspection.Generation) ([]evidence.PollenCoverCell, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, generation, slide, class, count, cover_version, entered_by, valid FROM pollen_cells WHERE task_id=? AND generation=?`, string(id), int64(gen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evidence.PollenCoverCell
	for rows.Next() {
		var c evidence.PollenCoverCell
		var valid int
		if err := rows.Scan(&c.TaskID, &c.Generation, &c.Slide, &c.Class, &c.Count, &c.CoverVersion, &c.EnteredBy, &valid); err != nil {
			return nil, err
		}
		c.Valid = valid == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLite) LoadEvidence(ctx context.Context, id inspection.TaskID, gen inspection.Generation) ([]evidence.EvidenceVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, generation, type, blind_code, slide, well, reading_raw, reading_scale, conclusion, version, immutable FROM evidence WHERE task_id=? AND generation=? ORDER BY version`, string(id), int64(gen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evidence.EvidenceVersion
	for rows.Next() {
		var v evidence.EvidenceVersion
		var immutable int
		if err := rows.Scan(&v.TaskID, &v.Generation, &v.Type, &v.BlindCode, &v.Slide, &v.Well, &v.Reading.Raw, &v.Reading.Scale, &v.Conclusion, &v.Version, &immutable); err != nil {
			return nil, err
		}
		v.Immutable = immutable == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *SQLite) LoadAttempts(ctx context.Context, id inspection.TaskID) ([]evidence.AdapterAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instrument, call_key, task_id, request, result, retry_count, at, raw_error FROM attempts WHERE task_id=? ORDER BY at`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evidence.AdapterAttempt
	for rows.Next() {
		var a evidence.AdapterAttempt
		if err := rows.Scan(&a.Instrument, &a.CallKey, &a.TaskID, &a.Request, &a.Result, &a.RetryCount, &a.At, &a.RawError); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLite) LoadReviews(ctx context.Context, id inspection.TaskID) ([]arbiter.ReviewAndFinal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, reviewer, scope, rejudge_gen, final_type, credential_id, barrier_key FROM reviews WHERE task_id=?`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []arbiter.ReviewAndFinal
	for rows.Next() {
		var r arbiter.ReviewAndFinal
		var rej int64
		if err := rows.Scan(&r.TaskID, &r.Reviewer, &r.ScopeSummary, &rej, &r.FinalType, &r.CredentialID, &r.BarrierKey); err != nil {
			return nil, err
		}
		r.RejudgeGen = inspection.Generation(rej)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLite) LoadFinal(ctx context.Context, id inspection.TaskID) (*arbiter.ReviewAndFinal, error) {
	var r arbiter.ReviewAndFinal
	var rej int64
	err := s.db.QueryRowContext(ctx, `SELECT task_id, reviewer, scope, rejudge_gen, final_type, credential_id, barrier_key FROM finals WHERE task_id=?`, string(id)).
		Scan(&r.TaskID, &r.Reviewer, &r.ScopeSummary, &rej, &r.FinalType, &r.CredentialID, &r.BarrierKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.RejudgeGen = inspection.Generation(rej)
	return &r, nil
}

func (s *SQLite) ListTasks(ctx context.Context) ([]inspection.InspectionTask, error) {
	rows, err := s.db.QueryContext(ctx, taskSelect+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inspection.InspectionTask
	for rows.Next() {
		t, err := s.scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func newTaskID() string {
	return fmt.Sprintf("task_%d", time.Now().UnixNano())
}
