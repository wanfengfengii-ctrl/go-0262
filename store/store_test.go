package store

import (
	"context"
	"errors"
	"testing"

	"nectargate/raw-honey-maturation-intake/inspection"
)

func createTask(t *testing.T, st Store, task inspection.InspectionTask) inspection.InspectionTask {
	t.Helper()
	var out inspection.InspectionTask
	err := st.WithTx(context.Background(), func(tx Tx) error {
		var err error
		out, err = tx.CreateTask(context.Background(), inspection.LockRequest{}, task)
		return err
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return out
}

func TestMemoryCreateAndLoad(t *testing.T) {
	m := NewMemory()
	created := createTask(t, m, inspection.InspectionTask{State: inspection.StatePendingLock})
	if created.ID == "" || created.Generation != 1 {
		t.Fatalf("unexpected created: %+v", created)
	}
	loaded, err := m.LoadTask(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if loaded.ID != created.ID {
		t.Fatalf("loaded %q != created %q", loaded.ID, created.ID)
	}
}

func TestMemoryNotFound(t *testing.T) {
	m := NewMemory()
	if _, err := m.LoadTask(context.Background(), "task_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestMemoryRollbackOnError(t *testing.T) {
	m := NewMemory()
	err := m.WithTx(context.Background(), func(tx Tx) error {
		if _, err := tx.CreateTask(context.Background(), inspection.LockRequest{}, inspection.InspectionTask{
			Barrel: "B-1", State: inspection.StatePendingLock,
		}); err != nil {
			return err
		}
		return errors.New("forced failure")
	})
	if err == nil {
		t.Fatal("expected forced failure")
	}
	// The task written inside the rolled-back transaction must not persist.
	tasks, err := m.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("rollback leaked %d tasks", len(tasks))
	}
}

func TestMemoryDuplicateBarrel(t *testing.T) {
	m := NewMemory()
	createTask(t, m, inspection.InspectionTask{Barrel: "B-1", Seal: "S-1", State: inspection.StatePendingLock})
	err := m.WithTx(context.Background(), func(tx Tx) error {
		_, err := tx.CreateTask(context.Background(), inspection.LockRequest{}, inspection.InspectionTask{
			Barrel: "B-1", Seal: "S-2", State: inspection.StatePendingLock,
		})
		return err
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v, want ErrDuplicate", err)
	}
}

func TestMemoryClose(t *testing.T) {
	m := NewMemory()
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.WithTx(context.Background(), func(tx Tx) error { return nil }); err == nil {
		t.Fatal("expected error after close")
	}
}
