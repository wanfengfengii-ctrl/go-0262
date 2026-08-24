// Package api exposes the JSON HTTP API for the NectarGate intake backend:
// JSON payloads, stable error codes, deterministic reason ordering, store
// initialization, startup recovery and a health check.
//
// Component: Go HTTP API.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"nectargate/raw-honey-maturation-intake/arbiter"
	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/service"
)

// Stable error codes returned to clients.
const (
	CodeBadRequest         = "BAD_REQUEST"
	CodeOperationConflict  = "OPERATION_CONFLICT"
	CodeFarmMismatch       = "FARM_MISMATCH"
	CodeSeasonMismatch     = "SEASON_MISMATCH"
	CodeStaleSummary       = "STALE_SUMMARY"
	CodeZoneOutOfRange     = "ZONE_OUT_OF_RANGE"
	CodeUnqualified        = "UNQUALIFIED_PERSONNEL"
	CodeStaleRule          = "STALE_RULE"
	CodeNotFound           = "NOT_FOUND"
	CodeInternal           = "INTERNAL_ERROR"
	CodeConflict           = "CONFLICT"
	CodeTerminalState      = "TERMINAL_STATE"
	CodePersonnelOverlap   = "PERSONNEL_OVERLAP"
	CodeInvalidReading     = "INVALID_READING"
	CodeAdapterRetry       = "ADAPTER_RETRY"
	CodeInvalidTransition  = "INVALID_TRANSITION"
	CodeGenerationMismatch = "GENERATION_MISMATCH"
)

// Server wires the service into an HTTP handler.
type Server struct {
	svc *service.Service
}

// NewServer constructs a Server from a service.
func NewServer(svc *service.Service) *Server {
	return &Server{svc: svc}
}

// Handler returns the routed HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /api/inspections/lock", s.handleLock)
	mux.HandleFunc("GET /api/inspections/{id}", s.handleGetTask)
	mux.HandleFunc("POST /api/inspections/{id}/sampling/confirm", s.handleSamplingConfirm)
	mux.HandleFunc("POST /api/inspections/{id}/samples/seal", s.handleSealSamples)
	mux.HandleFunc("POST /api/inspections/{id}/leases/claim", s.handleClaimLeases)
	mux.HandleFunc("POST /api/inspections/{id}/leases/swap-well", s.handleSwapWell)
	mux.HandleFunc("POST /api/inspections/{id}/pollen/counts", s.handlePollenCounts)
	mux.HandleFunc("POST /api/inspections/{id}/evidence/dna", s.handleDNA)
	mux.HandleFunc("POST /api/inspections/{id}/evidence/chemistry", s.handleChemistry)
	mux.HandleFunc("POST /api/inspections/{id}/rejudge", s.handleRejudge)
	mux.HandleFunc("POST /api/inspections/{id}/reviews", s.handleReview)
	mux.HandleFunc("POST /api/inspections/{id}/finalize", s.handleFinalize)
	mux.HandleFunc("POST /api/inspections/{id}/blind/reveal", s.handleReveal)
	return mux
}

type errorBody struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Generation int64    `json:"generation,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, reasons ...string) {
	sort.Strings(reasons)
	writeJSON(w, status, errorBody{Code: code, Message: msg, Reasons: reasons})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := inspection.TaskID(r.PathValue("id"))
	t, err := s.svc.GetTask(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, taskResponse{
		TaskID:     string(t.ID),
		Generation: int64(t.Generation),
		State:      t.State.String(),
		Farm:       string(t.Farm),
		Barrel:     t.Barrel,
		Seal:       t.Seal,
	})
}

type taskResponse struct {
	TaskID     string `json:"task_id"`
	Generation int64  `json:"generation"`
	State      string `json:"state"`
	Farm       string `json:"farm"`
	Barrel     string `json:"barrel"`
	Seal       string `json:"seal"`
}

// writeServiceError maps a service/domain error to a stable HTTP response.
func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	code, status, msg := mapError(err)
	writeError(w, status, code, msg)
}

func mapError(err error) (code string, status int, msg string) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return CodeNotFound, http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrBadRequest), errors.Is(err, service.ErrUnboundEvidence):
		return CodeBadRequest, http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrOperationConflict):
		return CodeOperationConflict, http.StatusConflict, err.Error()
	case errors.Is(err, service.ErrResourceOccupied):
		return CodeConflict, http.StatusConflict, err.Error()
	case errors.Is(err, service.ErrTerminalState), errors.Is(err, inspection.ErrTerminalState):
		return CodeTerminalState, http.StatusConflict, err.Error()
	case errors.Is(err, service.ErrUnqualified), errors.Is(err, catalog.ErrUnqualified):
		return CodeUnqualified, http.StatusForbidden, err.Error()
	case errors.Is(err, service.ErrOverlap), errors.Is(err, inspection.ErrSamplerReviewerOverlap), errors.Is(err, arbiter.ErrReviewerOverlap), errors.Is(err, arbiter.ErrReviewerDuplicate):
		return CodePersonnelOverlap, http.StatusConflict, err.Error()
	case errors.Is(err, service.ErrInvalidReading):
		return CodeInvalidReading, http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrAdapterRetry):
		return CodeAdapterRetry, http.StatusAccepted, err.Error()
	case errors.Is(err, catalog.ErrFarmMismatch), errors.Is(err, catalog.ErrUnknownFarm):
		return CodeFarmMismatch, http.StatusBadRequest, err.Error()
	case errors.Is(err, catalog.ErrSeasonMismatch):
		return CodeSeasonMismatch, http.StatusBadRequest, err.Error()
	case errors.Is(err, catalog.ErrStaleSummary):
		return CodeStaleSummary, http.StatusBadRequest, err.Error()
	case errors.Is(err, catalog.ErrZoneOutOfRange):
		return CodeZoneOutOfRange, http.StatusBadRequest, err.Error()
	case errors.Is(err, catalog.ErrStaleRule):
		return CodeStaleRule, http.StatusConflict, err.Error()
	case errors.Is(err, inspection.ErrGenerationMismatch):
		return CodeGenerationMismatch, http.StatusConflict, err.Error()
	case errors.Is(err, inspection.ErrWrongState), errors.Is(err, inspection.ErrInvalidTransition):
		return CodeInvalidTransition, http.StatusConflict, err.Error()
	case errors.Is(err, inspection.ErrSamplerCount), errors.Is(err, inspection.ErrReviewerCount):
		return CodeBadRequest, http.StatusBadRequest, err.Error()
	case errors.Is(err, ledger.ErrTriplicateCount), errors.Is(err, ledger.ErrBlindCodeMismatch),
		errors.Is(err, ledger.ErrAlreadyRevealed), errors.Is(err, ledger.ErrPrematureReveal):
		return CodeBadRequest, http.StatusBadRequest, err.Error()
	case errors.Is(err, evidence.ErrNotLockedClass), errors.Is(err, evidence.ErrCountNotConserved),
		errors.Is(err, evidence.ErrCoverIncomplete), errors.Is(err, evidence.ErrNegativeCount),
		errors.Is(err, evidence.ErrPollenBelowThreshold), errors.Is(err, evidence.ErrDuplicateRejudge),
		errors.Is(err, evidence.ErrDNACtAboveThreshold), errors.Is(err, evidence.ErrScaleMismatchReading):
		return CodeInvalidReading, http.StatusBadRequest, err.Error()
	case errors.Is(err, arbiter.ErrNotEnoughReviewers), errors.Is(err, arbiter.ErrIncompleteEvidence),
		errors.Is(err, arbiter.ErrFinalAlreadySet), errors.Is(err, arbiter.ErrReviewerNotInList),
		errors.Is(err, arbiter.ErrUnknownDecision):
		return CodeConflict, http.StatusConflict, err.Error()
	default:
		return CodeInternal, http.StatusInternalServerError, err.Error()
	}
}
