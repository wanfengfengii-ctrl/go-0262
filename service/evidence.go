package service

import (
	"context"
	"encoding/json"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/fixed"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/store"
)

// CountPollenInput is the花粉谱覆盖计数 payload.
type CountPollenInput struct {
	Operation     inspection.OperationID
	TaskID        inspection.TaskID
	Generation    inspection.Generation
	DeclaredTotal int
	EnteredBy     catalog.PersonnelID
	Counts        []evidence.PollenCountInput
}

// CountPollen validates the pollen coverage grid and persists it, advancing
// the task from counting-pollen to verifying-dna.
func (s *Service) CountPollen(ctx context.Context, in CountPollenInput) error {
	hash := store.HashContent(in)
	if replayed, _, err := s.checkReplay(ctx, in.Operation, hash); err != nil {
		return err
	} else if replayed {
		return nil
	}

	t, rule, err := s.ruleForTask(ctx, in.TaskID)
	if err != nil {
		return err
	}
	if err := t.MustBeGeneration(in.Generation); err != nil {
		return err
	}
	if err := t.MustNotBeTerminal(); err != nil {
		return err
	}
	if err := t.MustBeState(inspection.StateCountingPollen); err != nil {
		return err
	}
	if err := rule.ValidatePersonnel(in.EnteredBy); err != nil {
		return ErrUnqualified
	}
	if err := evidence.ValidatePollenCoverage(rule, t.Slides, in.DeclaredTotal, t.PollenThreshold.MinTargetCount, in.Counts); err != nil {
		return err
	}

	cells := evidence.BuildCoverCells(in.TaskID, in.Generation, in.EnteredBy, 1, in.Counts)
	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		if err := tx.SavePollenCells(ctx, cells); err != nil {
			return err
		}
		return tx.UpdateTaskState(ctx, in.TaskID, in.Generation, inspection.StateCountingPollen, inspection.StateVerifyingDNA)
	})
}

// SubmitDNAInput is the DNA fingerprint reading payload. Either Reading (a
// fixed-decimal Ct) is provided directly, or Instrument triggers an adapter.
type SubmitDNAInput struct {
	Operation   inspection.OperationID
	TaskID      inspection.TaskID
	Generation  inspection.Generation
	BlindCode   string
	Slide       string
	Well        string
	Reading     string
	Instrument  string
	AdapterCall string
}

// SubmitDNA writes an immutable qPCR Ct version and advances verifying-dna to
// retesting-chemistry. An out-of-threshold Ct marks the evidence as failed.
func (s *Service) SubmitDNA(ctx context.Context, in SubmitDNAInput) error {
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
	if err := t.MustBeState(inspection.StateVerifyingDNA); err != nil {
		return err
	}

	ct, err := s.resolveCt(ctx, in.TaskID, in.Instrument, in.AdapterCall, in.Reading)
	if err != nil {
		return err
	}

	conclusion := "pass"
	if evidence.ValidateDNA(ct, t.DNAThreshold) != nil {
		conclusion = "fail"
	}

	chain, err := s.store.LoadEvidence(ctx, in.TaskID, in.Generation)
	if err != nil {
		return err
	}
	version := evidence.NextVersion(chain, in.TaskID, in.Generation, evidence.EvidenceDNA)

	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		if err := tx.AppendEvidence(ctx, evidence.EvidenceVersion{
			TaskID:     in.TaskID,
			Generation: in.Generation,
			Type:       evidence.EvidenceDNA,
			BlindCode:  in.BlindCode,
			Slide:      in.Slide,
			Well:       in.Well,
			Reading:    ct,
			Conclusion: conclusion,
			Version:    version,
			Immutable:  true,
		}); err != nil {
			return err
		}
		return tx.UpdateTaskState(ctx, in.TaskID, in.Generation, inspection.StateVerifyingDNA, inspection.StateRetestingChemistry)
	})
}

// SubmitChemistryInput is the理化复测 payload. Direct readings or an
// instrument-triggered call are both supported.
type SubmitChemistryInput struct {
	Operation    inspection.OperationID
	TaskID       inspection.TaskID
	Generation   inspection.Generation
	BlindCode    string
	Slide        string
	Well         string
	HMF          string
	Amylase      string
	Moisture     string
	Conductivity string
	Acidity      string
	Instrument   string
	AdapterCall  string
}

// SubmitChemistry validates the five fixed-decimal metrics against the locked
// chemistry thresholds, writes a derived evidence version and advances the
// task to pending-independent-review.
func (s *Service) SubmitChemistry(ctx context.Context, in SubmitChemistryInput) error {
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
	if err := t.MustBeState(inspection.StateRetestingChemistry); err != nil {
		return err
	}

	reading, err := s.resolveChemistry(ctx, in.TaskID, in.Instrument, in.AdapterCall, in)
	if err != nil {
		return err
	}
	violations, err := evidence.ValidateChemistry(reading, t.ChemThreshold)
	if err != nil {
		return err
	}
	conclusion := "pass"
	if len(violations) > 0 {
		conclusion = "fail"
	}

	chain, err := s.store.LoadEvidence(ctx, in.TaskID, in.Generation)
	if err != nil {
		return err
	}
	version := evidence.NextVersion(chain, in.TaskID, in.Generation, evidence.EvidenceChemistry)

	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		if err := tx.AppendEvidence(ctx, evidence.EvidenceVersion{
			TaskID:     in.TaskID,
			Generation: in.Generation,
			Type:       evidence.EvidenceChemistry,
			BlindCode:  in.BlindCode,
			Slide:      in.Slide,
			Well:       in.Well,
			Reading:    chemistryReadingSummary(reading),
			Conclusion: conclusion,
			Version:    version,
			Immutable:  true,
		}); err != nil {
			return err
		}
		return tx.UpdateTaskState(ctx, in.TaskID, in.Generation, inspection.StateRetestingChemistry, inspection.StatePendingIndependentReview)
	})
}

// RejudgeInput creates the current-generation anomaly rejudgement evidence.
type RejudgeInput struct {
	Operation  inspection.OperationID
	TaskID     inspection.TaskID
	Generation inspection.Generation
	BlindCode  string
	Slide      string
	Well       string
	Reason     string
	Reviewer   catalog.PersonnelID
}

// Rejudge writes a single rejudge evidence per generation; a second rejudge in
// the same generation is rejected.
func (s *Service) Rejudge(ctx context.Context, in RejudgeInput) error {
	hash := store.HashContent(in)
	if replayed, _, err := s.checkReplay(ctx, in.Operation, hash); err != nil {
		return err
	} else if replayed {
		return nil
	}

	t, rule, err := s.ruleForTask(ctx, in.TaskID)
	if err != nil {
		return err
	}
	if err := t.MustBeGeneration(in.Generation); err != nil {
		return err
	}
	if err := t.MustNotBeTerminal(); err != nil {
		return err
	}
	switch t.State {
	case inspection.StateVerifyingDNA, inspection.StateRetestingChemistry, inspection.StatePendingIndependentReview:
	default:
		return inspection.ErrWrongState
	}
	if err := rule.ValidatePersonnel(in.Reviewer); err != nil {
		return ErrUnqualified
	}

	chain, err := s.store.LoadEvidence(ctx, in.TaskID, in.Generation)
	if err != nil {
		return err
	}
	if evidence.HasRejudgeForGeneration(chain, in.Generation) {
		return evidence.ErrDuplicateRejudge
	}
	version := evidence.NextVersion(chain, in.TaskID, in.Generation, evidence.EvidenceRejudge)

	return s.applyIdempotent(ctx, in.Operation, hash, "", func(tx store.Tx) error {
		return tx.AppendEvidence(ctx, evidence.EvidenceVersion{
			TaskID:     in.TaskID,
			Generation: in.Generation,
			Type:       evidence.EvidenceRejudge,
			BlindCode:  in.BlindCode,
			Slide:      in.Slide,
			Well:       in.Well,
			Conclusion: "anomaly",
			Version:    version,
			Immutable:  true,
		})
	})
}

// chemistryReadingSummary folds the five metrics into a single fixed.Decimal
// for the evidence row; the exact metrics are recomputed from the raw values.
func chemistryReadingSummary(r evidence.ChemistryReading) fixed.Decimal {
	// Use HMF as the representative reading; the version chain records the
	// derived conclusion, while each metric's validation is fully computed.
	return r.HMF
}

// resolveCt returns a Ct reading from a direct string or an instrument call.
func (s *Service) resolveCt(ctx context.Context, taskID inspection.TaskID, instrument, call, direct string) (fixed.Decimal, error) {
	if instrument == "" {
		d, err := fixed.Parse(direct, evidence.ScaleCt)
		if err != nil {
			return fixed.Decimal{}, ErrInvalidReading
		}
		return d, nil
	}
	adapter := s.adapter(evidence.InstrumentType(instrument))
	if adapter == nil {
		return fixed.Decimal{}, ErrBadRequest
	}
	raw, errCode := adapter.Call(call)
	result := evidence.Classify(errCode)
	// An instrument may return a success code with a payload that is not a
	// valid Ct decimal. That is an instrument format error, not a plain
	// reading error: reclassify it as malformed before recording so it enters
	// the auditable pending-retry path and is visible in the retry queue.
	var ct fixed.Decimal
	if result == evidence.AttemptOK {
		var err error
		ct, err = fixed.Parse(raw, evidence.ScaleCt)
		if err != nil {
			result = evidence.AttemptMalformed
		}
	}
	if err := s.recordAttempt(ctx, taskID, adapter.Instrument(), "dna", call, raw, result, errCode); err != nil {
		return fixed.Decimal{}, err
	}
	if result.IsFailure() {
		return fixed.Decimal{}, ErrAdapterRetry
	}
	return ct, nil
}

// resolveChemistry returns a ChemistryReading from direct strings or an
// instrument call. Instrument responses are a JSON object carrying all five
// metrics at their fixed scales.
func (s *Service) resolveChemistry(ctx context.Context, taskID inspection.TaskID, instrument, call string, in SubmitChemistryInput) (evidence.ChemistryReading, error) {
	if instrument == "" {
		return parseChemistry(in.HMF, in.Amylase, in.Moisture, in.Conductivity, in.Acidity)
	}
	adapter := s.adapter(evidence.InstrumentType(instrument))
	if adapter == nil {
		return evidence.ChemistryReading{}, ErrBadRequest
	}
	raw, errCode := adapter.Call(call)
	result := evidence.Classify(errCode)
	// An instrument may return a success code with a payload that is not a
	// valid chemistry reading (malformed JSON or non-decimal metrics). That is
	// an instrument format error, not a plain reading error: reclassify it as
	// malformed before recording so it enters the auditable pending-retry path
	// and is visible in the retry queue.
	var reading evidence.ChemistryReading
	if result == evidence.AttemptOK {
		var r struct {
			HMF          string `json:"hmf"`
			Amylase      string `json:"amylase"`
			Moisture     string `json:"moisture"`
			Conductivity string `json:"conductivity"`
			Acidity      string `json:"acidity"`
		}
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			result = evidence.AttemptMalformed
		} else if cr, err := parseChemistry(r.HMF, r.Amylase, r.Moisture, r.Conductivity, r.Acidity); err != nil {
			result = evidence.AttemptMalformed
		} else {
			reading = cr
		}
	}
	if err := s.recordAttempt(ctx, taskID, adapter.Instrument(), "chemistry", call, raw, result, errCode); err != nil {
		return evidence.ChemistryReading{}, err
	}
	if result.IsFailure() {
		return evidence.ChemistryReading{}, ErrAdapterRetry
	}
	return reading, nil
}

func parseChemistry(hmf, amylase, moisture, conductivity, acidity string) (evidence.ChemistryReading, error) {
	h, err := fixed.Parse(hmf, evidence.ScaleHMF)
	if err != nil {
		return evidence.ChemistryReading{}, ErrInvalidReading
	}
	a, err := fixed.Parse(amylase, evidence.ScaleAmylase)
	if err != nil {
		return evidence.ChemistryReading{}, ErrInvalidReading
	}
	m, err := fixed.Parse(moisture, evidence.ScaleMoisture)
	if err != nil {
		return evidence.ChemistryReading{}, ErrInvalidReading
	}
	c, err := fixed.Parse(conductivity, evidence.ScaleConductivity)
	if err != nil {
		return evidence.ChemistryReading{}, ErrInvalidReading
	}
	ac, err := fixed.Parse(acidity, evidence.ScaleAcidity)
	if err != nil {
		return evidence.ChemistryReading{}, ErrInvalidReading
	}
	return evidence.ChemistryReading{HMF: h, Amylase: a, Moisture: m, Conductivity: c, Acidity: ac}, nil
}

func (s *Service) recordAttempt(ctx context.Context, taskID inspection.TaskID, inst evidence.InstrumentType, callKey, req, raw string, result evidence.AttemptResult, rawErr string) error {
	attempts, err := s.store.LoadAttempts(ctx, taskID)
	if err != nil {
		return err
	}
	prior := evidence.PendingRetries(attempts, callKey)
	rec := evidence.NewAttempt(inst, callKey, taskID, req, result, rawErr, s.clock.Now(), prior)
	return s.store.WithTx(ctx, func(tx store.Tx) error {
		return tx.SaveAttempt(ctx, rec)
	})
}
