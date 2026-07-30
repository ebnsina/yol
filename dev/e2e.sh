#!/usr/bin/env bash
# Runs the end-to-end checks against a freshly migrated database and a freshly started API.
# A script rather than Makefile lines, because make runs each line in its own shell and a
# backgrounded server does not survive to the next one.
set -euo pipefail

cd "$(dirname "$0")/.."

PORT="${YOL_E2E_PORT:-8080}"
LOG="${TMPDIR:-/tmp}/yol-e2e-api.log"
api_pid=""

cleanup() {
	if [[ -n "$api_pid" ]] && kill -0 "$api_pid" 2>/dev/null; then
		kill -TERM "$api_pid" 2>/dev/null || true
		wait "$api_pid" 2>/dev/null || true
	fi
}
trap cleanup EXIT

if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t >/dev/null 2>&1; then
	echo "port $PORT is already in use; stop whatever is listening first" >&2
	exit 1
fi

echo "==> rebuilding the database"
make db-reset >/dev/null

echo "==> starting the API"
set -a
# shellcheck disable=SC1091
source ./.env
set +a
YOL_HTTP_ADDR=":$PORT" go run ./cmd/api >"$LOG" 2>&1 &
api_pid=$!

for _ in $(seq 60); do
	if curl -sf "localhost:$PORT/ready" >/dev/null 2>&1; then break; fi
	if ! kill -0 "$api_pid" 2>/dev/null; then
		echo "the API exited before becoming ready:" >&2
		tail -20 "$LOG" >&2
		exit 1
	fi
	sleep 0.5
done

if ! curl -sf "localhost:$PORT/ready" >/dev/null 2>&1; then
	echo "the API never became ready:" >&2
	tail -20 "$LOG" >&2
	exit 1
fi

echo "==> running end-to-end checks"
YOL_E2E_URL="http://localhost:$PORT" go test -tags e2e -count=1 "$@" ./internal/api/
