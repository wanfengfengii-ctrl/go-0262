package api

import (
	"encoding/json"
	"net/http"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/service"
)

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid JSON body")
		return false
	}
	return true
}

type lockRequest struct {
	Operation   string   `json:"operation"`
	Farm        string   `json:"farm"`
	Season      string   `json:"season"`
	BatchID     string   `json:"batch_id"`
	Barrel      string   `json:"barrel"`
	Seal        string   `json:"seal"`
	Zone        string   `json:"zone"`
	BlindCode   string   `json:"blind_code"`
	Slides      []string `json:"slides"`
	Well        string   `json:"well"`
	TankSlot    string   `json:"tank_slot"`
	Samplers    []string `json:"samplers"`
	Reviewers   []string `json:"reviewers"`
	RuleVersion int64    `json:"rule_version"`
}

type lockResponse struct {
	TaskID     string   `json:"task_id"`
	Generation int64    `json:"generation"`
	State      string   `json:"state"`
	Occupied   []string `json:"occupied"`
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	var req lockRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := s.svc.Lock(r.Context(), service.LockInput{
		Operation:   inspection.OperationID(req.Operation),
		Farm:        catalog.FarmID(req.Farm),
		Season:      catalog.NectarSeason(req.Season),
		BatchID:     req.BatchID,
		Barrel:      req.Barrel,
		Seal:        req.Seal,
		Zone:        catalog.TemporaryZone(req.Zone),
		BlindCode:   req.BlindCode,
		Slides:      req.Slides,
		Well:        req.Well,
		TankSlot:    req.TankSlot,
		Samplers:    toPersonnel(req.Samplers),
		Reviewers:   toPersonnel(req.Reviewers),
		RuleVersion: req.RuleVersion,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lockResponse{
		TaskID:     string(res.TaskID),
		Generation: int64(res.Generation),
		State:      res.State.String(),
		Occupied:   res.Occupied,
	})
}

func toPersonnel(ss []string) []catalog.PersonnelID {
	out := make([]catalog.PersonnelID, 0, len(ss))
	for _, s := range ss {
		out = append(out, catalog.PersonnelID(s))
	}
	return out
}

func taskID(r *http.Request) inspection.TaskID { return inspection.TaskID(r.PathValue("id")) }

type samplingConfirmRequest struct {
	Operation  string   `json:"operation"`
	Generation int64    `json:"generation"`
	Samplers   []string `json:"samplers"`
	Barrel     string   `json:"barrel"`
	Seal       string   `json:"seal"`
}

func (s *Server) handleSamplingConfirm(w http.ResponseWriter, r *http.Request) {
	var req samplingConfirmRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.ConfirmSampling(r.Context(), service.ConfirmSamplingInput{
		Operation:  inspection.OperationID(req.Operation),
		TaskID:     taskID(r),
		Generation: inspection.Generation(req.Generation),
		Samplers:   toPersonnel(req.Samplers),
		Barrel:     req.Barrel,
		Seal:       req.Seal,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type sealRequest struct {
	Operation   string   `json:"operation"`
	Generation  int64    `json:"generation"`
	BlindCode   string   `json:"blind_code"`
	Triplicates []string `json:"triplicates"`
	SealedBy    string   `json:"sealed_by"`
}

func (s *Server) handleSealSamples(w http.ResponseWriter, r *http.Request) {
	var req sealRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.SealSamples(r.Context(), service.SealSamplesInput{
		Operation:   inspection.OperationID(req.Operation),
		TaskID:      taskID(r),
		Generation:  inspection.Generation(req.Generation),
		BlindCode:   ledger.BlindCode(req.BlindCode),
		Triplicates: req.Triplicates,
		SealedBy:    catalog.PersonnelID(req.SealedBy),
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type claimRequest struct {
	Operation  string   `json:"operation"`
	Generation int64    `json:"generation"`
	TankSlot   string   `json:"tank_slot"`
	Well       string   `json:"well"`
	Slides     []string `json:"slides"`
}

func (s *Server) handleClaimLeases(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.ClaimLeases(r.Context(), service.ClaimLeasesInput{
		Operation:  inspection.OperationID(req.Operation),
		TaskID:     taskID(r),
		Generation: inspection.Generation(req.Generation),
		TankSlot:   req.TankSlot,
		Well:       req.Well,
		Slides:     req.Slides,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type swapRequest struct {
	Operation  string `json:"operation"`
	Generation int64  `json:"generation"`
	OldWell    string `json:"old_well"`
	NewWell    string `json:"new_well"`
}

func (s *Server) handleSwapWell(w http.ResponseWriter, r *http.Request) {
	var req swapRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.SwapWell(r.Context(), service.SwapWellInput{
		Operation:  inspection.OperationID(req.Operation),
		TaskID:     taskID(r),
		Generation: inspection.Generation(req.Generation),
		OldWell:    req.OldWell,
		NewWell:    req.NewWell,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type pollenRequest struct {
	Operation     string `json:"operation"`
	Generation    int64  `json:"generation"`
	DeclaredTotal int    `json:"declared_total"`
	EnteredBy     string `json:"entered_by"`
	Counts        []struct {
		Slide string `json:"slide"`
		Class string `json:"class"`
		Count int    `json:"count"`
	} `json:"counts"`
}

func (s *Server) handlePollenCounts(w http.ResponseWriter, r *http.Request) {
	var req pollenRequest
	if !decode(w, r, &req) {
		return
	}
	counts := make([]evidence.PollenCountInput, 0, len(req.Counts))
	for _, c := range req.Counts {
		counts = append(counts, evidence.PollenCountInput{Slide: c.Slide, Class: catalog.PollenClass(c.Class), Count: c.Count})
	}
	err := s.svc.CountPollen(r.Context(), service.CountPollenInput{
		Operation:     inspection.OperationID(req.Operation),
		TaskID:        taskID(r),
		Generation:    inspection.Generation(req.Generation),
		DeclaredTotal: req.DeclaredTotal,
		EnteredBy:     catalog.PersonnelID(req.EnteredBy),
		Counts:        counts,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type dnaRequest struct {
	Operation   string `json:"operation"`
	Generation  int64  `json:"generation"`
	BlindCode   string `json:"blind_code"`
	Slide       string `json:"slide"`
	Well        string `json:"well"`
	Reading     string `json:"reading"`
	Instrument  string `json:"instrument"`
	AdapterCall string `json:"adapter_call"`
}

func (s *Server) handleDNA(w http.ResponseWriter, r *http.Request) {
	var req dnaRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.SubmitDNA(r.Context(), service.SubmitDNAInput{
		Operation:   inspection.OperationID(req.Operation),
		TaskID:      taskID(r),
		Generation:  inspection.Generation(req.Generation),
		BlindCode:   req.BlindCode,
		Slide:       req.Slide,
		Well:        req.Well,
		Reading:     req.Reading,
		Instrument:  req.Instrument,
		AdapterCall: req.AdapterCall,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type chemistryRequest struct {
	Operation    string `json:"operation"`
	Generation   int64  `json:"generation"`
	BlindCode    string `json:"blind_code"`
	Slide        string `json:"slide"`
	Well         string `json:"well"`
	HMF          string `json:"hmf"`
	Amylase      string `json:"amylase"`
	Moisture     string `json:"moisture"`
	Conductivity string `json:"conductivity"`
	Acidity      string `json:"acidity"`
	Instrument   string `json:"instrument"`
	AdapterCall  string `json:"adapter_call"`
}

func (s *Server) handleChemistry(w http.ResponseWriter, r *http.Request) {
	var req chemistryRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.SubmitChemistry(r.Context(), service.SubmitChemistryInput{
		Operation:    inspection.OperationID(req.Operation),
		TaskID:       taskID(r),
		Generation:   inspection.Generation(req.Generation),
		BlindCode:    req.BlindCode,
		Slide:        req.Slide,
		Well:         req.Well,
		HMF:          req.HMF,
		Amylase:      req.Amylase,
		Moisture:     req.Moisture,
		Conductivity: req.Conductivity,
		Acidity:      req.Acidity,
		Instrument:   req.Instrument,
		AdapterCall:  req.AdapterCall,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type rejudgeRequest struct {
	Operation  string `json:"operation"`
	Generation int64  `json:"generation"`
	BlindCode  string `json:"blind_code"`
	Slide      string `json:"slide"`
	Well       string `json:"well"`
	Reason     string `json:"reason"`
	Reviewer   string `json:"reviewer"`
}

func (s *Server) handleRejudge(w http.ResponseWriter, r *http.Request) {
	var req rejudgeRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.Rejudge(r.Context(), service.RejudgeInput{
		Operation:  inspection.OperationID(req.Operation),
		TaskID:     taskID(r),
		Generation: inspection.Generation(req.Generation),
		BlindCode:  req.BlindCode,
		Slide:      req.Slide,
		Well:       req.Well,
		Reason:     req.Reason,
		Reviewer:   catalog.PersonnelID(req.Reviewer),
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type reviewRequest struct {
	Operation  string `json:"operation"`
	Generation int64  `json:"generation"`
	Reviewer   string `json:"reviewer"`
	Scope      string `json:"scope"`
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	var req reviewRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.AddReview(r.Context(), service.AddReviewInput{
		Operation:  inspection.OperationID(req.Operation),
		TaskID:     taskID(r),
		Generation: inspection.Generation(req.Generation),
		Reviewer:   catalog.PersonnelID(req.Reviewer),
		Scope:      req.Scope,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type finalizeRequest struct {
	Operation  string `json:"operation"`
	Generation int64  `json:"generation"`
	Reviewer   string `json:"reviewer"`
	Decision   string `json:"decision"`
}

type finalizeResponse struct {
	FinalType    string `json:"final_type"`
	CredentialID string `json:"credential_id"`
}

func (s *Server) handleFinalize(w http.ResponseWriter, r *http.Request) {
	var req finalizeRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := s.svc.Finalize(r.Context(), service.FinalizeInput{
		Operation:  inspection.OperationID(req.Operation),
		TaskID:     taskID(r),
		Generation: inspection.Generation(req.Generation),
		Reviewer:   catalog.PersonnelID(req.Reviewer),
		Decision:   arbiter.Decision(req.Decision),
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, finalizeResponse{FinalType: string(res.FinalType), CredentialID: res.CredentialID})
}

type revealRequest struct {
	Generation int64 `json:"generation"`
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	var req revealRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.svc.Reveal(r.Context(), service.RevealInput{
		TaskID:     taskID(r),
		Generation: inspection.Generation(req.Generation),
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
