package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/service"
	"nectargate/raw-honey-maturation-intake/store"
)

func TestModel_LockOperationReplayPreservesFirstResponse(t *testing.T) {
	cat := catalog.Seed(time.Now())
	srv := NewServer(service.New(store.NewMemory(), cat, nil, nil))
	handler := srv.Handler()

	postJSON := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	decodeLock := func(t *testing.T, rec *httptest.ResponseRecorder) lockResponse {
		t.Helper()
		var resp lockResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode lock response: %v; body=%s", err, rec.Body.String())
		}
		return resp
	}
	decodeError := func(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
		t.Helper()
		var resp errorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode error response: %v; body=%s", err, rec.Body.String())
		}
		return resp
	}

	const lockBody = `{"operation":"model-lock-op","farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":"B-MODEL-01","seal":"S-MODEL-01","zone":"4C","blind_code":"BC-MODEL-01","slides":["SL-MODEL-01"],"well":"W-MODEL-01","tank_slot":"TS-MODEL-01","samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`
	const sameOperationDifferentContent = `{"operation":"model-lock-op","farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":"B-MODEL-02","seal":"S-MODEL-02","zone":"4C","blind_code":"BC-MODEL-02","slides":["SL-MODEL-02"],"well":"W-MODEL-02","tank_slot":"TS-MODEL-02","samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`
	const duplicateFirstResources = `{"operation":"model-lock-duplicate-resources","farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":"B-MODEL-01","seal":"S-MODEL-01","zone":"4C","blind_code":"BC-MODEL-03","slides":["SL-MODEL-03"],"well":"W-MODEL-03","tank_slot":"TS-MODEL-03","samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`
	const alteredResourcesNewOperation = `{"operation":"model-lock-altered-resources","farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":"B-MODEL-02","seal":"S-MODEL-02","zone":"4C","blind_code":"BC-MODEL-02","slides":["SL-MODEL-02"],"well":"W-MODEL-02","tank_slot":"TS-MODEL-02","samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`

	var firstBody string
	var first lockResponse
	var replay lockResponse

	cases := []struct {
		name       string
		path       func() string
		body       func() string
		wantStatus int
		check      func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:       "first successful lock freezes task response",
			path:       func() string { return "/api/inspections/lock" },
			body:       func() string { return lockBody },
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				firstBody = rec.Body.String()
				first = decodeLock(t, rec)
				if first.TaskID == "" {
					t.Fatalf("first task_id is empty: %+v", first)
				}
				if first.Generation != 1 {
					t.Fatalf("first generation = %d, want 1", first.Generation)
				}
				if first.State != "pending_sampling_confirm" {
					t.Fatalf("first state = %q, want pending_sampling_confirm", first.State)
				}
				if want := []string{"B-MODEL-01", "S-MODEL-01"}; !reflect.DeepEqual(first.Occupied, want) {
					t.Fatalf("first occupied = %#v, want %#v", first.Occupied, want)
				}
			},
		},
		{
			name:       "same operation replay returns identical status and JSON",
			path:       func() string { return "/api/inspections/lock" },
			body:       func() string { return lockBody },
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if rec.Body.String() != firstBody {
					t.Fatalf("replay body changed:\nfirst:  %s\nreplay: %s", firstBody, rec.Body.String())
				}
				replay = decodeLock(t, rec)
				if !reflect.DeepEqual(replay, first) {
					t.Fatalf("replay response = %+v, want %+v", replay, first)
				}
			},
		},
		{
			name: "sampling can continue from replayed task id and generation",
			path: func() string {
				return "/api/inspections/" + replay.TaskID + "/sampling/confirm"
			},
			body: func() string {
				return fmt.Sprintf(`{"operation":"model-confirm-from-replay","generation":%d,"samplers":["alice","bob"],"barrel":"B-MODEL-01","seal":"S-MODEL-01"}`, replay.Generation)
			},
			wantStatus: http.StatusOK,
			check:      func(t *testing.T, rec *httptest.ResponseRecorder) {},
		},
		{
			name:       "same lock replay remains the original JSON after progress",
			path:       func() string { return "/api/inspections/lock" },
			body:       func() string { return lockBody },
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if rec.Body.String() != firstBody {
					t.Fatalf("post-progress replay body changed:\nfirst:  %s\nreplay: %s", firstBody, rec.Body.String())
				}
			},
		},
		{
			name:       "same operation with different content is rejected",
			path:       func() string { return "/api/inspections/lock" },
			body:       func() string { return sameOperationDifferentContent },
			wantStatus: http.StatusConflict,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if got := decodeError(t, rec).Code; got != CodeOperationConflict {
					t.Fatalf("error code = %q, want %q; body=%s", got, CodeOperationConflict, rec.Body.String())
				}
			},
		},
		{
			name:       "first locked resources remain uniquely occupied",
			path:       func() string { return "/api/inspections/lock" },
			body:       func() string { return duplicateFirstResources },
			wantStatus: http.StatusConflict,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if got := decodeError(t, rec).Code; got != CodeConflict {
					t.Fatalf("error code = %q, want %q; body=%s", got, CodeConflict, rec.Body.String())
				}
			},
		},
		{
			name:       "conflicted altered request did not occupy its resources",
			path:       func() string { return "/api/inspections/lock" },
			body:       func() string { return alteredResourcesNewOperation },
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				got := decodeLock(t, rec)
				if got.TaskID == "" || got.Generation != 1 || got.State != "pending_sampling_confirm" {
					t.Fatalf("altered resources lock response = %+v", got)
				}
				if want := []string{"B-MODEL-02", "S-MODEL-02"}; !reflect.DeepEqual(got.Occupied, want) {
					t.Fatalf("altered resources occupied = %#v, want %#v", got.Occupied, want)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(tc.path(), tc.body())
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			tc.check(t, rec)
		})
	}
}
