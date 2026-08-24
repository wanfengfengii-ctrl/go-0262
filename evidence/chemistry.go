package evidence

import (
	"errors"

	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/fixed"
)

// Fixed scales shared by the physical-reading domain rules.
const (
	ScaleCt           = 2 // DNA Ct
	ScaleHMF          = 1
	ScaleAmylase      = 1
	ScaleMoisture     = 1
	ScaleConductivity = 2
	ScaleAcidity      = 1
)

// ChemistryReading is a set of fixed-decimal physical readings (理化读数).
type ChemistryReading struct {
	HMF          fixed.Decimal
	Amylase      fixed.Decimal
	Moisture     fixed.Decimal
	Conductivity fixed.Decimal
	Acidity      fixed.Decimal
}

// ChemistryViolation describes a single out-of-threshold metric.
type ChemistryViolation struct {
	Metric string
	Reason string
}

// ValidateChemistry compares every metric against the locked chemistry
// thresholds and returns the list of violations (empty when all pass). It also
// verifies the fixed-decimal scale of each reading so arithmetic stays exact.
func ValidateChemistry(r ChemistryReading, th catalog.ChemistryThreshold) ([]ChemistryViolation, error) {
	if err := checkScales(r); err != nil {
		return nil, err
	}
	var out []ChemistryViolation
	if r.HMF.Compare(th.MaxHMF) > 0 {
		out = append(out, ChemistryViolation{Metric: "hmf", Reason: "exceeds maximum"})
	}
	if r.Amylase.Compare(th.MinAmylase) < 0 {
		out = append(out, ChemistryViolation{Metric: "amylase", Reason: "below minimum"})
	}
	if r.Moisture.Compare(th.MaxMoisture) > 0 {
		out = append(out, ChemistryViolation{Metric: "moisture", Reason: "exceeds maximum"})
	}
	if r.Conductivity.Compare(th.MaxConductivity) > 0 {
		out = append(out, ChemistryViolation{Metric: "conductivity", Reason: "exceeds maximum"})
	}
	if r.Acidity.Compare(th.MaxAcidity) > 0 {
		out = append(out, ChemistryViolation{Metric: "acidity", Reason: "exceeds maximum"})
	}
	return out, nil
}

func checkScales(r ChemistryReading) error {
	for _, p := range []struct {
		name  string
		scale int
		d     fixed.Decimal
	}{
		{"hmf", ScaleHMF, r.HMF},
		{"amylase", ScaleAmylase, r.Amylase},
		{"moisture", ScaleMoisture, r.Moisture},
		{"conductivity", ScaleConductivity, r.Conductivity},
		{"acidity", ScaleAcidity, r.Acidity},
	} {
		if p.d.Scale != p.scale {
			return errors.New("evidence: " + p.name + " has wrong fixed-decimal scale")
		}
	}
	return nil
}

// ErrScaleMismatchReading reports a reading whose fixed-decimal scale does not
// match the domain rule.
var ErrScaleMismatchReading = errors.New("evidence: reading scale mismatch")

// ValidateDNA compares a qPCR Ct reading against the locked DNA threshold. Ct
// at or below MaxCt is acceptable; anything higher indicates fingerprint
// mismatch and drives a rejudge.
func ValidateDNA(ct fixed.Decimal, th catalog.DNAThreshold) error {
	if ct.Scale != ScaleCt {
		return ErrScaleMismatchReading
	}
	if ct.Compare(th.MaxCt) > 0 {
		return ErrDNACtAboveThreshold
	}
	return nil
}

// ErrDNACtAboveThreshold reports a Ct value above the locked maximum.
var ErrDNACtAboveThreshold = errors.New("evidence: dna ct above threshold")
