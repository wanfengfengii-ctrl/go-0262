package ledger

import "nectargate/raw-honey-maturation-intake/inspection"

// Active returns only the leases that are still held (not yet released). It is
// used by the claim path to decide occupancy without trusting stale rows.
func Active(leases []ResourceLease) []ResourceLease {
	out := make([]ResourceLease, 0, len(leases))
	for _, l := range leases {
		if l.Status == LeaseActive {
			out = append(out, l)
		}
	}
	return out
}

// OccupiedByOther reports whether a resource is already actively leased by a
// different task. The same task may re-claim a resource it already holds.
func OccupiedByOther(leases []ResourceLease, typ ResourceType, id string, task inspection.TaskID) bool {
	for _, l := range leases {
		if l.Status != LeaseActive {
			continue
		}
		if l.ResourceType == typ && l.ResourceID == id && l.TaskID != task {
			return true
		}
	}
	return false
}

// FindActive locates the active lease of a resource type and id, or nil.
func FindActive(leases []ResourceLease, typ ResourceType, id string) *ResourceLease {
	for i := range leases {
		l := &leases[i]
		if l.Status == LeaseActive && l.ResourceType == typ && l.ResourceID == id {
			return l
		}
	}
	return nil
}

// Release marks every active lease of a task as released at the given logical
// time. Released leases no longer occupy their resources.
func ReleaseAll(leases []ResourceLease, task inspection.TaskID, at inspection.LogicalTime) []ResourceLease {
	out := make([]ResourceLease, 0, len(leases))
	for _, l := range leases {
		if l.Status == LeaseActive && l.TaskID == task {
			l.Status = LeaseReleased
			l.ReleasedAt = at
		}
		out = append(out, l)
	}
	return out
}
