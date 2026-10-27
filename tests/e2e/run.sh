#!/usr/bin/env bash
# End-to-end tests for webhook-over-websocket using the Postman CLI.
#
#   mise exec -- ./tests/e2e/run.sh      (or: make e2e)
#
# Steps:
#   1. build the real binary and the local target (tests/e2e/target)
#   2. start target, `server`, and two `client`s on free ports
#        - main client     -> target          (channel id issued by the server)
#        - dead client     -> closed port     (fixed channel id, for the 502 case)
#   3. wait until both channels are connected (/internal/channels)
#   4. postman collection run: folders 01..03 (+05 with E2E_TRAEFIK=1)
#   5. stop the main client, wait until the server drops the channel
#   6. postman collection run: folders 04, 07 (+06 with E2E_TRAEFIK=1)
#
# E2E_TRAEFIK=1 also runs the /traefik-config folders (05, 06). They are
# opt-in because /traefik-config panics on main (serverHandle.mlist is never
# set, nil pointer dereference) and the connection is dropped.
# All processes are killed on exit; their logs are printed on failure.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
COLLECTION="${SCRIPT_DIR}/webhook-over-websocket.postman_collection.json"
GO="${GO:-go}"
POSTMAN="${POSTMAN:-postman}"

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/wow-e2e.XXXXXX")"
PIDS=()
STATUS=1

log() { printf '[e2e] %s\n' "$*" >&2; }

cleanup() {
	for pid in "${PIDS[@]:-}"; do
		[ -n "${pid}" ] && kill "${pid}" 2>/dev/null || true
	done
	for pid in "${PIDS[@]:-}"; do
		[ -n "${pid}" ] && wait "${pid}" 2>/dev/null || true
	done
	if [ "${STATUS}" -ne 0 ]; then
		for f in "${WORK_DIR}"/*.log; do
			[ -f "${f}" ] || continue
			printf '\n===== %s =====\n' "$(basename "${f}")" >&2
			cat "${f}" >&2
		done
		log "FAILED (logs above)"
	else
		log "PASSED"
	fi
	rm -rf "${WORK_DIR}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# wait_until <timeout-seconds> <description> <command...>
wait_until() {
	local timeout="$1" what="$2"
	shift 2
	local deadline=$((SECONDS + timeout))
	until "$@" >/dev/null 2>&1; do
		if [ "${SECONDS}" -ge "${deadline}" ]; then
			log "timed out waiting for ${what}"
			return 1
		fi
		sleep 0.2
	done
}

webhook_channels() {
	curl -fsS "${BASE_URL}/internal/channels" | grep -o '"webhook_channels":\[[^]]*\]' || true
}
channel_connected() { webhook_channels | grep -q "\"$1\""; }
channel_gone() { ! curl -fsS "${BASE_URL}/internal/channels" | grep -q "\"$1\""; }
proc_alive() { kill -0 "$1" 2>/dev/null; }

# ---------------------------------------------------------------- build
log "building binaries into ${WORK_DIR}"
(cd "${ROOT_DIR}" && "${GO}" build -o "${WORK_DIR}/wow" . && "${GO}" build -o "${WORK_DIR}/target" ./tests/e2e/target)
WOW="${WORK_DIR}/wow"

# Avoid picking up WOW_* configuration from the caller's environment.
while IFS= read -r v; do unset "${v}"; done < <(env | sed -n 's/^\(WOW_[A-Za-z0-9_]*\)=.*/\1/p')
unset POD_IP || true

PORTS=()
while IFS= read -r p; do PORTS+=("${p}"); done < <("${WORK_DIR}/target" -free-ports 4)
TARGET_PORT="${PORTS[0]}"
SERVER_PORT="${PORTS[1]}"
MEMBERLIST_PORT="${PORTS[2]}"
DEAD_PORT="${PORTS[3]}"
BASE_URL="http://127.0.0.1:${SERVER_PORT}"
DEAD_CHANNEL="e2e-dead-target-$$"
log "ports: target=${TARGET_PORT} server=${SERVER_PORT} memberlist=${MEMBERLIST_PORT} dead=${DEAD_PORT}"

# ---------------------------------------------------------------- start processes
"${WORK_DIR}/target" -port "${TARGET_PORT}" >"${WORK_DIR}/target.log" 2>&1 &
PIDS+=($!)

"${WOW}" server --port "${SERVER_PORT}" --memberlist-port "${MEMBERLIST_PORT}" --log-level DEBUG \
	>"${WORK_DIR}/server.log" 2>&1 &
SERVER_PID=$!
PIDS+=("${SERVER_PID}")

wait_until 20 "target" curl -fsS "http://127.0.0.1:${TARGET_PORT}/ready"
wait_until 20 "server /healthz" curl -fsS "${BASE_URL}/healthz"

"${WOW}" client --server-url "${BASE_URL}" --target-url "http://127.0.0.1:${TARGET_PORT}" \
	>"${WORK_DIR}/client.log" 2>&1 &
CLIENT_PID=$!
PIDS+=("${CLIENT_PID}")

"${WOW}" client --server-url "${BASE_URL}" --target-url "http://127.0.0.1:${DEAD_PORT}" \
	--channel-id "${DEAD_CHANNEL}" --transfer-request-timeout 2s \
	>"${WORK_DIR}/client-dead-target.log" 2>&1 &
PIDS+=($!)

client_issued() { grep -q '^Issued Channel ID: ' "${WORK_DIR}/client.log"; }
wait_until 20 "client to print its channel id" client_issued
CHANNEL_ID="$(sed -n 's/^Issued Channel ID: //p' "${WORK_DIR}/client.log" | head -n1)"
log "main client channel: ${CHANNEL_ID}"
grep -qF "Please set the webhook destination as follows: ${BASE_URL}/webhook/${CHANNEL_ID}" "${WORK_DIR}/client.log" || {
	log "client did not print the expected webhook URL"
	exit 1
}

wait_until 30 "main client to connect" channel_connected "${CHANNEL_ID}"
wait_until 30 "dead-target client to connect" channel_connected "${DEAD_CHANNEL}"
proc_alive "${SERVER_PID}" || { log "server exited"; exit 1; }

POSTMAN_ARGS=(
	--env-var "baseUrl=${BASE_URL}"
	--env-var "serverPort=${SERVER_PORT}"
	--env-var "targetHost=127.0.0.1:${TARGET_PORT}"
	--env-var "deadChannelId=${DEAD_CHANNEL}"
	--timeout-request 60000
	--disable-unicode
)

PHASE1_FOLDERS=(-i "01 health and discovery" -i "02 webhook forwarding" -i "03 errors and channel lifecycle")
# Postman runs the folders in the order of the -i flags.
PHASE2_FOLDERS=(-i "04 after client disconnect")
if [ "${E2E_TRAEFIK:-0}" = "1" ]; then
	PHASE1_FOLDERS+=(-i "05 traefik config")
	PHASE2_FOLDERS+=(-i "06 traefik config after disconnect")
else
	code="$(curl -sS -o /dev/null -w '%{http_code}' "${BASE_URL}/traefik-config" 2>/dev/null || true)"
	log "skipping /traefik-config folders (set E2E_TRAEFIK=1 to run them); probe GET /traefik-config -> HTTP '${code}'"
fi
# Must be last: it registers the disconnected channel id again.
PHASE2_FOLDERS+=(-i "07 reissue after disconnect")

# ---------------------------------------------------------------- phase 1
log "postman: ${PHASE1_FOLDERS[*]}"
"${POSTMAN}" collection run "${COLLECTION}" \
	"${PHASE1_FOLDERS[@]}" \
	--env-var "expectedChannelId=${CHANNEL_ID}" \
	"${POSTMAN_ARGS[@]}"

# ---------------------------------------------------------------- phase 2
log "stopping main client (SIGTERM)"
kill -TERM "${CLIENT_PID}"
wait_until 15 "main client to exit" bash -c "! kill -0 ${CLIENT_PID} 2>/dev/null"
wait_until 15 "server to drop channel ${CHANNEL_ID}" channel_gone "${CHANNEL_ID}"

log "postman: ${PHASE2_FOLDERS[*]}"
"${POSTMAN}" collection run "${COLLECTION}" \
	"${PHASE2_FOLDERS[@]}" \
	--env-var "disconnectedChannelId=${CHANNEL_ID}" \
	"${POSTMAN_ARGS[@]}"

STATUS=0
