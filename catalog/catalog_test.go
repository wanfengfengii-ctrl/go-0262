package catalog

import (
	"errors"
	"testing"
	"time"

	"nectargate/raw-honey-maturation-intake/fixed"
)

func newRule() *FarmSourceRule {
	now := time.Now()
	return &FarmSourceRule{
		Farm:         "farm-01",
		NectarSeason: "spring-2026",
		Summary: BatchSummary{
			BatchID:     "batch-01",
			ExtractedAt: now,
			ValidFor:    48 * time.Hour,
		},
		ZoneRange:     []TemporaryZone{"4C", "6C"},
		Qualified:     map[PersonnelID]bool{"alice": true, "bob": true},
		PollenClasses: AllPollenClasses,
		DNA:           DNAThreshold{MaxCt: fixed.MustParse("32.00", 2)},
		Chemistry: ChemistryThreshold{
			MaxHMF: fixed.MustParse("40.0", 1),
		},
		Version: 1,
	}
}

func TestValidateLockSeasonMismatch(t *testing.T) {
	r := newRule()
	err := r.ValidateLock("autumn-2026", "4C", time.Now())
	if !errors.Is(err, ErrSeasonMismatch) {
		t.Fatalf("got %v, want ErrSeasonMismatch", err)
	}
}

func TestValidateLockStaleSummary(t *testing.T) {
	r := newRule()
	later := r.Summary.ExtractedAt.Add(72 * time.Hour)
	if err := r.ValidateLock("spring-2026", "4C", later); !errors.Is(err, ErrStaleSummary) {
		t.Fatalf("got %v, want ErrStaleSummary", err)
	}
}

func TestValidateLockZoneBoundary(t *testing.T) {
	r := newRule()
	if err := r.ValidateLock("spring-2026", "4C", time.Now()); err != nil {
		t.Fatalf("lower boundary rejected: %v", err)
	}
	if err := r.ValidateLock("spring-2026", "6C", time.Now()); err != nil {
		t.Fatalf("upper boundary rejected: %v", err)
	}
	if err := r.ValidateLock("spring-2026", "8C", time.Now()); !errors.Is(err, ErrZoneOutOfRange) {
		t.Fatalf("got %v, want ErrZoneOutOfRange", err)
	}
}

func TestValidatePersonnel(t *testing.T) {
	r := newRule()
	if err := r.ValidatePersonnel("alice", "bob"); err != nil {
		t.Fatalf("qualified personnel rejected: %v", err)
	}
	if err := r.ValidatePersonnel("mallory"); !errors.Is(err, ErrUnqualified) {
		t.Fatalf("got %v, want ErrUnqualified", err)
	}
}

func TestPollenClassLocked(t *testing.T) {
	r := newRule()
	if !r.IsPollenClass(PollenTarget) {
		t.Fatal("target class should be locked")
	}
	if r.IsPollenClass(PollenClass("alien")) {
		t.Fatal("unknown class must not be locked")
	}
}
