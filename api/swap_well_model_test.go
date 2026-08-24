package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/service"
	"nectargate/raw-honey-maturation-intake/store"
)

func TestModel_SwapWellRequiresAllowedStageAndActiveOldLease(t *testing.T) {
	ctx := context.Background()

	type lockedTask struct {
		id         string
		generation int64
		label      string
		barrel     string
		seal       string
		blind      string
		slide      string
		well       string
		tank       string
	}

	newHarness := func() (*Server, http.Handler) {
		cat := catalog.Seed(time.Now())
		svc := service.New(store.NewMemory(), cat, nil, nil)
		srv := NewServer(svc)
		return srv, srv.Handler()
	}

	postJSON := func(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	lockTask := func(t *testing.T, h http.Handler, label, well string) lockedTask {
		t.Helper()
		task := lockedTask{
			label:  label,
			barrel: "B-" + label,
			seal:   "S-" + label,
			blind:  "BC-" + label,
			slide:  "SL-" + label,
			well:   well,
			tank:   "TS-" + label,
		}
		body := fmt.Sprintf(
			`{"operation":%q,"farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":%q,"seal":%q,"zone":"4C","blind_code":%q,"slides":[%q],"well":%q,"tank_slot":%q,"samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`,
			"lock-"+label, task.barrel, task.seal, task.blind, task.slide, task.well, task.tank,
		)
		rec := postJSON(t, h, "/api/inspections/lock", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("lock %s: status=%d body=%s", label, rec.Code, rec.Body.String())
		}
		var resp lockResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode lock response: %v", err)
		}
		task.id = resp.TaskID
		task.generation = resp.Generation
		return task
	}

	postStep := func(t *testing.T, h http.Handler, task lockedTask, suffix, body string) {
		t.Helper()
		rec := postJSON(t, h, "/api/inspections/"+task.id+suffix, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status=%d body=%s", task.label, suffix, rec.Code, rec.Body.String())
		}
	}

	advanceToCountingPollen := func(t *testing.T, h http.Handler, task lockedTask) {
		t.Helper()
		postStep(t, h, task, "/sampling/confirm", fmt.Sprintf(
			`{"operation":%q,"generation":%d,"samplers":["alice","bob"],"barrel":%q,"seal":%q}`,
			"confirm-"+task.label, task.generation, task.barrel, task.seal,
		))
		postStep(t, h, task, "/samples/seal", fmt.Sprintf(
			`{"operation":%q,"generation":%d,"blind_code":%q,"triplicates":["T-1-%s","T-2-%s","T-3-%s"],"sealed_by":"alice"}`,
			"seal-"+task.label, task.generation, task.blind, task.label, task.label, task.label,
		))
		postStep(t, h, task, "/leases/claim", fmt.Sprintf(
			`{"operation":%q,"generation":%d,"tank_slot":%q,"well":%q,"slides":[%q]}`,
			"claim-"+task.label, task.generation, task.tank, task.well, task.slide,
		))
	}

	swapWell := func(t *testing.T, h http.Handler, task lockedTask, op, oldWell, newWell string) *httptest.ResponseRecorder {
		t.Helper()
		return postJSON(t, h, "/api/inspections/"+task.id+"/leases/swap-well", fmt.Sprintf(
			`{"operation":%q,"generation":%d,"old_well":%q,"new_well":%q}`,
			op, task.generation, oldWell, newWell,
		))
	}

	hasWell := func(leases []ledger.ResourceLease, well string) bool {
		for _, lease := range leases {
			if lease.ResourceType == ledger.ResourceWell && lease.ResourceID == well {
				return true
			}
		}
		return false
	}

	hasActiveWell := func(leases []ledger.ResourceLease, well string) bool {
		for _, lease := range leases {
			if lease.ResourceType == ledger.ResourceWell && lease.ResourceID == well && lease.Status == ledger.LeaseActive {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name              string
		run               func(t *testing.T, h http.Handler) (inspection.TaskID, *httptest.ResponseRecorder)
		wantStatus        int
		wantCode          string
		wantLeaseCount    int
		wantActiveWells   []string
		wantNoActiveWells []string
		wantNoWellEntries []string
	}{
		{
			name: "rejects swap immediately after lock before sampling and resource claim",
			run: func(t *testing.T, h http.Handler) (inspection.TaskID, *httptest.ResponseRecorder) {
				task := lockTask(t, h, "EARLY", "W-EARLY-OLD")
				rec := swapWell(t, h, task, "swap-early", "W-EARLY-OLD", "W-EARLY-NEW")
				return inspection.TaskID(task.id), rec
			},
			wantStatus:        http.StatusConflict,
			wantCode:          CodeInvalidTransition,
			wantLeaseCount:    0,
			wantNoWellEntries: []string{"W-EARLY-OLD", "W-EARLY-NEW"},
		},
		{
			name: "rejects old well that was never an active lease for the task",
			run: func(t *testing.T, h http.Handler) (inspection.TaskID, *httptest.ResponseRecorder) {
				task := lockTask(t, h, "NEVER", "W-NEVER-CURRENT")
				advanceToCountingPollen(t, h, task)
				rec := swapWell(t, h, task, "swap-never", "W-NEVER-GHOST", "W-NEVER-NEW")
				return inspection.TaskID(task.id), rec
			},
			wantStatus:        http.StatusBadRequest,
			wantCode:          CodeBadRequest,
			wantLeaseCount:    3,
			wantActiveWells:   []string{"W-NEVER-CURRENT"},
			wantNoWellEntries: []string{"W-NEVER-GHOST", "W-NEVER-NEW"},
		},
		{
			name: "rejects an old well already released by the same task",
			run: func(t *testing.T, h http.Handler) (inspection.TaskID, *httptest.ResponseRecorder) {
				task := lockTask(t, h, "RELEASED", "W-RELEASED-OLD")
				advanceToCountingPollen(t, h, task)
				if rec := swapWell(t, h, task, "swap-released-first", "W-RELEASED-OLD", "W-RELEASED-CURRENT"); rec.Code != http.StatusOK {
					t.Fatalf("initial legal swap: status=%d body=%s", rec.Code, rec.Body.String())
				}
				rec := swapWell(t, h, task, "swap-released-second", "W-RELEASED-OLD", "W-RELEASED-NEW")
				return inspection.TaskID(task.id), rec
			},
			wantStatus:        http.StatusBadRequest,
			wantCode:          CodeBadRequest,
			wantLeaseCount:    4,
			wantActiveWells:   []string{"W-RELEASED-CURRENT"},
			wantNoActiveWells: []string{"W-RELEASED-OLD", "W-RELEASED-NEW"},
			wantNoWellEntries: []string{"W-RELEASED-NEW"},
		},
		{
			name: "rejects an old well released by a different task",
			run: func(t *testing.T, h http.Handler) (inspection.TaskID, *httptest.ResponseRecorder) {
				holder := lockTask(t, h, "OTHER", "W-OTHER-OLD")
				advanceToCountingPollen(t, h, holder)
				if rec := swapWell(t, h, holder, "swap-other-release", "W-OTHER-OLD", "W-OTHER-CURRENT"); rec.Code != http.StatusOK {
					t.Fatalf("holder swap: status=%d body=%s", rec.Code, rec.Body.String())
				}
				task := lockTask(t, h, "SUBJECT", "W-SUBJECT-CURRENT")
				advanceToCountingPollen(t, h, task)
				rec := swapWell(t, h, task, "swap-other-old", "W-OTHER-OLD", "W-SUBJECT-NEW")
				return inspection.TaskID(task.id), rec
			},
			wantStatus:        http.StatusBadRequest,
			wantCode:          CodeBadRequest,
			wantLeaseCount:    3,
			wantActiveWells:   []string{"W-SUBJECT-CURRENT"},
			wantNoWellEntries: []string{"W-OTHER-OLD", "W-SUBJECT-NEW"},
		},
		{
			name: "rejects occupied new well without releasing the old well",
			run: func(t *testing.T, h http.Handler) (inspection.TaskID, *httptest.ResponseRecorder) {
				task := lockTask(t, h, "CONFLICT", "W-CONFLICT-OLD")
				advanceToCountingPollen(t, h, task)
				holder := lockTask(t, h, "TAKEN", "W-TAKEN")
				advanceToCountingPollen(t, h, holder)
				rec := swapWell(t, h, task, "swap-conflict", "W-CONFLICT-OLD", "W-TAKEN")
				return inspection.TaskID(task.id), rec
			},
			wantStatus:        http.StatusConflict,
			wantCode:          CodeConflict,
			wantLeaseCount:    3,
			wantActiveWells:   []string{"W-CONFLICT-OLD"},
			wantNoActiveWells: []string{"W-TAKEN"},
		},
		{
			name: "allows swap from an active old well in an allowed stage",
			run: func(t *testing.T, h http.Handler) (inspection.TaskID, *httptest.ResponseRecorder) {
				task := lockTask(t, h, "LEGAL", "W-LEGAL-OLD")
				advanceToCountingPollen(t, h, task)
				rec := swapWell(t, h, task, "swap-legal", "W-LEGAL-OLD", "W-LEGAL-NEW")
				return inspection.TaskID(task.id), rec
			},
			wantStatus:        http.StatusOK,
			wantLeaseCount:    4,
			wantActiveWells:   []string{"W-LEGAL-NEW"},
			wantNoActiveWells: []string{"W-LEGAL-OLD"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, h := newHarness()
			taskID, rec := tc.run(t, h)
			if rec.Code != tc.wantStatus {
				t.Fatalf("swap status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if tc.wantCode != "" {
				var body errorBody
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if body.Code != tc.wantCode {
					t.Fatalf("error code=%q body=%s, want %q", body.Code, rec.Body.String(), tc.wantCode)
				}
			}

			leases, err := srv.svc.Store().LoadLeases(ctx, taskID)
			if err != nil {
				t.Fatalf("LoadLeases: %v", err)
			}
			if tc.wantLeaseCount >= 0 && len(leases) != tc.wantLeaseCount {
				t.Fatalf("lease count=%d leases=%+v, want %d", len(leases), leases, tc.wantLeaseCount)
			}
			for _, well := range tc.wantActiveWells {
				if !hasActiveWell(leases, well) {
					t.Fatalf("leases=%+v, want active well %s", leases, well)
				}
			}
			for _, well := range tc.wantNoActiveWells {
				if hasActiveWell(leases, well) {
					t.Fatalf("leases=%+v, did not want active well %s", leases, well)
				}
			}
			for _, well := range tc.wantNoWellEntries {
				if hasWell(leases, well) {
					t.Fatalf("leases=%+v, did not want any lease entry for well %s", leases, well)
				}
			}
		})
	}
}
