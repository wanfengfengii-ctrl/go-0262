package catalog

import (
	"time"

	"nectargate/raw-honey-maturation-intake/fixed"
)

// Qualified personnel shared across the fictional farms.
const (
	PersonSamplerA  PersonnelID = "alice"
	PersonSamplerB  PersonnelID = "bob"
	PersonReviewerA PersonnelID = "carol"
	PersonReviewerB PersonnelID = "dave"
)

// Seed builds the fictional farm catalog used by the executable and tests. Two
// farms with distinct nectar seasons and storage zones exercise farm/season
// mismatch and zone-boundary rejection; two additional staff act as reviewers
// so the sampler/reviewer separation rule is enforceable.
func Seed(now time.Time) *Catalog {
	c := NewCatalog()

	qualified := map[PersonnelID]bool{
		PersonSamplerA:  true,
		PersonSamplerB:  true,
		PersonReviewerA: true,
		PersonReviewerB: true,
	}

	chem := ChemistryThreshold{
		MaxHMF:          fixed.MustParse("40.0", 1),
		MinAmylase:      fixed.MustParse("8.0", 1),
		MaxMoisture:     fixed.MustParse("18.0", 1),
		MaxConductivity: fixed.MustParse("0.80", 2),
		MaxAcidity:      fixed.MustParse("40.0", 1),
	}

	c.Add(&FarmSourceRule{
		Farm:         "farm-01",
		NectarSeason: "spring-2026",
		Summary: BatchSummary{
			BatchID:     "batch-01",
			ExtractedAt: now,
			ValidFor:    48 * time.Hour,
		},
		ZoneRange:     []TemporaryZone{"4C", "6C"},
		Qualified:     qualified,
		PollenClasses: AllPollenClasses,
		DNA:           DNAThreshold{MaxCt: fixed.MustParse("32.00", 2)},
		Pollen:        PollenThreshold{MinTargetCount: 100},
		Chemistry:     chem,
		Version:       1,
	})

	c.Add(&FarmSourceRule{
		Farm:         "farm-02",
		NectarSeason: "summer-2026",
		Summary: BatchSummary{
			BatchID:     "batch-02",
			ExtractedAt: now,
			ValidFor:    72 * time.Hour,
		},
		ZoneRange:     []TemporaryZone{"4C"},
		Qualified:     qualified,
		PollenClasses: AllPollenClasses,
		DNA:           DNAThreshold{MaxCt: fixed.MustParse("30.00", 2)},
		Pollen:        PollenThreshold{MinTargetCount: 150},
		Chemistry:     chem,
		Version:       2,
	})

	return c
}
