package evidence

import "nectargate/raw-honey-maturation-intake/inspection"

// NextVersion returns the next immutable version number for an evidence chain
// of a given type within one task generation. Versions are never overwritten;
// each append produces a strictly increasing, non-reusable number.
func NextVersion(chain []EvidenceVersion, taskID inspection.TaskID, gen inspection.Generation, typ EvidenceType) int {
	max := 0
	for _, e := range chain {
		if e.TaskID == taskID && e.Generation == gen && e.Type == typ && e.Version > max {
			max = e.Version
		}
	}
	return max + 1
}

// CurrentOnly filters an evidence chain to the current generation of a task,
// so late readings from an older generation are retained but never influence
// the current arbitration.
func CurrentOnly(chain []EvidenceVersion, gen inspection.Generation) []EvidenceVersion {
	out := make([]EvidenceVersion, 0, len(chain))
	for _, e := range chain {
		if e.Generation == gen {
			out = append(out, e)
		}
	}
	return out
}

// HasRejudgeForGeneration reports whether a rejudge evidence already exists
// for the current generation, enforcing the single-rejudge-per-generation rule.
func HasRejudgeForGeneration(chain []EvidenceVersion, gen inspection.Generation) bool {
	for _, e := range chain {
		if e.Generation == gen && e.Type == EvidenceRejudge {
			return true
		}
	}
	return false
}

// Conclusive returns the effective DNA and chemistry conclusions from the
// current-generation evidence chain. It scans the highest version of each
// reading type; an out-of-threshold conclusion marks the chain anomalous.
func Conclusive(chain []EvidenceVersion, gen inspection.Generation) (anomalous bool, reasons []string) {
	for _, e := range chain {
		if e.Generation != gen {
			continue
		}
		switch e.Type {
		case EvidenceDNA, EvidenceChemistry, EvidenceRejudge:
			if e.Conclusion == "fail" || e.Conclusion == "anomaly" {
				anomalous = true
				reasons = append(reasons, string(e.Type)+":"+e.Conclusion)
			}
		}
	}
	return anomalous, reasons
}
