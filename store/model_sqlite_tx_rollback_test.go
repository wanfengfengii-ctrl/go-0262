package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/api"
	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
	"nectargate/raw-honey-maturation-intake/service"
	"nectargate/raw-honey-maturation-intake/store"
)

func TestModel_SQLiteWithTxRollsBackFailedBusinessWrites(t *testing.T) {
	ctx := context.Background()
	forcedErr := errors.New("forced business error")

	type claimConflictCase struct {
		name          string
		blockerTank   string
		blockerWell   string
		blockerSlides []string
		failedTank    string
		failedWell    string
		failedSlides  []string
		thirdTank     string
		thirdWell     string
		thirdSlides   []string
	}

	tests := []struct {
		name string
		run  func(t *testing.T, dbPath string)
	}{
		{
			name: "api_conflict_claim_leaves_no_lease_and_next_batch_can_claim",
			run: func(t *testing.T, dbPath string) {
				cases := []claimConflictCase{
					{
						name:          "well_conflict_after_tank_slot_write",
						blockerTank:   "TS-holder-well",
						blockerWell:   "W-blocked",
						blockerSlides: []string{"SL-holder-well"},
						failedTank:    "TS-candidate-well",
						failedWell:    "W-blocked",
						failedSlides:  []string{"SL-candidate-well"},
						thirdTank:     "TS-candidate-well",
						thirdWell:     "W-third-well",
						thirdSlides:   []string{"SL-third-well"},
					},
					{
						name:          "slide_conflict_after_tank_slot_and_well_writes",
						blockerTank:   "TS-holder-slide",
						blockerWell:   "W-holder-slide",
						blockerSlides: []string{"SL-blocked"},
						failedTank:    "TS-candidate-slide",
						failedWell:    "W-candidate-slide",
						failedSlides:  []string{"SL-blocked"},
						thirdTank:     "TS-candidate-slide",
						thirdWell:     "W-candidate-slide",
						thirdSlides:   []string{"SL-third-slide"},
					},
				}

				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "nectargate.db"))
						if err != nil {
							t.Fatalf("OpenSQLite: %v", err)
						}
						t.Cleanup(func() {
							if err := st.Close(); err != nil {
								t.Fatalf("Close: %v", err)
							}
						})

						svc := service.New(st, catalog.Seed(time.Now()), nil, nil)
						handler := api.NewServer(svc).Handler()
						people := []catalog.PersonnelID{catalog.PersonSamplerA, catalog.PersonSamplerB}
						reviewers := []catalog.PersonnelID{catalog.PersonReviewerA, catalog.PersonReviewerB}

						lockAndSeal := func(label, tank, well string, slides []string) (inspection.TaskID, inspection.Generation) {
							t.Helper()
							lock := service.LockInput{
								Operation:   inspection.OperationID("lock-" + tc.name + "-" + label),
								Farm:        "farm-01",
								Season:      "spring-2026",
								BatchID:     "batch-" + label,
								Barrel:      "barrel-" + tc.name + "-" + label,
								Seal:        "seal-" + tc.name + "-" + label,
								Zone:        "4C",
								BlindCode:   "blind-" + tc.name + "-" + label,
								Slides:      slides,
								Well:        well,
								TankSlot:    tank,
								Samplers:    people,
								Reviewers:   reviewers,
								RuleVersion: 1,
							}
							res, err := svc.Lock(ctx, lock)
							if err != nil {
								t.Fatalf("Lock %s: %v", label, err)
							}
							if err := svc.ConfirmSampling(ctx, service.ConfirmSamplingInput{
								Operation:  inspection.OperationID("confirm-" + tc.name + "-" + label),
								TaskID:     res.TaskID,
								Generation: res.Generation,
								Samplers:   people,
								Barrel:     lock.Barrel,
								Seal:       lock.Seal,
							}); err != nil {
								t.Fatalf("ConfirmSampling %s: %v", label, err)
							}
							if err := svc.SealSamples(ctx, service.SealSamplesInput{
								Operation:   inspection.OperationID("seal-" + tc.name + "-" + label),
								TaskID:      res.TaskID,
								Generation:  res.Generation,
								BlindCode:   ledger.BlindCode(lock.BlindCode),
								Triplicates: []string{"tri-" + label + "-1", "tri-" + label + "-2", "tri-" + label + "-3"},
								SealedBy:    catalog.PersonSamplerA,
							}); err != nil {
								t.Fatalf("SealSamples %s: %v", label, err)
							}
							return res.TaskID, res.Generation
						}

						claim := func(label string, id inspection.TaskID, gen inspection.Generation, tank, well string, slides []string) error {
							t.Helper()
							return svc.ClaimLeases(ctx, service.ClaimLeasesInput{
								Operation:  inspection.OperationID("claim-" + tc.name + "-" + label),
								TaskID:     id,
								Generation: gen,
								TankSlot:   tank,
								Well:       well,
								Slides:     slides,
							})
						}

						blockerID, blockerGen := lockAndSeal("blocker", tc.blockerTank, tc.blockerWell, tc.blockerSlides)
						if err := claim("blocker", blockerID, blockerGen, tc.blockerTank, tc.blockerWell, tc.blockerSlides); err != nil {
							t.Fatalf("claim blocker: %v", err)
						}

						failedID, failedGen := lockAndSeal("failed", tc.failedTank, tc.failedWell, tc.failedSlides)
						failedOperation := inspection.OperationID("claim-" + tc.name + "-failed")
						body, err := json.Marshal(map[string]any{
							"operation":  string(failedOperation),
							"generation": int64(failedGen),
							"tank_slot":  tc.failedTank,
							"well":       tc.failedWell,
							"slides":     tc.failedSlides,
						})
						if err != nil {
							t.Fatalf("marshal claim body: %v", err)
						}
						req := httptest.NewRequest(http.MethodPost, "/api/inspections/"+string(failedID)+"/leases/claim", bytes.NewReader(body))
						rec := httptest.NewRecorder()
						handler.ServeHTTP(rec, req)
						if rec.Code != http.StatusConflict {
							t.Fatalf("failed claim status = %d, body %s; want %d", rec.Code, rec.Body.String(), http.StatusConflict)
						}
						var apiErr struct {
							Code string `json:"code"`
						}
						if err := json.Unmarshal(rec.Body.Bytes(), &apiErr); err != nil {
							t.Fatalf("decode conflict body: %v", err)
						}
						if apiErr.Code != api.CodeConflict {
							t.Fatalf("conflict code = %q, want %q", apiErr.Code, api.CodeConflict)
						}

						leases, err := st.LoadLeases(ctx, failedID)
						if err != nil {
							t.Fatalf("LoadLeases failed task: %v", err)
						}
						if len(leases) != 0 {
							t.Fatalf("failed claim leaked leases: %+v", leases)
						}
						if _, err := st.LoadOperation(ctx, failedOperation); !errors.Is(err, store.ErrNotFound) {
							t.Fatalf("failed claim operation lookup = %v, want ErrNotFound", err)
						}
						failedTask, err := st.LoadTask(ctx, failedID)
						if err != nil {
							t.Fatalf("LoadTask failed task: %v", err)
						}
						if failedTask.State != inspection.StateClaimingResources {
							t.Fatalf("failed task state = %s, want %s", failedTask.State, inspection.StateClaimingResources)
						}

						thirdID, thirdGen := lockAndSeal("third", tc.thirdTank, tc.thirdWell, tc.thirdSlides)
						if err := claim("third", thirdID, thirdGen, tc.thirdTank, tc.thirdWell, tc.thirdSlides); err != nil {
							t.Fatalf("third claim of failed resources: %v", err)
						}
					})
				}
			},
		},
		{
			name: "withtx_error_rolls_back_lease_operation_and_state",
			run: func(t *testing.T, dbPath string) {
				st, err := store.OpenSQLite(dbPath)
				if err != nil {
					t.Fatalf("OpenSQLite: %v", err)
				}
				t.Cleanup(func() {
					if err := st.Close(); err != nil {
						t.Fatalf("Close: %v", err)
					}
				})

				var task inspection.InspectionTask
				if err := st.WithTx(ctx, func(tx store.Tx) error {
					var err error
					task, err = tx.CreateTask(ctx, inspection.LockRequest{}, inspection.InspectionTask{
						State:  inspection.StateClaimingResources,
						Barrel: "rollback-barrel",
						Seal:   "rollback-seal",
					})
					return err
				}); err != nil {
					t.Fatalf("seed task: %v", err)
				}

				operation := inspection.OperationID("rollback-operation")
				err = st.WithTx(ctx, func(tx store.Tx) error {
					if err := tx.SaveLease(ctx, ledger.ResourceLease{
						ResourceType: ledger.ResourceTankSlot,
						ResourceID:   "TS-rollback",
						TaskID:       task.ID,
						Generation:   task.Generation,
						Status:       ledger.LeaseActive,
					}); err != nil {
						return err
					}
					if err := tx.SaveOperation(ctx, store.OperationRecord{
						Operation:   operation,
						ContentHash: "hash",
						TaskID:      task.ID,
					}); err != nil {
						return err
					}
					if err := tx.UpdateTaskState(ctx, task.ID, task.Generation, inspection.StateClaimingResources, inspection.StateCountingPollen); err != nil {
						return err
					}
					return forcedErr
				})
				if !errors.Is(err, forcedErr) {
					t.Fatalf("WithTx error = %v, want %v", err, forcedErr)
				}

				leases, err := st.LoadLeases(ctx, task.ID)
				if err != nil {
					t.Fatalf("LoadLeases: %v", err)
				}
				if len(leases) != 0 {
					t.Fatalf("rolled-back tx leaked leases: %+v", leases)
				}
				if _, err := st.LoadOperation(ctx, operation); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("rolled-back operation lookup = %v, want ErrNotFound", err)
				}
				loaded, err := st.LoadTask(ctx, task.ID)
				if err != nil {
					t.Fatalf("LoadTask: %v", err)
				}
				if loaded.State != inspection.StateClaimingResources {
					t.Fatalf("rolled-back state = %s, want %s", loaded.State, inspection.StateClaimingResources)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, filepath.Join(t.TempDir(), "nectargate.db"))
		})
	}
}
