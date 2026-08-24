package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"nectargate/raw-honey-maturation-intake/inspection"
)

func TestModel_EvidenceEndpointsRejectForeignCurrentGenerationBindings(t *testing.T) {
	ctx := context.Background()

	type lockedIDs struct {
		barrel string
		seal   string
		blind  string
		slide  string
		well   string
		tank   string
	}

	mainIDs := lockedIDs{
		barrel: "B-MAIN",
		seal:   "S-MAIN",
		blind:  "BC-MAIN",
		slide:  "SL-MAIN",
		well:   "W-MAIN",
		tank:   "TS-MAIN",
	}
	foreignIDs := lockedIDs{
		barrel: "B-FOREIGN",
		seal:   "S-FOREIGN",
		blind:  "BC-FOREIGN",
		slide:  "SL-FOREIGN",
		well:   "W-FOREIGN",
		tank:   "TS-FOREIGN",
	}

	lockTask := func(t *testing.T, h http.Handler, op string, ids lockedIDs) lockResponse {
		t.Helper()
		body := fmt.Sprintf(`{"operation":%q,"farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":%q,"seal":%q,"zone":"4C","blind_code":%q,"slides":[%q],"well":%q,"tank_slot":%q,"samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}`,
			op, ids.barrel, ids.seal, ids.blind, ids.slide, ids.well, ids.tank)
		rec := doJSON(t, h, http.MethodPost, "/api/inspections/lock", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("lock %s: %d %s", op, rec.Code, rec.Body.String())
		}
		var resp lockResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode lock %s: %v", op, err)
		}
		return resp
	}

	postOK := func(t *testing.T, h http.Handler, path, body string) {
		t.Helper()
		rec := doJSON(t, h, http.MethodPost, path, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	advanceThroughLeases := func(t *testing.T, h http.Handler, lr lockResponse, ids lockedIDs, prefix string) {
		t.Helper()
		base := "/api/inspections/" + lr.TaskID
		postOK(t, h, base+"/sampling/confirm", fmt.Sprintf(`{"operation":%q,"generation":%d,"samplers":["alice","bob"],"barrel":%q,"seal":%q}`,
			prefix+"-confirm", lr.Generation, ids.barrel, ids.seal))
		postOK(t, h, base+"/samples/seal", fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"triplicates":["T-1","T-2","T-3"],"sealed_by":"alice"}`,
			prefix+"-seal", lr.Generation, ids.blind))
		postOK(t, h, base+"/leases/claim", fmt.Sprintf(`{"operation":%q,"generation":%d,"tank_slot":%q,"well":%q,"slides":[%q]}`,
			prefix+"-claim", lr.Generation, ids.tank, ids.well, ids.slide))
	}

	advanceToDNA := func(t *testing.T, h http.Handler, lr lockResponse, ids lockedIDs, prefix string) {
		t.Helper()
		advanceThroughLeases(t, h, lr, ids, prefix)
		postOK(t, h, "/api/inspections/"+lr.TaskID+"/pollen/counts", fmt.Sprintf(`{"operation":%q,"generation":%d,"declared_total":140,"entered_by":"alice","counts":[{"slide":%q,"class":"target","count":120},{"slide":%q,"class":"accompanying","count":10},{"slide":%q,"class":"unknown","count":5},{"slide":%q,"class":"contaminant","count":5}]}`,
			prefix+"-pollen", lr.Generation, ids.slide, ids.slide, ids.slide, ids.slide))
	}

	submitGoodDNA := func(t *testing.T, h http.Handler, lr lockResponse, ids lockedIDs, op string) {
		t.Helper()
		postOK(t, h, "/api/inspections/"+lr.TaskID+"/evidence/dna", fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"reading":"30.00"}`,
			op, lr.Generation, ids.blind, ids.slide, ids.well))
	}

	submitGoodChemistry := func(t *testing.T, h http.Handler, lr lockResponse, ids lockedIDs, op string) {
		t.Helper()
		postOK(t, h, "/api/inspections/"+lr.TaskID+"/evidence/chemistry", fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"hmf":"20.0","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`,
			op, lr.Generation, ids.blind, ids.slide, ids.well))
	}

	currentState := func(t *testing.T, srv *Server, taskID string) inspection.State {
		t.Helper()
		task, err := srv.svc.GetTask(ctx, inspection.TaskID(taskID))
		if err != nil {
			t.Fatalf("GetTask %s: %v", taskID, err)
		}
		return task.State
	}

	evidenceLen := func(t *testing.T, srv *Server, lr lockResponse) int {
		t.Helper()
		chain, err := srv.svc.Store().LoadEvidence(ctx, inspection.TaskID(lr.TaskID), inspection.Generation(lr.Generation))
		if err != nil {
			t.Fatalf("LoadEvidence %s: %v", lr.TaskID, err)
		}
		return len(chain)
	}

	finishCleanly := func(t *testing.T, h http.Handler, srv *Server, lr lockResponse) {
		t.Helper()
		base := "/api/inspections/" + lr.TaskID
		postOK(t, h, base+"/reviews", fmt.Sprintf(`{"operation":"review-carol","generation":%d,"reviewer":"carol","scope":"closed"}`, lr.Generation))
		postOK(t, h, base+"/reviews", fmt.Sprintf(`{"operation":"review-dave","generation":%d,"reviewer":"dave","scope":"closed"}`, lr.Generation))
		rec := doJSON(t, h, http.MethodPost, base+"/finalize", fmt.Sprintf(`{"operation":"finalize","generation":%d,"reviewer":"carol"}`, lr.Generation))
		if rec.Code != http.StatusOK {
			t.Fatalf("finalize: %d %s", rec.Code, rec.Body.String())
		}
		var final finalizeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &final); err != nil {
			t.Fatalf("decode final: %v", err)
		}
		if final.FinalType != "matured" {
			t.Fatalf("final_type = %s, want matured", final.FinalType)
		}
		if got := currentState(t, srv, lr.TaskID); got != inspection.StateMatured {
			t.Fatalf("state = %s, want matured", got)
		}
	}

	cases := []struct {
		name      string
		stage     string
		endpoint  string
		wantState inspection.State
		body      func(op string, gen int64, main, foreign lockedIDs) string
	}{
		{
			name:      "dna_foreign_blind",
			stage:     "dna",
			endpoint:  "evidence/dna",
			wantState: inspection.StateVerifyingDNA,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"reading":"30.00"}`,
					op, gen, foreign.blind, main.slide, main.well)
			},
		},
		{
			name:      "dna_foreign_slide",
			stage:     "dna",
			endpoint:  "evidence/dna",
			wantState: inspection.StateVerifyingDNA,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"reading":"30.00"}`,
					op, gen, main.blind, foreign.slide, main.well)
			},
		},
		{
			name:      "dna_foreign_well",
			stage:     "dna",
			endpoint:  "evidence/dna",
			wantState: inspection.StateVerifyingDNA,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"reading":"30.00"}`,
					op, gen, main.blind, main.slide, foreign.well)
			},
		},
		{
			name:      "chemistry_foreign_blind",
			stage:     "chemistry",
			endpoint:  "evidence/chemistry",
			wantState: inspection.StateRetestingChemistry,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"hmf":"20.0","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`,
					op, gen, foreign.blind, main.slide, main.well)
			},
		},
		{
			name:      "chemistry_foreign_slide",
			stage:     "chemistry",
			endpoint:  "evidence/chemistry",
			wantState: inspection.StateRetestingChemistry,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"hmf":"20.0","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`,
					op, gen, main.blind, foreign.slide, main.well)
			},
		},
		{
			name:      "chemistry_foreign_well",
			stage:     "chemistry",
			endpoint:  "evidence/chemistry",
			wantState: inspection.StateRetestingChemistry,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"hmf":"20.0","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`,
					op, gen, main.blind, main.slide, foreign.well)
			},
		},
		{
			name:      "rejudge_foreign_blind",
			stage:     "rejudge",
			endpoint:  "rejudge",
			wantState: inspection.StatePendingIndependentReview,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"reason":"foreign binding","reviewer":"carol"}`,
					op, gen, foreign.blind, main.slide, main.well)
			},
		},
		{
			name:      "rejudge_foreign_slide",
			stage:     "rejudge",
			endpoint:  "rejudge",
			wantState: inspection.StatePendingIndependentReview,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"reason":"foreign binding","reviewer":"carol"}`,
					op, gen, main.blind, foreign.slide, main.well)
			},
		},
		{
			name:      "rejudge_foreign_well",
			stage:     "rejudge",
			endpoint:  "rejudge",
			wantState: inspection.StatePendingIndependentReview,
			body: func(op string, gen int64, main, foreign lockedIDs) string {
				return fmt.Sprintf(`{"operation":%q,"generation":%d,"blind_code":%q,"slide":%q,"well":%q,"reason":"foreign binding","reviewer":"carol"}`,
					op, gen, main.blind, main.slide, foreign.well)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer()
			h := srv.Handler()
			mainLock := lockTask(t, h, "main-lock", mainIDs)
			foreignLock := lockTask(t, h, "foreign-lock", foreignIDs)
			advanceThroughLeases(t, h, foreignLock, foreignIDs, "foreign")
			advanceToDNA(t, h, mainLock, mainIDs, "main")

			switch tc.stage {
			case "chemistry":
				submitGoodDNA(t, h, mainLock, mainIDs, "main-good-dna")
			case "rejudge":
				submitGoodDNA(t, h, mainLock, mainIDs, "main-good-dna")
				submitGoodChemistry(t, h, mainLock, mainIDs, "main-good-chemistry")
			}

			beforeEvidence := evidenceLen(t, srv, mainLock)
			beforeState := currentState(t, srv, mainLock.TaskID)
			if beforeState != tc.wantState {
				t.Fatalf("setup state = %s, want %s", beforeState, tc.wantState)
			}

			rec := doJSON(t, h, http.MethodPost, "/api/inspections/"+mainLock.TaskID+"/"+tc.endpoint,
				tc.body("bad-"+tc.name, mainLock.Generation, mainIDs, foreignIDs))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeBadRequest) {
				t.Fatalf("foreign binding response = %d %s, want %d with %s",
					rec.Code, rec.Body.String(), http.StatusBadRequest, CodeBadRequest)
			}
			if got := currentState(t, srv, mainLock.TaskID); got != tc.wantState {
				t.Fatalf("state changed after rejection: got %s, want %s", got, tc.wantState)
			}
			if got := evidenceLen(t, srv, mainLock); got != beforeEvidence {
				t.Fatalf("evidence count changed after rejection: got %d, want %d", got, beforeEvidence)
			}

			switch tc.stage {
			case "dna":
				submitGoodDNA(t, h, mainLock, mainIDs, "after-bad-good-dna")
				submitGoodChemistry(t, h, mainLock, mainIDs, "after-bad-good-chemistry")
			case "chemistry":
				submitGoodChemistry(t, h, mainLock, mainIDs, "after-bad-good-chemistry")
			}
			finishCleanly(t, h, srv, mainLock)
		})
	}
}
