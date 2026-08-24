package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/service"
	"nectargate/raw-honey-maturation-intake/store"
)

func newTestServer() *Server {
	cat := catalog.Seed(time.Now())
	svc := service.New(store.NewMemory(), cat, nil, nil)
	return NewServer(svc)
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	srv := newTestServer()
	rec := doJSON(t, srv.Handler(), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
}

func TestLockSuccess(t *testing.T) {
	srv := newTestServer()
	body := `{"operation":"op-1","farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":"B-001","seal":"S-001","zone":"4C","blind_code":"BC-001","slides":["SL-1"],"well":"W-001","tank_slot":"TS-001","samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`
	rec := doJSON(t, srv.Handler(), http.MethodPost, "/api/inspections/lock", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("lock status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp lockResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TaskID == "" || resp.Generation != 1 || resp.State != "pending_sampling_confirm" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestLockFarmMismatch(t *testing.T) {
	srv := newTestServer()
	body := `{"operation":"op-2","farm":"farm-99","season":"spring-2026","zone":"4C","rule_version":1}`
	rec := doJSON(t, srv.Handler(), http.MethodPost, "/api/inspections/lock", body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeFarmMismatch) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLockZoneOutOfRange(t *testing.T) {
	srv := newTestServer()
	body := `{"operation":"op-3","farm":"farm-01","season":"spring-2026","zone":"8C","rule_version":1}`
	rec := doJSON(t, srv.Handler(), http.MethodPost, "/api/inspections/lock", body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeZoneOutOfRange) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLockSamplerReviewerOverlap(t *testing.T) {
	srv := newTestServer()
	body := `{"operation":"op-4","farm":"farm-01","season":"spring-2026","zone":"4C","samplers":["alice","bob"],"reviewers":["alice","bob"],"rule_version":1}`
	rec := doJSON(t, srv.Handler(), http.MethodPost, "/api/inspections/lock", body)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodePersonnelOverlap) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFullFlow(t *testing.T) {
	srv := newTestServer()
	h := srv.Handler()

	lockBody := `{"operation":"op-1","farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":"B-001","seal":"S-001","zone":"4C","blind_code":"BC-001","slides":["SL-1"],"well":"W-001","tank_slot":"TS-001","samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`
	rec := doJSON(t, h, http.MethodPost, "/api/inspections/lock", lockBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("lock: %d %s", rec.Code, rec.Body.String())
	}
	var lr lockResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	id := lr.TaskID

	steps := []struct {
		path string
		body string
	}{
		{"/sampling/confirm", `{"operation":"c1","generation":1,"samplers":["alice","bob"],"barrel":"B-001","seal":"S-001"}`},
		{"/samples/seal", `{"operation":"s1","generation":1,"blind_code":"BC-001","triplicates":["T-1","T-2","T-3"],"sealed_by":"alice"}`},
		{"/leases/claim", `{"operation":"l1","generation":1,"tank_slot":"TS-001","well":"W-001","slides":["SL-1"]}`},
		{"/pollen/counts", `{"operation":"p1","generation":1,"declared_total":140,"entered_by":"alice","counts":[{"slide":"SL-1","class":"target","count":120},{"slide":"SL-1","class":"accompanying","count":10},{"slide":"SL-1","class":"unknown","count":5},{"slide":"SL-1","class":"contaminant","count":5}]}`},
		{"/evidence/dna", `{"operation":"d1","generation":1,"blind_code":"BC-001","slide":"SL-1","well":"W-001","reading":"30.00"}`},
		{"/evidence/chemistry", `{"operation":"ch1","generation":1,"blind_code":"BC-001","slide":"SL-1","well":"W-001","hmf":"20.0","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`},
		{"/reviews", `{"operation":"r1","generation":1,"reviewer":"carol","scope":"closed"}`},
		{"/reviews", `{"operation":"r2","generation":1,"reviewer":"dave","scope":"closed"}`},
	}
	for _, st := range steps {
		rec := doJSON(t, h, http.MethodPost, "/api/inspections/"+id+st.path, st.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", st.path, rec.Code, rec.Body.String())
		}
	}

	rec = doJSON(t, h, http.MethodPost, "/api/inspections/"+id+"/finalize", `{"operation":"f1","generation":1,"reviewer":"carol"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("finalize: %d %s", rec.Code, rec.Body.String())
	}
	var fr finalizeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &fr)
	if fr.FinalType != "matured" {
		t.Fatalf("final_type = %s, want matured", fr.FinalType)
	}
}
