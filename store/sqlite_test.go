package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/ledger"
)

func TestSQLitePersistAndRecover(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nectargate.db")

	s, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	created := createTask(t, s, inspection.InspectionTask{
		Barrel: "B-1", Seal: "S-1", State: inspection.StatePendingSamplingConfirm,
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	loaded, err := s2.LoadTask(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("LoadTask after restart: %v", err)
	}
	if loaded.Barrel != "B-1" || loaded.Generation != created.Generation {
		t.Fatalf("task not recovered: %+v", loaded)
	}
}

func TestSQLiteDuplicateBarrel(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "nectargate.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()

	createTask(t, s, inspection.InspectionTask{Barrel: "B-1", Seal: "S-1", State: inspection.StatePendingLock})
	err = s.WithTx(context.Background(), func(tx Tx) error {
		_, err := tx.CreateTask(context.Background(), inspection.LockRequest{}, inspection.InspectionTask{
			Barrel: "B-1", Seal: "S-2", State: inspection.StatePendingLock,
		})
		return err
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v, want ErrDuplicate", err)
	}
}

func TestSQLiteRecoverReleasesOrphanLease(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nectargate.db")

	s, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	created := createTask(t, s, inspection.InspectionTask{Barrel: "B-1", Seal: "S-1", State: inspection.StatePendingLock})
	// Claim a lease, then force the task terminal without releasing the lease.
	if err := s.WithTx(context.Background(), func(tx Tx) error {
		if err := tx.SaveLease(context.Background(), ledger.ResourceLease{
			ResourceType: ledger.ResourceWell, ResourceID: "W-1", TaskID: created.ID,
			Generation: created.Generation, Status: ledger.LeaseActive,
		}); err != nil {
			return err
		}
		return tx.UpdateTaskState(context.Background(), created.ID, created.Generation, inspection.StatePendingLock, inspection.StateCancelled)
	}); err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	s.Close()

	// On restart, the orphan active lease must be released.
	s2, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	leases, err := s2.LoadLeases(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("LoadLeases: %v", err)
	}
	for _, l := range leases {
		if l.ResourceID == "W-1" && l.Status != ledger.LeaseReleased {
			t.Fatalf("orphan lease not released after recovery: %+v", l)
		}
	}
}

// TestSQLiteFailedTxLeavesNoLease reproduces the reported regression: when a
// multi-write transaction saves one resource lease and then fails on the next
// (e.g. a well already occupied), the partial lease must not persist. With the
// bug, WithTx committed the partial writes, so a later claim of the first
// resource by a different task saw it as still occupied even though no claim
// had succeeded.
func TestSQLiteFailedTxLeavesNoLease(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "nectargate.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()

	created := createTask(t, s, inspection.InspectionTask{Barrel: "B-1", Seal: "S-1", State: inspection.StatePendingLock})

	// A second task pre-holds the well so the upcoming transaction conflicts
	// on its second lease write.
	if err := s.WithTx(context.Background(), func(tx Tx) error {
		return tx.SaveLease(context.Background(), ledger.ResourceLease{
			ResourceType: ledger.ResourceWell, ResourceID: "W-TAKEN", TaskID: "task-other",
			Generation: 1, Status: ledger.LeaseActive,
		})
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	// The transaction saves the tank slot, then conflicts on the well. It must
	// roll back entirely, leaving the tank slot free for a different task.
	err = s.WithTx(context.Background(), func(tx Tx) error {
		if err := tx.SaveLease(context.Background(), ledger.ResourceLease{
			ResourceType: ledger.ResourceTankSlot, ResourceID: "TS-1", TaskID: created.ID,
			Generation: created.Generation, Status: ledger.LeaseActive,
		}); err != nil {
			return err
		}
		return tx.SaveLease(context.Background(), ledger.ResourceLease{
			ResourceType: ledger.ResourceWell, ResourceID: "W-TAKEN", TaskID: created.ID,
			Generation: created.Generation, Status: ledger.LeaseActive,
		})
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v, want ErrDuplicate", err)
	}

	// The tank slot must be free: a third task claiming it must succeed. If
	// the failed tx had committed, this claim would see TS-1 still active for
	// the first task and return ErrDuplicate.
	if err := s.WithTx(context.Background(), func(tx Tx) error {
		return tx.SaveLease(context.Background(), ledger.ResourceLease{
			ResourceType: ledger.ResourceTankSlot, ResourceID: "TS-1", TaskID: "task-third",
			Generation: 1, Status: ledger.LeaseActive,
		})
	}); err != nil {
		t.Fatalf("tank slot leaked from failed tx: %v", err)
	}
}
