#!/usr/bin/env bash
# Verifies the promises Phase 1 makes about a customer's server, against real machines from the
# harness rather than mocks.
#
# The claims under test:
#   1. Looking at a server changes nothing on it.
#   2. A server already in use is reported as it is, not as empty.
#   3. Ports 80 and 443 being taken is explained and the answer honoured.
#   4. Setting up a server leaves everything already on it running.
#   5. Logs can be read from a container we do not manage.
#   6. A watched server has nothing whatsoever created on it.
set -uo pipefail

cd "$(dirname "$0")/.."

PORT="${YOL_E2E_PORT:-8080}"
API="http://localhost:$PORT"
KEY="$PWD/dev/fake-vps/keys/id_ed25519"
SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5 -i "$KEY")
LOG="${TMPDIR:-/tmp}/yol-verify-api.log"

passed=0
failed=0
api_pid=""

cleanup() {
	if [[ -n "$api_pid" ]] && kill -0 "$api_pid" 2>/dev/null; then
		kill -TERM "$api_pid" 2>/dev/null || true
		wait "$api_pid" 2>/dev/null || true
	fi
}
trap cleanup EXIT

check() {
	local label="$1" condition="$2" detail="${3:-}"
	if [[ "$condition" == "pass" ]]; then
		printf '  \033[32mok\033[0m    %s\n' "$label"
		passed=$((passed + 1))
	else
		printf '  \033[31mFAIL\033[0m  %s\n' "$label"
		[[ -n "$detail" ]] && printf '        %s\n' "$detail"
		failed=$((failed + 1))
	fi
}

# snapshot captures everything that would reveal a change to a machine.
snapshot() {
	local port="$1"
	ssh "${SSH_OPTS[@]}" -p "$port" root@localhost '
		docker ps -a --format "{{.Names}}:{{.State}}" 2>/dev/null | sort
		echo ---
		docker volume ls -q 2>/dev/null | sort
		echo ---
		docker images -q 2>/dev/null | sort
		echo ---
		systemctl list-units --type=service --state=active --no-legend --plain | awk "{print \$1}" | sort
		echo ---
		ls -A /var/lib/yol /usr/local/bin/yol-agent /etc/systemd/system/yol-agent.service 2>/dev/null | sort
	' 2>/dev/null
}

wipe_agent() {
	ssh "${SSH_OPTS[@]}" -p "$1" root@localhost '
		systemctl disable --now yol-agent >/dev/null 2>&1
		rm -rf /var/lib/yol /usr/local/bin/yol-agent /etc/systemd/system/yol-agent.service
		systemctl daemon-reload 2>/dev/null
		docker rm -f yol-router >/dev/null 2>&1
	' >/dev/null 2>&1
}

api() {
	local method="$1" path="$2" body="${3:-}"
	if [[ -n "$body" ]]; then
		curl -sS -X "$method" "$API$path" -H "Authorization: Bearer $ACCOUNT_TOKEN" \
			-H 'Content-Type: application/json' -d "$body"
	else
		curl -sS -X "$method" "$API$path" -H "Authorization: Bearer $ACCOUNT_TOKEN"
	fi
}

json() { python3 -c "import sys,json;print(eval('d'+sys.argv[1]))" "$1"; }
field() { python3 -c "
import sys, json
d = json.load(sys.stdin)
for key in sys.argv[1].split('.'):
    d = d[int(key)] if key.isdigit() else d[key]
print(d)
" "$1"; }

await_status() {
	local server_id="$1" want="$2" limit="${3:-40}"
	for _ in $(seq "$limit"); do
		local status
		status=$(api GET "/v1/organizations/$SLUG/servers/$server_id" | field server.status)
		[[ "$status" == "$want" ]] && { echo "$status"; return 0; }
		[[ "$status" == "failed" ]] && { echo "$status"; return 1; }
		sleep 3
	done
	echo "timeout"
	return 1
}

echo "==> preparing"
make db-reset >/dev/null 2>&1
make build-agent-linux >/dev/null 2>&1
wipe_agent 2201
wipe_agent 2202
wipe_agent 2203

set -a
# shellcheck disable=SC1091
source ./.env
set +a
YOL_HTTP_ADDR=":$PORT" go run ./cmd/api >"$LOG" 2>&1 &
api_pid=$!

for _ in $(seq 60); do
	curl -sf "$API/ready" >/dev/null 2>&1 && break
	sleep 0.5
done
curl -sf "$API/ready" >/dev/null 2>&1 || { echo "the API never became ready"; tail -20 "$LOG"; exit 1; }

ACCOUNT_TOKEN=$(curl -sS -X POST "$API/v1/auth/signup" -H 'Content-Type: application/json' \
	-d '{"email":"verify@example.com","password":"a-long-enough-passphrase","name":"Verify Person"}' | field token)
SLUG=$(curl -sS -X POST "$API/v1/organizations" -H "Authorization: Bearer $ACCOUNT_TOKEN" \
	-H 'Content-Type: application/json' -d '{"name":"Verification Org"}' | field organization.slug)
PRIVATE_KEY=$(python3 -c "import json;print(json.dumps(open('$KEY').read()))")

connect() {
	local name="$1" port="$2" mode="$3"
	api POST "/v1/organizations/$SLUG/servers" \
		"{\"name\":\"$name\",\"host\":\"127.0.0.1\",\"sshPort\":$port,\"sshUser\":\"root\",\"mode\":\"$mode\",\"key\":$PRIVATE_KEY}" \
		| field server.id
}

echo
echo "==> a watched server must have nothing created on it"
WATCH_BEFORE=$(snapshot 2201)
WATCHED=$(connect "Watched server" 2201 watch)
sleep 12
WATCH_AFTER=$(snapshot 2201)

[[ "$WATCH_BEFORE" == "$WATCH_AFTER" ]] &&
	check "watching a server changes nothing on it" pass ||
	check "watching a server changes nothing on it" fail "$(diff <(echo "$WATCH_BEFORE") <(echo "$WATCH_AFTER") | head -6)"

facts=$(api GET "/v1/organizations/$SLUG/servers/$WATCHED" | field server.facts.osName)
[[ -n "$facts" && "$facts" != "None" ]] &&
	check "a watched server is still surveyed and reported" pass ||
	check "a watched server is still surveyed and reported" fail "operating system was not read"

setup_code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
	"$API/v1/organizations/$SLUG/servers/$WATCHED/setup" -H "Authorization: Bearer $ACCOUNT_TOKEN")
[[ "$setup_code" == "409" ]] &&
	check "setting up a watched server is refused" pass ||
	check "setting up a watched server is refused" fail "status $setup_code, want 409"

echo
echo "==> a server already in use"
make vps-messy >/dev/null 2>&1
MESSY_BEFORE=$(snapshot 2202)
MESSY=$(connect "Their existing server" 2202 managed)

status=$(await_status "$MESSY" awaiting_choice 20 || true)
[[ "$status" == "awaiting_choice" ]] &&
	check "a port conflict stops and waits for an answer" pass ||
	check "a port conflict stops and waits for an answer" fail "status $status"

MESSY_AFTER_SURVEY=$(snapshot 2202)
[[ "$MESSY_BEFORE" == "$MESSY_AFTER_SURVEY" ]] &&
	check "looking at their server changed nothing, so cancelling loses nothing" pass ||
	check "looking at their server changed nothing, so cancelling loses nothing" fail \
		"$(diff <(echo "$MESSY_BEFORE") <(echo "$MESSY_AFTER_SURVEY") | head -6)"

resources=$(api GET "/v1/organizations/$SLUG/servers/$MESSY/resources")
for expected in their-nginx their-postgres old-worker; do
	echo "$resources" | grep -q "$expected" &&
		check "$expected is reported as being on the server" pass ||
		check "$expected is reported as being on the server" fail "not present in the inventory"
done

ours=$(echo "$resources" | python3 -c '
import sys, json
print(sum(1 for r in json.load(sys.stdin)["resources"] if r["managed"]))')
[[ "$ours" == "0" ]] &&
	check "nothing already on their server is treated as ours" pass ||
	check "nothing already on their server is treated as ours" fail "$ours marked as managed"

events=$(api GET "/v1/organizations/$SLUG/servers/$MESSY/events")
echo "$events" | grep -q 'their-nginx' &&
	check "the port conflict names the container holding the port" pass ||
	check "the port conflict names the container holding the port" fail "their nginx was not named"

echo
echo "==> their choice is honoured, and their work survives setup"
api PATCH "/v1/organizations/$SLUG/servers/$MESSY/routing" '{"routingMode":"behind_proxy"}' >/dev/null
routing=$(api GET "/v1/organizations/$SLUG/servers/$MESSY" | field server.routingMode)
[[ "$routing" == "behind_proxy" ]] &&
	check "keeping their web server in front is recorded" pass ||
	check "keeping their web server in front is recorded" fail "routing is $routing"

curl -sS -o /dev/null -X POST "$API/v1/organizations/$SLUG/servers/$MESSY/setup" \
	-H "Authorization: Bearer $ACCOUNT_TOKEN"
status=$(await_status "$MESSY" online 40 || true)
[[ "$status" == "online" ]] &&
	check "their server was set up and connected" pass ||
	check "their server was set up and connected" fail "status $status"

sleep 12
survivors=$(ssh "${SSH_OPTS[@]}" -p 2202 root@localhost \
	'docker ps -a --format "{{.Names}}" | sort | tr "\n" " "' 2>/dev/null)
for expected in their-nginx their-postgres old-worker; do
	[[ "$survivors" == *"$expected"* ]] &&
		check "$expected survived setup and reconciliation" pass ||
		check "$expected survived setup and reconciliation" fail "survivors: $survivors"
done

[[ "$survivors" != *"yol-router"* ]] &&
	check "no router was started where their web server keeps the ports" pass ||
	check "no router was started where their web server keeps the ports" fail "a router was started anyway"

serving=$(curl -sS -o /dev/null -w '%{http_code}' http://localhost:8102 || echo 000)
[[ "$serving" == "200" ]] &&
	check "their web server is still serving" pass ||
	check "their web server is still serving" fail "returned $serving"

echo
echo "==> reading logs from a container we do not manage"
ssh "${SSH_OPTS[@]}" -p 2202 root@localhost \
	'docker exec their-nginx curl -s -o /dev/null localhost:80/verification-probe' >/dev/null 2>&1
lines=$(timeout 12 curl -sN \
	"$API/v1/organizations/$SLUG/servers/$MESSY/containers/their-nginx/logs?tail=50" \
	-H "Authorization: Bearer $ACCOUNT_TOKEN" | grep -c '^data: ' || true)
[[ "${lines:-0}" -gt 0 ]] &&
	check "logs stream from a container we did not create" pass ||
	check "logs stream from a container we did not create" fail "no log chunks arrived"

echo
echo "==> ownership: only what we created is ever removed"
ssh "${SSH_OPTS[@]}" -p 2202 root@localhost \
	'docker run -d --name stale-ours --label yol.managed=true --restart=unless-stopped alpine:latest sleep 3600' >/dev/null 2>&1
sleep 25
after=$(ssh "${SSH_OPTS[@]}" -p 2202 root@localhost \
	'docker ps -a --format "{{.Names}}" | sort | tr "\n" " "' 2>/dev/null)

[[ "$after" != *"stale-ours"* ]] &&
	check "a container carrying our label but not wanted is removed" pass ||
	check "a container carrying our label but not wanted is removed" fail "it is still there"
[[ "$after" == *"their-nginx"* && "$after" == *"their-postgres"* ]] &&
	check "their containers were untouched by that removal" pass ||
	check "their containers were untouched by that removal" fail "survivors: $after"

echo
echo "==> a clean server we handle web traffic for"
CLEAN=$(connect "Clean server" 2203 managed)
status=$(await_status "$CLEAN" awaiting_choice 20 || true)
routing=$(api GET "/v1/organizations/$SLUG/servers/$CLEAN" | field server.routingMode)
[[ "$routing" == "takeover" ]] &&
	check "free ports mean we can handle web traffic" pass ||
	check "free ports mean we can handle web traffic" fail "routing is $routing"

curl -sS -o /dev/null -X POST "$API/v1/organizations/$SLUG/servers/$CLEAN/setup" \
	-H "Authorization: Bearer $ACCOUNT_TOKEN"
status=$(await_status "$CLEAN" online 40 || true)
[[ "$status" == "online" ]] &&
	check "the clean server was set up and connected" pass ||
	check "the clean server was set up and connected" fail "status $status"

sleep 20
router=$(ssh "${SSH_OPTS[@]}" -p 2203 root@localhost \
	'docker inspect -f "{{.State.Status}} {{.HostConfig.Memory}}" yol-router 2>/dev/null' 2>/dev/null)
[[ "$router" == running* ]] &&
	check "the router is running" pass ||
	check "the router is running" fail "state: ${router:-absent}"
[[ "$router" == *" 134217728" ]] &&
	check "the router was given a memory limit" pass ||
	check "the router was given a memory limit" fail "limit: ${router:-none}"

routed=$(curl -sS -o /dev/null -w '%{http_code}' http://localhost:8103 || echo 000)
[[ "$routed" == "200" ]] &&
	check "the router answers web traffic" pass ||
	check "the router answers web traffic" fail "returned $routed"

echo
echo "==> the agent recovers on its own"
ssh "${SSH_OPTS[@]}" -p 2203 root@localhost 'docker rm -f yol-router' >/dev/null 2>&1
sleep 25
recovered=$(ssh "${SSH_OPTS[@]}" -p 2203 root@localhost \
	'docker inspect -f "{{.State.Status}}" yol-router 2>/dev/null' 2>/dev/null)
[[ "$recovered" == "running" ]] &&
	check "a container removed by hand is put back without asking" pass ||
	check "a container removed by hand is put back without asking" fail "state: ${recovered:-absent}"

echo
printf '%d passed, %d failed\n' "$passed" "$failed"
[[ "$failed" -eq 0 ]] || exit 1
