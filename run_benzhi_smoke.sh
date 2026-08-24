#!/usr/bin/env bash
# Smoke test for the NectarGate intake backend.
#
# This script builds the server, starts it on a local ephemeral port with a
# throwaway SQLite database, probes the health endpoint, exercises the public
# lock API, and then cleans up every process and temporary file. It performs no
# external network access and never calls `go test`.
set -euo pipefail

# --- fail-fast helpers -------------------------------------------------------
PORT="${NECTARGATE_SMOKE_PORT:-18080}"
WORKDIR="$(mktemp -d ./nectargate-smoke.XXXXXX)"
SERVER_PID=""
cleanup() {
  if [[ -n "${SERVER_PID}" ]] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -rf "${WORKDIR}"
}
trap cleanup EXIT

log() { printf '[smoke] %s\n' "$*"; }

# --- build -------------------------------------------------------------------
log "building server binary"
BIN="${WORKDIR}/nectargate"
go build -o "${BIN}" ./cmd/nectargate

# --- start -------------------------------------------------------------------
DB="${WORKDIR}/smoke.db"
log "starting server on :${PORT}"
NECTARGATE_ADDR=":${PORT}" NECTARGATE_DB="${DB}" "${BIN}" &
SERVER_PID=$!

# --- wait for readiness ------------------------------------------------------
log "waiting for health endpoint"
ready=0
for _ in $(seq 1 100); do
  if resp="$(curl -s --max-time 1 "http://127.0.0.1:${PORT}/healthz" 2>/dev/null)"; then
    if [[ "${resp}" == *'"status":"ok"'* ]]; then
      ready=1
      break
    fi
  fi
  sleep 0.1
done
if [[ "${ready}" -ne 1 ]]; then
  log "server did not become ready" >&2
  exit 1
fi
log "server is healthy"

# --- exercise the public lock API -------------------------------------------
LOCK_BODY='{"operation":"smoke-1","farm":"farm-01","season":"spring-2026","batch_id":"batch-01","barrel":"B-SMOKE","seal":"S-SMOKE","zone":"4C","blind_code":"BC-SMOKE","slides":["SL-1"],"well":"W-SMOKE","tank_slot":"TS-SMOKE","samplers":["alice","bob"],"reviewers":["carol","dave"],"rule_version":1}'

log "POST /api/inspections/lock"
lock_resp="$(curl -sS --max-time 5 -X POST "http://127.0.0.1:${PORT}/api/inspections/lock" \
  -H 'Content-Type: application/json' -d "${LOCK_BODY}")"

# Assert the captured response (never pipe curl into grep, which can close the
# pipe early and make curl fail with SIGPIPE).
if [[ "${lock_resp}" != *'"task_id"'* ]]; then
  log "lock response missing task_id: ${lock_resp}" >&2
  exit 1
fi
if [[ "${lock_resp}" != *'"pending_sampling_confirm"'* ]]; then
  log "lock response unexpected state: ${lock_resp}" >&2
  exit 1
fi

# Extract the task id and confirm the task is retrievable via the public API.
task_id="$(printf '%s' "${lock_resp}" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')"
if [[ -z "${task_id}" ]]; then
  log "could not parse task_id from ${lock_resp}" >&2
  exit 1
fi
log "locked task ${task_id}"

get_resp="$(curl -sS --max-time 5 "http://127.0.0.1:${PORT}/api/inspections/${task_id}")"
if [[ "${get_resp}" != *"${task_id}"* ]]; then
  log "task lookup failed: ${get_resp}" >&2
  exit 1
fi

log "smoke test passed (task ${task_id} locked and readable)"
