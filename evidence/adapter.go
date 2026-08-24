package evidence

import (
	"strings"
	"sync"

	"nectargate/raw-honey-maturation-intake/inspection"
)

// ScriptedAdapter is a deterministic instrument adapter used by tests and the
// demo entry point. It returns a fixed raw value and error code on every call
// and counts how many times it was invoked.
type ScriptedAdapter struct {
	mu         sync.Mutex
	instrument InstrumentType
	raw        string
	errCode    string
	calls      int
}

// NewScriptedAdapter returns an adapter that always answers with raw and the
// given error code (empty error code means success).
func NewScriptedAdapter(inst InstrumentType, raw, errCode string) *ScriptedAdapter {
	return &ScriptedAdapter{instrument: inst, raw: raw, errCode: errCode}
}

// Instrument implements InstrumentAdapter.
func (a *ScriptedAdapter) Instrument() InstrumentType { return a.instrument }

// Call implements InstrumentAdapter.
func (a *ScriptedAdapter) Call(_ string) (string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return a.raw, a.errCode
}

// Calls reports how many times the adapter was invoked.
func (a *ScriptedAdapter) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// Classify maps an instrument adapter's non-empty error code to a stable
// attempt result. Unknown codes are treated as malformed responses.
func Classify(errorCode string) AttemptResult {
	switch strings.ToLower(errorCode) {
	case "rejected":
		return AttemptRejected
	case "disconnected":
		return AttemptDisconnected
	case "timeout":
		return AttemptTimeout
	case "":
		return AttemptOK
	default:
		return AttemptMalformed
	}
}

// IsFailure reports whether an attempt result is not a clean success.
func (r AttemptResult) IsFailure() bool {
	return r != AttemptOK
}

// NewAttempt builds an auditable adapter attempt record for a task generation.
// Failures carry a retry count derived from prior attempts for the same call
// key; successes reset the pending retry state.
func NewAttempt(
	inst InstrumentType,
	callKey string,
	taskID inspection.TaskID,
	req string,
	result AttemptResult,
	rawError string,
	at inspection.LogicalTime,
	priorRetries int,
) AdapterAttempt {
	retries := priorRetries
	if result.IsFailure() {
		retries++
	}
	return AdapterAttempt{
		Instrument: inst,
		CallKey:    callKey,
		TaskID:     taskID,
		Request:    req,
		Result:     result,
		RetryCount: retries,
		At:         at,
		RawError:   rawError,
	}
}

// PendingRetries returns the highest retry count recorded for a call key, used
// to compute the next retry after a process restart.
func PendingRetries(attempts []AdapterAttempt, callKey string) int {
	max := 0
	for _, a := range attempts {
		if a.CallKey == callKey && a.RetryCount > max {
			max = a.RetryCount
		}
	}
	return max
}
