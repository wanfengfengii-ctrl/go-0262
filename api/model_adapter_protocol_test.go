package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/service"
	"nectargate/raw-honey-maturation-intake/store"
)

const modelValidChemistryRaw = `{"hmf":"20.0","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`

type modelRequestWant struct {
	path       string
	body       map[string]any
	statusCode int
	errorCode  string
}

type modelAttemptWant struct {
	instrument evidence.InstrumentType
	callKey    string
	request    string
	result     evidence.AttemptResult
	retryCount int
	rawError   string
}

type modelEvidenceWant struct {
	typ        evidence.EvidenceType
	reading    string
	conclusion string
}

func TestModel_AdapterSuccessPayloadProtocolHandling(t *testing.T) {
	cases := []struct {
		name         string
		stage        inspection.State
		adapters     map[evidence.InstrumentType]evidence.InstrumentAdapter
		requests     []modelRequestWant
		attempts     []modelAttemptWant
		pendingKey   string
		pendingCount int
		evidence     []modelEvidenceWant
		state        string
	}{
		{
			name:  "dna adapter success code with malformed Ct is retryable",
			stage: inspection.StateVerifyingDNA,
			adapters: map[evidence.InstrumentType]evidence.InstrumentAdapter{
				evidence.InstrumentQPCR: evidence.NewScriptedAdapter(evidence.InstrumentQPCR, "Ct:not-a-decimal", ""),
			},
			requests: []modelRequestWant{
				{path: "/evidence/dna", body: modelDNABody("model-dna-malformed-1", "", "qpcr", "dna-malformed-run"), statusCode: http.StatusAccepted, errorCode: CodeAdapterRetry},
				{path: "/evidence/dna", body: modelDNABody("model-dna-malformed-2", "", "qpcr", "dna-malformed-run"), statusCode: http.StatusAccepted, errorCode: CodeAdapterRetry},
			},
			attempts: []modelAttemptWant{
				{instrument: evidence.InstrumentQPCR, callKey: "dna", request: "dna-malformed-run", result: evidence.AttemptMalformed, retryCount: 1},
				{instrument: evidence.InstrumentQPCR, callKey: "dna", request: "dna-malformed-run", result: evidence.AttemptMalformed, retryCount: 2},
			},
			pendingKey:   "dna",
			pendingCount: 2,
			state:        "verifying_dna",
		},
		{
			name:  "chemistry adapter success code with malformed metrics is retryable",
			stage: inspection.StateRetestingChemistry,
			adapters: map[evidence.InstrumentType]evidence.InstrumentAdapter{
				evidence.InstrumentSpectro: evidence.NewScriptedAdapter(evidence.InstrumentSpectro, `{"hmf":"NaN","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`, ""),
			},
			requests: []modelRequestWant{
				{path: "/evidence/chemistry", body: modelChemistryBody("model-chem-malformed-1", "", "", "spectrophotometer", "chem-malformed-run"), statusCode: http.StatusAccepted, errorCode: CodeAdapterRetry},
				{path: "/evidence/chemistry", body: modelChemistryBody("model-chem-malformed-2", "", "", "spectrophotometer", "chem-malformed-run"), statusCode: http.StatusAccepted, errorCode: CodeAdapterRetry},
			},
			attempts: []modelAttemptWant{
				{instrument: evidence.InstrumentSpectro, callKey: "chemistry", request: "chem-malformed-run", result: evidence.AttemptMalformed, retryCount: 1},
				{instrument: evidence.InstrumentSpectro, callKey: "chemistry", request: "chem-malformed-run", result: evidence.AttemptMalformed, retryCount: 2},
			},
			pendingKey:   "chemistry",
			pendingCount: 2,
			evidence: []modelEvidenceWant{
				{typ: evidence.EvidenceDNA, reading: "30.00", conclusion: "pass"},
			},
			state: "retesting_chemistry",
		},
		{
			name:  "direct malformed DNA reading remains invalid reading",
			stage: inspection.StateVerifyingDNA,
			requests: []modelRequestWant{
				{path: "/evidence/dna", body: modelDNABody("model-dna-direct-bad", "not-a-decimal", "", ""), statusCode: http.StatusBadRequest, errorCode: CodeInvalidReading},
			},
			state: "verifying_dna",
		},
		{
			name:  "direct malformed chemistry field remains invalid reading",
			stage: inspection.StateRetestingChemistry,
			requests: []modelRequestWant{
				{path: "/evidence/chemistry", body: modelChemistryBody("model-chem-direct-bad", "bad-hmf", "12.0", "", ""), statusCode: http.StatusBadRequest, errorCode: CodeInvalidReading},
			},
			evidence: []modelEvidenceWant{
				{typ: evidence.EvidenceDNA, reading: "30.00", conclusion: "pass"},
			},
			state: "retesting_chemistry",
		},
		{
			name:  "valid qPCR adapter success writes DNA evidence and advances",
			stage: inspection.StateVerifyingDNA,
			adapters: map[evidence.InstrumentType]evidence.InstrumentAdapter{
				evidence.InstrumentQPCR: evidence.NewScriptedAdapter(evidence.InstrumentQPCR, "30.00", ""),
			},
			requests: []modelRequestWant{
				{path: "/evidence/dna", body: modelDNABody("model-dna-ok", "", "qpcr", "dna-ok-run"), statusCode: http.StatusOK},
			},
			attempts: []modelAttemptWant{
				{instrument: evidence.InstrumentQPCR, callKey: "dna", request: "dna-ok-run", result: evidence.AttemptOK},
			},
			pendingKey: "dna",
			evidence: []modelEvidenceWant{
				{typ: evidence.EvidenceDNA, reading: "30.00", conclusion: "pass"},
			},
			state: "retesting_chemistry",
		},
		{
			name:  "valid chemistry adapter success writes chemistry evidence and advances",
			stage: inspection.StateRetestingChemistry,
			adapters: map[evidence.InstrumentType]evidence.InstrumentAdapter{
				evidence.InstrumentSpectro: evidence.NewScriptedAdapter(evidence.InstrumentSpectro, modelValidChemistryRaw, ""),
			},
			requests: []modelRequestWant{
				{path: "/evidence/chemistry", body: modelChemistryBody("model-chem-ok", "", "", "spectrophotometer", "chem-ok-run"), statusCode: http.StatusOK},
			},
			attempts: []modelAttemptWant{
				{instrument: evidence.InstrumentSpectro, callKey: "chemistry", request: "chem-ok-run", result: evidence.AttemptOK},
			},
			pendingKey: "chemistry",
			evidence: []modelEvidenceWant{
				{typ: evidence.EvidenceDNA, reading: "30.00", conclusion: "pass"},
				{typ: evidence.EvidenceChemistry, reading: "20.0", conclusion: "pass"},
			},
			state: "pending_independent_review",
		},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := modelStartAPIFlow(t, i, c.stage, c.adapters)
			for _, req := range c.requests {
				rec := doJSON(t, env.handler, http.MethodPost, "/api/inspections/"+string(env.id)+req.path, modelJSON(t, req.body))
				if rec.Code != req.statusCode {
					t.Fatalf("%s status = %d, want %d; body %s", req.path, rec.Code, req.statusCode, rec.Body.String())
				}
				if req.errorCode != "" {
					var body errorBody
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatalf("decode error body: %v", err)
					}
					if body.Code != req.errorCode {
						t.Fatalf("%s error code = %s, want %s", req.path, body.Code, req.errorCode)
					}
				}
			}

			attempts, err := env.store.LoadAttempts(context.Background(), env.id)
			if err != nil {
				t.Fatalf("LoadAttempts: %v", err)
			}
			if len(attempts) != len(c.attempts) {
				t.Fatalf("attempt count = %d, want %d: %+v", len(attempts), len(c.attempts), attempts)
			}
			for j, want := range c.attempts {
				got := attempts[j]
				if got.Instrument != want.instrument || got.CallKey != want.callKey || got.Request != want.request ||
					got.Result != want.result || got.RetryCount != want.retryCount || got.RawError != want.rawError {
					t.Fatalf("attempt %d = %+v, want %+v", j, got, want)
				}
			}
			if c.pendingKey != "" && evidence.PendingRetries(attempts, c.pendingKey) != c.pendingCount {
				t.Fatalf("pending retries for %s = %d, want %d", c.pendingKey, evidence.PendingRetries(attempts, c.pendingKey), c.pendingCount)
			}

			versions, err := env.store.LoadEvidence(context.Background(), env.id, env.generation)
			if err != nil {
				t.Fatalf("LoadEvidence: %v", err)
			}
			if len(versions) != len(c.evidence) {
				t.Fatalf("evidence count = %d, want %d: %+v", len(versions), len(c.evidence), versions)
			}
			for _, want := range c.evidence {
				got, ok := modelFindEvidence(versions, want.typ)
				if !ok {
					t.Fatalf("missing evidence type %s in %+v", want.typ, versions)
				}
				if got.Reading.String() != want.reading || got.Conclusion != want.conclusion || !got.Immutable {
					t.Fatalf("evidence %s = %+v, want reading %s conclusion %s immutable", want.typ, got, want.reading, want.conclusion)
				}
			}

			rec := doJSON(t, env.handler, http.MethodGet, "/api/inspections/"+string(env.id), "")
			if rec.Code != http.StatusOK {
				t.Fatalf("get task status = %d, body %s", rec.Code, rec.Body.String())
			}
			var task taskResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
				t.Fatalf("decode task: %v", err)
			}
			if task.State != c.state {
				t.Fatalf("state = %s, want %s", task.State, c.state)
			}
		})
	}
}

type modelAPIEnv struct {
	handler    http.Handler
	store      store.Store
	id         inspection.TaskID
	generation inspection.Generation
}

func modelStartAPIFlow(t *testing.T, seq int, target inspection.State, adapters map[evidence.InstrumentType]evidence.InstrumentAdapter) modelAPIEnv {
	t.Helper()

	st := store.NewMemory()
	srv := NewServer(service.New(st, catalog.Seed(time.Now()), nil, adapters))
	h := srv.Handler()

	suffix := fmt.Sprintf("%02d", seq)
	lockBody := map[string]any{
		"operation":    "model-lock-" + suffix,
		"farm":         "farm-01",
		"season":       "spring-2026",
		"batch_id":     "batch-" + suffix,
		"barrel":       "B-" + suffix,
		"seal":         "S-" + suffix,
		"zone":         "4C",
		"blind_code":   "BC-00",
		"slides":       []string{"SL-1"},
		"well":         "W-00",
		"tank_slot":    "TS-00",
		"samplers":     []string{"alice", "bob"},
		"reviewers":    []string{"carol", "dave"},
		"rule_version": 1,
	}
	rec := doJSON(t, h, http.MethodPost, "/api/inspections/lock", modelJSON(t, lockBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("lock status = %d, body %s", rec.Code, rec.Body.String())
	}
	var lock lockResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lock); err != nil {
		t.Fatalf("decode lock: %v", err)
	}

	id := inspection.TaskID(lock.TaskID)
	gen := inspection.Generation(lock.Generation)
	modelPostOK(t, h, id, "/sampling/confirm", map[string]any{
		"operation":  "model-confirm-" + suffix,
		"generation": lock.Generation,
		"samplers":   []string{"alice", "bob"},
		"barrel":     "B-" + suffix,
		"seal":       "S-" + suffix,
	})
	modelPostOK(t, h, id, "/samples/seal", map[string]any{
		"operation":   "model-seal-" + suffix,
		"generation":  lock.Generation,
		"blind_code":  "BC-00",
		"triplicates": []string{"T-1", "T-2", "T-3"},
		"sealed_by":   "alice",
	})
	modelPostOK(t, h, id, "/leases/claim", map[string]any{
		"operation":  "model-claim-" + suffix,
		"generation": lock.Generation,
		"tank_slot":  "TS-00",
		"well":       "W-00",
		"slides":     []string{"SL-1"},
	})
	modelPostOK(t, h, id, "/pollen/counts", map[string]any{
		"operation":      "model-pollen-" + suffix,
		"generation":     lock.Generation,
		"declared_total": 140,
		"entered_by":     "alice",
		"counts": []map[string]any{
			{"slide": "SL-1", "class": "target", "count": 120},
			{"slide": "SL-1", "class": "accompanying", "count": 10},
			{"slide": "SL-1", "class": "unknown", "count": 5},
			{"slide": "SL-1", "class": "contaminant", "count": 5},
		},
	})
	if target == inspection.StateRetestingChemistry {
		modelPostOK(t, h, id, "/evidence/dna", modelDNABody("model-setup-dna-"+suffix, "30.00", "", ""))
	}

	return modelAPIEnv{handler: h, store: st, id: id, generation: gen}
}

func modelPostOK(t *testing.T, h http.Handler, id inspection.TaskID, path string, body map[string]any) {
	t.Helper()
	rec := doJSON(t, h, http.MethodPost, "/api/inspections/"+string(id)+path, modelJSON(t, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body %s", path, rec.Code, rec.Body.String())
	}
}

func modelDNABody(operation, reading, instrument, adapterCall string) map[string]any {
	body := map[string]any{
		"operation":  operation,
		"generation": 1,
		"blind_code": "BC-00",
		"slide":      "SL-1",
		"well":       "W-00",
	}
	if reading != "" {
		body["reading"] = reading
	}
	if instrument != "" {
		body["instrument"] = instrument
		body["adapter_call"] = adapterCall
	}
	return body
}

func modelChemistryBody(operation, hmf, amylase, instrument, adapterCall string) map[string]any {
	body := map[string]any{
		"operation":    operation,
		"generation":   1,
		"blind_code":   "BC-00",
		"slide":        "SL-1",
		"well":         "W-00",
		"hmf":          hmf,
		"amylase":      amylase,
		"moisture":     "16.0",
		"conductivity": "0.50",
		"acidity":      "30.0",
	}
	if instrument != "" {
		body["instrument"] = instrument
		body["adapter_call"] = adapterCall
	}
	return body
}

func modelFindEvidence(versions []evidence.EvidenceVersion, typ evidence.EvidenceType) (evidence.EvidenceVersion, bool) {
	for _, v := range versions {
		if v.Type == typ {
			return v, true
		}
	}
	return evidence.EvidenceVersion{}, false
}

func modelJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(b)
}
