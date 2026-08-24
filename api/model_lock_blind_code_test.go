package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/api"
	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/service"
	"nectargate/raw-honey-maturation-intake/store"
)

func TestModel_LockRejectsDuplicateBlindCodeForOpenTasks(t *testing.T) {
	st := store.NewMemory()
	srv := api.NewServer(service.New(st, catalog.Seed(time.Now()), nil, nil))
	handler := srv.Handler()

	type lockPayload struct {
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
		TaskID string `json:"task_id"`
		State  string `json:"state"`
	}
	type errorResponse struct {
		Code string `json:"code"`
	}

	lock := func(operation, barrel, seal, blind string) lockPayload {
		return lockPayload{
			Operation:   operation,
			Farm:        "farm-01",
			Season:      "spring-2026",
			BatchID:     "batch-01",
			Barrel:      barrel,
			Seal:        seal,
			Zone:        "4C",
			BlindCode:   blind,
			Slides:      []string{"SL-" + barrel},
			Well:        "W-" + barrel,
			TankSlot:    "TS-" + barrel,
			Samplers:    []string{"alice", "bob"},
			Reviewers:   []string{"carol", "dave"},
			RuleVersion: 1,
		}
	}

	cases := []struct {
		name          string
		payload       lockPayload
		wantStatus    int
		wantState     string
		wantErrorCode string
		wantTaskCount int
	}{
		{
			name:          "first open task reserves the blind code",
			payload:       lock("model-lock-open-1", "MODEL-B-001", "MODEL-S-001", "MODEL-BC-SHARED"),
			wantStatus:    http.StatusOK,
			wantState:     "pending_sampling_confirm",
			wantTaskCount: 1,
		},
		{
			name:          "second open task with same blind code is rejected before creation",
			payload:       lock("model-lock-open-2", "MODEL-B-002", "MODEL-S-002", "MODEL-BC-SHARED"),
			wantStatus:    http.StatusConflict,
			wantErrorCode: "CONFLICT",
			wantTaskCount: 1,
		},
		{
			name:          "the rejected request did not occupy its barrel or seal",
			payload:       lock("model-lock-open-3", "MODEL-B-002", "MODEL-S-002", "MODEL-BC-UNIQUE"),
			wantStatus:    http.StatusOK,
			wantState:     "pending_sampling_confirm",
			wantTaskCount: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal lock payload: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/inspections/lock", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("lock status = %d, want %d, body %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantErrorCode != "" {
				var got errorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if got.Code != tc.wantErrorCode {
					t.Fatalf("error code = %q, want %q", got.Code, tc.wantErrorCode)
				}
			}
			if tc.wantState != "" {
				var got lockResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("decode lock response: %v", err)
				}
				if got.TaskID == "" {
					t.Fatal("successful lock returned empty task id")
				}
				if got.State != tc.wantState {
					t.Fatalf("state = %q, want %q", got.State, tc.wantState)
				}
			}

			tasks, err := st.ListTasks(context.Background())
			if err != nil {
				t.Fatalf("ListTasks: %v", err)
			}
			if len(tasks) != tc.wantTaskCount {
				t.Fatalf("task count = %d, want %d", len(tasks), tc.wantTaskCount)
			}
		})
	}
}
