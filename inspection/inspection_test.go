package inspection

import "testing"

func TestTerminalStates(t *testing.T) {
	for _, s := range []State{StateMatured, StateQuarantined, StateCancelled} {
		if !s.IsTerminal() {
			t.Fatalf("%s should be terminal", s)
		}
		if len(s.NextStates()) != 0 {
			t.Fatalf("%s should have no next states", s)
		}
	}
	if StatePendingLock.IsTerminal() {
		t.Fatal("pending lock must not be terminal")
	}
}

func TestHappyPathTransitions(t *testing.T) {
	path := []State{
		StatePendingLock,
		StatePendingSamplingConfirm,
		StateSealingSamples,
		StateClaimingResources,
		StateCountingPollen,
		StateVerifyingDNA,
		StateRetestingChemistry,
		StatePendingIndependentReview,
		StateReadyForMaturation,
		StateMatured,
	}
	for i := 0; i < len(path)-1; i++ {
		if !path[i].CanTransitionTo(path[i+1]) {
			t.Fatalf("%s -> %s should be allowed", path[i], path[i+1])
		}
	}
}

func TestCancelAllowedFromNonTerminal(t *testing.T) {
	nonTerminal := []State{
		StatePendingLock,
		StatePendingSamplingConfirm,
		StateSealingSamples,
		StateClaimingResources,
		StateCountingPollen,
		StateVerifyingDNA,
		StateRetestingChemistry,
		StatePendingIndependentReview,
		StateReadyForMaturation,
	}
	for _, s := range nonTerminal {
		if !s.CanTransitionTo(StateCancelled) {
			t.Fatalf("%s -> cancelled should be allowed", s)
		}
	}
}

func TestIllegalTransitions(t *testing.T) {
	if StatePendingLock.CanTransitionTo(StateMatured) {
		t.Fatal("pending lock must not jump to matured")
	}
	if StateVerifyingDNA.CanTransitionTo(StatePendingSamplingConfirm) {
		t.Fatal("must not regress to sampling confirm")
	}
	if StateReadyForMaturation.CanTransitionTo(StateCancelled) {
		// cancel is still permitted before a terminal conclusion.
	}
	if StateMatured.CanTransitionTo(StateQuarantined) {
		t.Fatal("terminal matured must not transition")
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, s := range []State{
		StatePendingLock, StateMatured, StateQuarantined, StateCancelled,
	} {
		if s.String() == "" || s.String() == "unknown" {
			t.Fatalf("state %d has no stable name", s)
		}
	}
}
