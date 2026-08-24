package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/fixed"
	"nectargate/raw-honey-maturation-intake/inspection"
	"nectargate/raw-honey-maturation-intake/store"
)

func TestLockFarmSeasonMatch(t *testing.T) {
	e := newEnv(t, nil)
	id, gen := e.lock(t, defaultLockInput("B-001", "S-001"))
	if id == "" || gen != 1 {
		t.Fatalf("got id=%q gen=%d", id, gen)
	}
	if e.currentState(t, id) != inspection.StatePendingSamplingConfirm {
		t.Fatalf("unexpected state %s", e.currentState(t, id))
	}
}

func TestLockFarmMismatch(t *testing.T) {
	e := newEnv(t, nil)
	in := defaultLockInput("B-002", "S-002")
	in.Farm = "farm-99"
	if _, err := e.svc.Lock(context.Background(), in); !errors.Is(err, catalog.ErrFarmMismatch) {
		t.Fatalf("got %v, want ErrFarmMismatch", err)
	}
}

func TestLockSeasonMismatch(t *testing.T) {
	e := newEnv(t, nil)
	in := defaultLockInput("B-003", "S-003")
	in.Season = "autumn-2026"
	if _, err := e.svc.Lock(context.Background(), in); !errors.Is(err, catalog.ErrSeasonMismatch) {
		t.Fatalf("got %v, want ErrSeasonMismatch", err)
	}
}

func TestLockZoneBoundary(t *testing.T) {
	e := newEnv(t, nil)
	for _, z := range []string{"4C", "6C"} {
		in := defaultLockInput("B-"+z, "S-"+z)
		in.Zone = catalog.TemporaryZone(z)
		if _, err := e.svc.Lock(context.Background(), in); err != nil {
			t.Fatalf("zone %s should pass: %v", z, err)
		}
	}
	in := defaultLockInput("B-8C", "S-8C")
	in.Zone = "8C"
	if _, err := e.svc.Lock(context.Background(), in); !errors.Is(err, catalog.ErrZoneOutOfRange) {
		t.Fatalf("got %v, want ErrZoneOutOfRange", err)
	}
}

func TestLockSealDuplicate(t *testing.T) {
	e := newEnv(t, nil)
	e.lock(t, defaultLockInput("B-010", "S-010"))
	in := defaultLockInput("B-011", "S-010") // same seal, different barrel
	if _, err := e.svc.Lock(context.Background(), in); !errors.Is(err, ErrResourceOccupied) {
		t.Fatalf("got %v, want ErrResourceOccupied", err)
	}
}

func TestLockStaleSummary(t *testing.T) {
	st := store.NewMemory()
	cat := catalog.NewCatalog()
	cat.Add(&catalog.FarmSourceRule{
		Farm:         "farm-03",
		NectarSeason: "spring-2026",
		Summary: catalog.BatchSummary{
			BatchID:     "batch-03",
			ExtractedAt: time.Now().Add(-72 * time.Hour),
			ValidFor:    48 * time.Hour,
		},
		ZoneRange:     []catalog.TemporaryZone{"4C"},
		Qualified:     map[catalog.PersonnelID]bool{"alice": true, "bob": true, "carol": true, "dave": true},
		PollenClasses: catalog.AllPollenClasses,
		DNA:           catalog.DNAThreshold{MaxCt: fixed.MustParse("32.00", 2)},
		Pollen:        catalog.PollenThreshold{MinTargetCount: 100},
		Chemistry: catalog.ChemistryThreshold{
			MaxHMF: fixed.MustParse("40.0", 1), MinAmylase: fixed.MustParse("8.0", 1),
			MaxMoisture: fixed.MustParse("18.0", 1), MaxConductivity: fixed.MustParse("0.80", 2),
			MaxAcidity: fixed.MustParse("40.0", 1),
		},
		Version: 3,
	})
	svc := New(st, cat, &fakeClock{t: 1}, nil)
	in := defaultLockInput("B-020", "S-020")
	in.Farm = "farm-03"
	in.RuleVersion = 3
	if _, err := svc.Lock(context.Background(), in); !errors.Is(err, catalog.ErrStaleSummary) {
		t.Fatalf("got %v, want ErrStaleSummary", err)
	}
}
