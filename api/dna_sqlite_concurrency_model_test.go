package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/service"
	"nectargate/raw-honey-maturation-intake/store"
)

type modelDNALoadBarrierStore struct {
	store.Store

	mu        sync.Mutex
	taskID    inspection.TaskID
	state     inspection.State
	remaining int
	wait      chan struct{}
}

func (s *modelDNALoadBarrierStore) arm(id inspection.TaskID, state inspection.State, parties int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taskID = id
	s.state = state
	s.remaining = parties
	s.wait = make(chan struct{})
}

func (s *modelDNALoadBarrierStore) LoadTask(ctx context.Context, id inspection.TaskID) (inspection.InspectionTask, error) {
	task, err := s.Store.LoadTask(ctx, id)
	if err != nil {
		return task, err
	}

	var wait <-chan struct{}
	s.mu.Lock()
	if s.wait != nil && id == s.taskID && task.State == s.state {
		s.remaining--
		wait = s.wait
		if s.remaining == 0 {
			close(s.wait)
			s.wait = nil
		}
	}
	s.mu.Unlock()
	if wait != nil {
		<-wait
	}
	return task, nil
}

type modelDNAResult struct {
	operation string
	status    int
	body      string
}

func TestModel_SQLiteDNAConcurrentAdvanceCAS(t *testing.T) {
	cases := []struct {
		name          string
		operations    []string
		readings      []string
		concurrent    bool
		wantOK        int
		wantConflicts int
	}{
		{
			name:       "single qPCR reading advances verifying DNA",
			operations: []string{"dna-single"},
			readings:   []string{"30.00"},
			wantOK:     1,
		},
		{
			name:          "stale concurrent qPCR reading conflicts and rolls back",
			operations:    []string{"dna-concurrent-a", "dna-concurrent-b"},
			readings:      []string{"30.00", "31.00"},
			concurrent:    true,
			wantOK:        1,
			wantConflicts: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "nectargate.db"))
			if err != nil {
				t.Fatalf("OpenSQLite: %v", err)
			}
			defer sqliteStore.Close()

			barrierStore := &modelDNALoadBarrierStore{Store: sqliteStore}
			svc := service.New(barrierStore, catalog.Seed(time.Now()), nil, nil)
			handler := NewServer(svc).Handler()
			taskID := modelStageTaskAtDNA(t, handler)

			if tc.concurrent {
				barrierStore.arm(inspection.TaskID(taskID), inspection.StateVerifyingDNA, len(tc.operations))
			}

			results := modelSubmitDNA(t, handler, taskID, tc.operations, tc.readings, tc.concurrent)
			ok, conflicts := 0, 0
			for _, result := range results {
				switch result.status {
				case http.StatusOK:
					ok++
				case http.StatusConflict:
					conflicts++
					var body errorBody
					if err := json.Unmarshal([]byte(result.body), &body); err != nil {
						t.Fatalf("%s conflict body is not JSON: %v: %s", result.operation, err, result.body)
					}
					if body.Code == "" || body.Code == CodeInternal {
						t.Fatalf("%s conflict used code %q, body %s", result.operation, body.Code, result.body)
					}
				default:
					t.Fatalf("%s status = %d, body %s", result.operation, result.status, result.body)
				}
			}
			if ok != tc.wantOK || conflicts != tc.wantConflicts {
				t.Fatalf("statuses got ok=%d conflicts=%d results=%+v, want ok=%d conflicts=%d", ok, conflicts, results, tc.wantOK, tc.wantConflicts)
			}

			ctx := context.Background()
			task, err := sqliteStore.LoadTask(ctx, inspection.TaskID(taskID))
			if err != nil {
				t.Fatalf("LoadTask: %v", err)
			}
			if task.State != inspection.StateRetestingChemistry {
				t.Fatalf("state = %s, want %s", task.State, inspection.StateRetestingChemistry)
			}

			versions, err := sqliteStore.LoadEvidence(ctx, inspection.TaskID(taskID), task.Generation)
			if err != nil {
				t.Fatalf("LoadEvidence: %v", err)
			}
			dnaVersions := 0
			for _, version := range versions {
				if version.Type == evidence.EvidenceDNA {
					dnaVersions++
				}
			}
			if dnaVersions != tc.wantOK {
				t.Fatalf("DNA evidence versions = %d, want %d: %+v", dnaVersions, tc.wantOK, versions)
			}

			savedOps := 0
			for _, op := range tc.operations {
				_, err := sqliteStore.LoadOperation(ctx, inspection.OperationID(op))
				switch {
				case err == nil:
					savedOps++
				case errors.Is(err, store.ErrNotFound):
				default:
					t.Fatalf("LoadOperation(%s): %v", op, err)
				}
			}
			if savedOps != tc.wantOK {
				t.Fatalf("saved DNA operations = %d, want %d", savedOps, tc.wantOK)
			}
		})
	}
}

func modelStageTaskAtDNA(t *testing.T, handler http.Handler) string {
	t.Helper()

	lockBody := `{"operation":"model-lock","farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":"B-MODEL-001","seal":"S-MODEL-001","zone":"4C","blind_code":"BC-MODEL-001","slides":["SL-MODEL-001"],"well":"W-MODEL-001","tank_slot":"TS-MODEL-001","samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`
	rec := modelDoJSON(handler, http.MethodPost, "/api/inspections/lock", lockBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("lock: %d %s", rec.Code, rec.Body.String())
	}
	var locked lockResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &locked); err != nil {
		t.Fatalf("decode lock response: %v", err)
	}

	steps := []struct {
		path string
		body string
	}{
		{"/sampling/confirm", `{"operation":"model-confirm","generation":1,"samplers":["alice","bob"],"barrel":"B-MODEL-001","seal":"S-MODEL-001"}`},
		{"/samples/seal", `{"operation":"model-seal","generation":1,"blind_code":"BC-MODEL-001","triplicates":["T-MODEL-1","T-MODEL-2","T-MODEL-3"],"sealed_by":"alice"}`},
		{"/leases/claim", `{"operation":"model-claim","generation":1,"tank_slot":"TS-MODEL-001","well":"W-MODEL-001","slides":["SL-MODEL-001"]}`},
		{"/pollen/counts", `{"operation":"model-pollen","generation":1,"declared_total":140,"entered_by":"alice","counts":[{"slide":"SL-MODEL-001","class":"target","count":120},{"slide":"SL-MODEL-001","class":"accompanying","count":10},{"slide":"SL-MODEL-001","class":"unknown","count":5},{"slide":"SL-MODEL-001","class":"contaminant","count":5}]}`},
	}
	for _, step := range steps {
		rec := modelDoJSON(handler, http.MethodPost, "/api/inspections/"+locked.TaskID+step.path, step.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step.path, rec.Code, rec.Body.String())
		}
	}
	return locked.TaskID
}

func modelSubmitDNA(t *testing.T, handler http.Handler, taskID string, operations, readings []string, concurrent bool) []modelDNAResult {
	t.Helper()
	results := make([]modelDNAResult, len(operations))
	post := func(i int) {
		body := `{"operation":"` + operations[i] + `","generation":1,"blind_code":"BC-MODEL-001","slide":"SL-MODEL-001","well":"W-MODEL-001","reading":"` + readings[i] + `"}`
		rec := modelDoJSON(handler, http.MethodPost, "/api/inspections/"+taskID+"/evidence/dna", body)
		results[i] = modelDNAResult{operation: operations[i], status: rec.Code, body: rec.Body.String()}
	}
	if !concurrent {
		for i := range operations {
			post(i)
		}
		return results
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range operations {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			post(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return results
}

func modelDoJSON(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
