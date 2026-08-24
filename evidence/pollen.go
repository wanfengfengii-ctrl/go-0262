package evidence

import (
	"errors"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/inspection"
)

// ErrNegativeCount is returned when a pollen count is negative.
var ErrNegativeCount = errors.New("evidence: pollen count must not be negative")

// PollenCountInput is a single submitted pollen count before it becomes a
// persisted cover cell.
type PollenCountInput struct {
	Slide string
	Class catalog.PollenClass
	Count int
}

// ValidatePollenCoverage enforces the pollen-count domain rules:
//
//   - every count is a non-negative integer,
//   - every class comes from the locked classification set,
//   - the sum of all counts equals the declared total particle count,
//   - the coverage grid covers every locked slide for every locked class,
//   - the total target-pollen count meets the locked pollen threshold.
//
// It returns a descriptive error on the first violation; valid input produces
// a nil error so callers can persist the cover cells.
func ValidatePollenCoverage(
	rule *catalog.FarmSourceRule,
	lockedSlides []string,
	declaredTotal int,
	minTarget int,
	inputs []PollenCountInput,
) error {
	if declaredTotal < 0 {
		return ErrCountNotConserved
	}

	sum := 0
	target := 0
	covered := make(map[string]map[catalog.PollenClass]int, len(lockedSlides))
	for _, in := range inputs {
		if in.Count < 0 {
			return ErrNegativeCount
		}
		if !rule.IsPollenClass(in.Class) {
			return ErrNotLockedClass
		}
		sum += in.Count
		if in.Class == catalog.PollenTarget {
			target += in.Count
		}
		if covered[in.Slide] == nil {
			covered[in.Slide] = make(map[catalog.PollenClass]int)
		}
		covered[in.Slide][in.Class] += in.Count
	}

	if sum != declaredTotal {
		return ErrCountNotConserved
	}

	// Coverage: every locked slide must carry every locked class.
	for _, slide := range lockedSlides {
		cell := covered[slide]
		if cell == nil {
			return ErrCoverIncomplete
		}
		for _, class := range rule.PollenClasses {
			if _, ok := cell[class]; !ok {
				return ErrCoverIncomplete
			}
		}
	}

	if target < minTarget {
		return ErrPollenBelowThreshold
	}
	return nil
}

// ErrPollenBelowThreshold reports that the target-pollen count did not meet the
// locked pollen-spectrum threshold.
var ErrPollenBelowThreshold = errors.New("evidence: target pollen below threshold")

// BuildCoverCells turns validated inputs into persisted cover cells for a task
// generation, assigning the cover version and the recording personnel.
func BuildCoverCells(
	taskID inspection.TaskID,
	gen inspection.Generation,
	enteredBy catalog.PersonnelID,
	coverVersion int,
	inputs []PollenCountInput,
) []PollenCoverCell {
	cells := make([]PollenCoverCell, 0, len(inputs))
	for _, in := range inputs {
		cells = append(cells, PollenCoverCell{
			TaskID:       taskID,
			Generation:   gen,
			Slide:        in.Slide,
			Class:        in.Class,
			Count:        in.Count,
			CoverVersion: coverVersion,
			EnteredBy:    enteredBy,
			Valid:        true,
		})
	}
	return cells
}
