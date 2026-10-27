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
#   4. postman collection run: folders 01..03, 05
#   5. stop the main client, wait until the server drops the channel
#   6. postman collection run: folders 04, 06, 07
#
# Pub/Sub (redis) mode:
#   7. start redis (tests/e2e/redis) and two servers (node A, node B) with
#      --cluster-backend redis, and the clients connected to node B
#   8. postman collection run against node A: folders 02, 11
#      (every webhook is relayed from node A to node B through redis)
#   9. stop the main client, postman collection run: folders 12, 07
#
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
# webhook_status <base-url> <channel> prints the status of a webhook to the channel.
webhook_status() { curl -sS -o /dev/null -w '%{http_code}' -X POST "$1/webhook/$2/__e2e_probe" 2>/dev/null || true; }
relay_connected() { [ "$(webhook_status "$1" "$2")" != "404" ]; }
relay_gone() { [ "$(webhook_status "$1" "$2")" = "404" ]; }
channel_gone() { ! curl -fsS "${BASE_URL}/internal/channels" | grep -q "\"$1\""; }
proc_alive() { kill -0 "$1" 2>/dev/null; }

# ---------------------------------------------------------------- build
log "building binaries into ${WORK_DIR}"
(cd "${ROOT_DIR}" && "${GO}" build -o "${WORK_DIR}/wow" . && "${GO}" build -o "${WORK_DIR}/target" ./tests/e2e/target &&
	"${GO}" build -o "${WORK_DIR}/redis" ./tests/e2e/redis)
WOW="${WORK_DIR}/wow"

# Avoid picking up WOW_* configuration from the caller's environment.
while IFS= read -r v; do unset "${v}"; done < <(env | sed -n 's/^\(WOW_[A-Za-z0-9_]*\)=.*/\1/p')
unset POD_IP || true

PORTS=()
while IFS= read -r p; do PORTS+=("${p}"); done < <("${WORK_DIR}/target" -free-ports 7)
TARGET_PORT="${PORTS[0]}"
SERVER_PORT="${PORTS[1]}"
MEMBERLIST_PORT="${PORTS[2]}"
DEAD_PORT="${PORTS[3]}"
REDIS_PORT="${PORTS[4]}"
NODE_A_PORT="${PORTS[5]}"
NODE_B_PORT="${PORTS[6]}"
BASE_URL="http://127.0.0.1:${SERVER_PORT}"
DEAD_CHANNEL="e2e-dead-target-$$"
log "ports: target=${TARGET_PORT} server=${SERVER_PORT} memberlist=${MEMBERLIST_PORT} dead=${DEAD_PORT} redis=${REDIS_PORT} node-a=${NODE_A_PORT} node-b=${NODE_B_PORT}"

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

# Postman runs the folders in the order of the -i flags.
PHASE1_FOLDERS=(-i "01 health and discovery" -i "02 webhook forwarding" -i "03 errors and channel lifecycle" -i "05 traefik config")
# 07 must be last: it registers the disconnected channel id again.
PHASE2_FOLDERS=(-i "04 after client disconnect" -i "06 traefik config after disconnect" -i "07 reissue after disconnect")

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

# ================================================================ Pub/Sub (redis) mode
REDIS_URL="redis://127.0.0.1:${REDIS_PORT}"
NODE_A_URL="http://127.0.0.1:${NODE_A_PORT}"
NODE_B_URL="http://127.0.0.1:${NODE_B_PORT}"
PUBSUB_DEAD_CHANNEL="e2e-pubsub-dead-target-$$"

"${WORK_DIR}/redis" -addr "127.0.0.1:${REDIS_PORT}" >"${WORK_DIR}/redis.log" 2>&1 &
PIDS+=($!)
wait_until 20 "redis" bash -c "exec 3<>/dev/tcp/127.0.0.1/${REDIS_PORT}"

for node in a b; do
	port="${NODE_A_PORT}"
	[ "${node}" = "b" ] && port="${NODE_B_PORT}"
	"${WOW}" server --port "${port}" --cluster-backend redis --redis-url "${REDIS_URL}" \
		--redis-heartbeat-duration 1s --log-level DEBUG \
		>"${WORK_DIR}/server-node-${node}.log" 2>&1 &
	PIDS+=($!)
done
wait_until 20 "node A /healthz" curl -fsS "${NODE_A_URL}/healthz"
wait_until 20 "node B /healthz" curl -fsS "${NODE_B_URL}/healthz"

# The clients connect to node B, while the webhooks are sent to node A.
"${WOW}" client --server-url "${NODE_B_URL}" --target-url "http://127.0.0.1:${TARGET_PORT}" \
	>"${WORK_DIR}/client-pubsub.log" 2>&1 &
PUBSUB_CLIENT_PID=$!
PIDS+=("${PUBSUB_CLIENT_PID}")

"${WOW}" client --server-url "${NODE_B_URL}" --target-url "http://127.0.0.1:${DEAD_PORT}" \
	--channel-id "${PUBSUB_DEAD_CHANNEL}" --transfer-request-timeout 2s \
	>"${WORK_DIR}/client-pubsub-dead-target.log" 2>&1 &
PIDS+=($!)

pubsub_client_issued() { grep -q '^Issued Channel ID: ' "${WORK_DIR}/client-pubsub.log"; }
wait_until 20 "pub/sub client to print its channel id" pubsub_client_issued
PUBSUB_CHANNEL_ID="$(sed -n 's/^Issued Channel ID: //p' "${WORK_DIR}/client-pubsub.log" | head -n1)"
log "pub/sub client channel: ${PUBSUB_CHANNEL_ID}"

wait_until 30 "pub/sub client to be reachable from node A" relay_connected "${NODE_A_URL}" "${PUBSUB_CHANNEL_ID}"
wait_until 30 "pub/sub dead-target client to be reachable from node A" relay_connected "${NODE_A_URL}" "${PUBSUB_DEAD_CHANNEL}"

PUBSUB_ARGS=(
	--env-var "baseUrl=${NODE_A_URL}"
	--env-var "otherBaseUrl=${NODE_B_URL}"
	--env-var "serverPort=${NODE_A_PORT}"
	--env-var "targetHost=127.0.0.1:${TARGET_PORT}"
	--env-var "deadChannelId=${PUBSUB_DEAD_CHANNEL}"
	--timeout-request 60000
	--disable-unicode
)

log "postman (pub/sub): 02, 11"
"${POSTMAN}" collection run "${COLLECTION}" \
	-i "02 webhook forwarding" -i "11 pubsub errors and channel lifecycle" \
	--env-var "channelId=${PUBSUB_CHANNEL_ID}" \
	"${PUBSUB_ARGS[@]}"

log "stopping pub/sub client (SIGTERM)"
kill -TERM "${PUBSUB_CLIENT_PID}"
wait_until 15 "pub/sub client to exit" bash -c "! kill -0 ${PUBSUB_CLIENT_PID} 2>/dev/null"
wait_until 15 "node A to see channel ${PUBSUB_CHANNEL_ID} gone" relay_gone "${NODE_A_URL}" "${PUBSUB_CHANNEL_ID}"

log "postman (pub/sub): 12, 07"
"${POSTMAN}" collection run "${COLLECTION}" \
	-i "12 pubsub after client disconnect" -i "07 reissue after disconnect" \
	--env-var "disconnectedChannelId=${PUBSUB_CHANNEL_ID}" \
	"${PUBSUB_ARGS[@]}"

STATUS=0
