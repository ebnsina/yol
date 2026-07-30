#!/usr/bin/env bash
# Verifies the promise Phase 2 makes: a push becomes a running app, and replacing it drops no
# requests. Run against a real machine from the harness, with a stand-in for GitHub so no
# repository, installation or network is needed.
#
# The claims under test:
#   1. A commit becomes an image built on the customer's own server.
#   2. What was built is what runs, and it answers.
#   3. Replacing it drops no requests at all.
#   4. Traffic really moved: the new version is what answers afterwards.
#   5. A version that never answers fails the deploy and leaves the old one serving.
#   6. Going back to a previous version needs no build.
set -uo pipefail

cd "$(dirname "$0")/.."

PORT="${YOL_E2E_PORT:-8080}"
GITHUB_PORT="${YOL_E2E_GITHUB_PORT:-8099}"
API="http://localhost:$PORT"
KEY="$PWD/dev/fake-vps/keys/id_ed25519"
SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5 -i "$KEY")
SCRATCH="${TMPDIR:-/tmp}/yol-verify-phase2"
LOG="$SCRATCH/api.log"
GITHUB_LOG="$SCRATCH/github.log"
VERSION_FILE="$SCRATCH/branch-head"

# One address for the stand-in, because two different things reach it: the control plane, running
# here, and the agent, running inside a stand-in server. `host.docker.internal` is only meaningful
# inside a container and does not resolve here, so the machine's own address is used instead.
HOST_ADDRESS="${YOL_E2E_HOST_ADDRESS:-$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}')}"
[[ -n "$HOST_ADDRESS" ]] || { echo "could not work out this machine's address"; exit 1; }
GITHUB="http://$HOST_ADDRESS:$GITHUB_PORT"

# The harness server this runs against, and the port its router answers on.
VPS_SSH_PORT=2201
VPS_HTTP="http://localhost:8101"

passed=0
failed=0
api_pid=""
github_pid=""

mkdir -p "$SCRATCH"

cleanup() {
	for pid in "$api_pid" "$github_pid"; do
		if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
			kill -TERM "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	# The binaries those commands compiled and ran, which outlive the command itself.
	pkill -u "$(id -u)" -f 'go-build.*/exe/api' 2>/dev/null || true
	pkill -u "$(id -u)" -f 'go-build.*/exe/fake-github' 2>/dev/null || true
}
trap cleanup EXIT

# free_port stops whatever this run needs the port for, and only what we recognise: killing by port
# alone once took down something that had nothing to do with this.
free_port() {
	local port="$1" pid name
	for pid in $(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null); do
		name=$(basename "$(ps -p "$pid" -o comm= 2>/dev/null)")
		case "$name" in
			api | fake-github)
				kill "$pid" 2>/dev/null || true
				;;
			*)
				echo "port $port is held by $name ($pid), which is not ours to stop"
				exit 1
				;;
		esac
	done
}

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

api() {
	local method="$1" path="$2" body="${3:-}"
	if [[ -n "$body" ]]; then
		curl -sS -X "$method" "$API$path" -H "Authorization: Bearer $ACCOUNT_TOKEN" \
			-H 'Content-Type: application/json' -d "$body"
	else
		curl -sS -X "$method" "$API$path" -H "Authorization: Bearer $ACCOUNT_TOKEN"
	fi
}

field() { python3 -c "
import sys, json
d = json.load(sys.stdin)
for key in sys.argv[1].split('.'):
    d = d[int(key)] if key.isdigit() else d[key]
print(d)
" "$1"; }

wipe_agent() {
	ssh "${SSH_OPTS[@]}" -p "$1" root@localhost '
		systemctl disable --now yol-agent >/dev/null 2>&1
		rm -rf /var/lib/yol /usr/local/bin/yol-agent /etc/systemd/system/yol-agent.service
		systemctl daemon-reload 2>/dev/null
		docker ps -aq --filter "label=yol.managed=true" | xargs -r docker rm -f >/dev/null 2>&1
		docker rm -f yol-router >/dev/null 2>&1
		docker images --format "{{.Repository}}:{{.Tag}}" | grep "^yol/" | xargs -r docker rmi -f >/dev/null 2>&1
		docker buildx rm yol-builder >/dev/null 2>&1
	' >/dev/null 2>&1
}

await_server() {
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

# await_deployment waits for one to reach a status, or to fail trying.
await_deployment() {
	local deployment_id="$1" want="$2" limit="${3:-60}"
	for _ in $(seq "$limit"); do
		local status
		status=$(api GET "/v1/organizations/$SLUG/projects/$PROJECT/deployments/$deployment_id" |
			field deployment.status)
		[[ "$status" == "$want" ]] && { echo "$status"; return 0; }
		if [[ "$status" == "failed" && "$want" != "failed" ]]; then
			echo "$status"
			return 1
		fi
		sleep 5
	done
	echo "timeout"
	return 1
}

echo "==> preparing"
make db-reset >/dev/null 2>&1
make build-agent-linux >/dev/null 2>&1
wipe_agent "$VPS_SSH_PORT"
free_port "$PORT"
free_port "$GITHUB_PORT"
echo one >"$VERSION_FILE"

go run ./dev/fake-github --addr ":$GITHUB_PORT" --version-file "$VERSION_FILE" >"$GITHUB_LOG" 2>&1 &
github_pid=$!

set -a
# shellcheck disable=SC1091
source ./.env
set +a
# Where the control plane looks for GitHub, and where the agent fetches code from.
YOL_GITHUB_API_URL="$GITHUB" YOL_HTTP_ADDR=":$PORT" go run ./cmd/api >"$LOG" 2>&1 &
api_pid=$!

for _ in $(seq 60); do
	curl -sf "$API/ready" >/dev/null 2>&1 && break
	sleep 0.5
done
curl -sf "$API/ready" >/dev/null 2>&1 || { echo "the API never became ready"; tail -20 "$LOG"; exit 1; }
kill -0 "$github_pid" 2>/dev/null ||
	{ echo "the stand-in for GitHub did not start"; tail -10 "$GITHUB_LOG"; exit 1; }
curl -sf "$GITHUB/installation/repositories" >/dev/null 2>&1 ||
	{ echo "the stand-in for GitHub never became ready at $GITHUB"; tail -10 "$GITHUB_LOG"; exit 1; }
ssh "${SSH_OPTS[@]}" -p "$VPS_SSH_PORT" root@localhost \
	"curl -sf --max-time 5 $GITHUB/installation/repositories >/dev/null" 2>/dev/null ||
	{ echo "the server cannot reach the stand-in at $GITHUB, so no build could fetch code"; exit 1; }

ACCOUNT_TOKEN=$(curl -sS -X POST "$API/v1/auth/signup" -H 'Content-Type: application/json' \
	-d '{"email":"deploy@example.com","password":"a-long-enough-passphrase","name":"Deploy Person"}' | field token)
SLUG=$(curl -sS -X POST "$API/v1/organizations" -H "Authorization: Bearer $ACCOUNT_TOKEN" \
	-H 'Content-Type: application/json' -d '{"name":"Deploying Org"}' | field organization.slug)
PRIVATE_KEY=$(python3 -c "import json;print(json.dumps(open('$KEY').read()))")

echo
echo "==> a server to deploy to"
SERVER=$(api POST "/v1/organizations/$SLUG/servers" \
	"{\"name\":\"Deploy target\",\"host\":\"127.0.0.1\",\"sshPort\":$VPS_SSH_PORT,\"sshUser\":\"root\",\"mode\":\"managed\",\"key\":$PRIVATE_KEY}" |
	field server.id)

# Looking at a server changes nothing, so setting it up is a separate thing to ask for.
status=$(await_server "$SERVER" awaiting_choice 30 || true)
[[ "$status" == "awaiting_choice" ]] ||
	{ echo "the server was never looked at (status $status)"; tail -20 "$LOG"; exit 1; }
curl -sS -o /dev/null -X POST "$API/v1/organizations/$SLUG/servers/$SERVER/setup" \
	-H "Authorization: Bearer $ACCOUNT_TOKEN"

status=$(await_server "$SERVER" online 60 || true)
[[ "$status" == "online" ]] &&
	check "the server is set up and its agent is connected" pass ||
	check "the server is set up and its agent is connected" fail "status $status"
[[ "$status" == "online" ]] || { echo; echo "nothing further can be verified"; tail -30 "$LOG"; exit 1; }

echo
echo "==> a project pointed at code"
PROJECT=$(api POST "/v1/organizations/$SLUG/projects" '{"name":"Shop"}' | field project.id)
ENVIRONMENT=$(api GET "/v1/organizations/$SLUG/projects/$PROJECT" | field project.environments.0.id)
SERVICE=$(api GET "/v1/organizations/$SLUG/projects/$PROJECT" | field project.environments.0.services.0.id)

api PATCH "/v1/organizations/$SLUG/projects/$PROJECT/environments/$ENVIRONMENT" \
	"{\"serverId\":\"$SERVER\"}" >/dev/null
api POST "/v1/organizations/$SLUG/github/installations" '{"installationId":"42"}' >/dev/null
api PUT "/v1/organizations/$SLUG/projects/$PROJECT/repository" \
	'{"installationId":"42","repositoryId":"987","fullName":"harness/shop"}' >/dev/null

# What the app answers on, so the rollout has something to wait for.
api PATCH "/v1/organizations/$SLUG/projects/$PROJECT/services/$SERVICE" \
	'{"healthPath":"/","healthPort":3000}' >/dev/null

repo=$(api GET "/v1/organizations/$SLUG/projects/$PROJECT" | field project.repository.fullName)
[[ "$repo" == "harness/shop" ]] &&
	check "the project knows where its code comes from" pass ||
	check "the project knows where its code comes from" fail "repository $repo"

echo
echo "==> the first deploy builds on the customer's own server"
FIRST=$(api POST "/v1/organizations/$SLUG/projects/$PROJECT/environments/$ENVIRONMENT/deployments" '{}' |
	field deployment.id)

status=$(await_deployment "$FIRST" live 60 || true)
[[ "$status" == "live" ]] &&
	check "the first version was built and is serving" pass ||
	check "the first version was built and is serving" fail "status $status"

built=$(ssh "${SSH_OPTS[@]}" -p "$VPS_SSH_PORT" root@localhost \
	'docker images --format "{{.Repository}}:{{.Tag}}" | grep -c "^yol/"' 2>/dev/null || echo 0)
[[ "${built:-0}" -ge 1 ]] &&
	check "the image was built on the server itself" pass ||
	check "the image was built on the server itself" fail "no image of ours is on the machine"

output=$(api GET "/v1/organizations/$SLUG/projects/$PROJECT/deployments/$FIRST/logs")
echo "$output" | grep -q "Fetching the code" &&
	check "the build output was kept and can be read back" pass ||
	check "the build output was kept and can be read back" fail "nothing was recorded"

answer=$(curl -sS --max-time 5 "$VPS_HTTP/" 2>/dev/null)
[[ "$answer" == "version-one" ]] &&
	check "the app answers on the server's address" pass ||
	check "the app answers on the server's address" fail "answered '$answer'"

echo
echo "==> replacing it drops no requests"
echo two >"$VERSION_FILE"

# Asking continuously while the next version goes out. Every failure is counted, and any at all
# means the promise is broken.
FAILURES="$SCRATCH/failures"
ANSWERS="$SCRATCH/answers"
: >"$FAILURES"
: >"$ANSWERS"

(
	while [[ ! -f "$SCRATCH/stop" ]]; do
		body=$(curl -sS --max-time 4 "$VPS_HTTP/" 2>/dev/null)
		if [[ -z "$body" ]]; then
			echo "empty" >>"$FAILURES"
		else
			echo "$body" >>"$ANSWERS"
		fi
		sleep 0.1
	done
) &
loop_pid=$!

SECOND=$(api POST "/v1/organizations/$SLUG/projects/$PROJECT/environments/$ENVIRONMENT/deployments" '{}' |
	field deployment.id)
status=$(await_deployment "$SECOND" live 60 || true)

# Long enough afterwards to catch a request dropped while the old version is retired.
sleep 15
touch "$SCRATCH/stop"
wait "$loop_pid" 2>/dev/null || true
rm -f "$SCRATCH/stop"

[[ "$status" == "live" ]] &&
	check "the second version was built and took over" pass ||
	check "the second version was built and took over" fail "status $status"

dropped=$(wc -l <"$FAILURES" | tr -d ' ')
served=$(wc -l <"$ANSWERS" | tr -d ' ')
[[ "$dropped" == "0" ]] &&
	check "no request was dropped while the version changed ($served served)" pass ||
	check "no request was dropped while the version changed" fail "$dropped of $((served + dropped)) failed"

grep -q "version-two" "$ANSWERS" &&
	check "traffic moved: the new version answered" pass ||
	check "traffic moved: the new version answered" fail "only the old version ever answered"

answer=$(curl -sS --max-time 5 "$VPS_HTTP/" 2>/dev/null)
[[ "$answer" == "version-two" ]] &&
	check "the new version is what answers now" pass ||
	check "the new version is what answers now" fail "answered '$answer'"

running=$(ssh "${SSH_OPTS[@]}" -p "$VPS_SSH_PORT" root@localhost \
	'docker ps --filter "label=yol.role=app" --format "{{.Names}}" | wc -l' 2>/dev/null | tr -d ' ')
[[ "${running:-0}" == "1" ]] &&
	check "the version it replaced was retired afterwards" pass ||
	check "the version it replaced was retired afterwards" fail "$running app containers running"

echo
echo "==> a version that never answers changes nothing"
# A health check nothing will satisfy, so the next deploy cannot come up.
api PATCH "/v1/organizations/$SLUG/projects/$PROJECT/services/$SERVICE" \
	'{"healthPath":"/nothing-here","healthPort":9999}' >/dev/null

BROKEN=$(api POST "/v1/organizations/$SLUG/projects/$PROJECT/environments/$ENVIRONMENT/deployments" '{}' |
	field deployment.id)
status=$(await_deployment "$BROKEN" failed 60 || true)

[[ "$status" == "failed" ]] &&
	check "a version that never answers fails the deploy" pass ||
	check "a version that never answers fails the deploy" fail "status $status"

reason=$(api GET "/v1/organizations/$SLUG/projects/$PROJECT/deployments/$BROKEN" | field deployment.failureReason)
[[ -n "$reason" && "$reason" != "None" ]] &&
	check "the failure says what happened, in words" pass ||
	check "the failure says what happened, in words" fail "no reason was recorded"

answer=$(curl -sS --max-time 5 "$VPS_HTTP/" 2>/dev/null)
[[ "$answer" == "version-two" ]] &&
	check "the version already serving was left alone" pass ||
	check "the version already serving was left alone" fail "answered '$answer'"

echo
echo "==> going back to a previous version needs no build"
api PATCH "/v1/organizations/$SLUG/projects/$PROJECT/services/$SERVICE" \
	'{"healthPath":"/","healthPort":3000}' >/dev/null

images_before=$(ssh "${SSH_OPTS[@]}" -p "$VPS_SSH_PORT" root@localhost \
	'docker images --format "{{.ID}}" | sort -u | wc -l' 2>/dev/null | tr -d ' ')

BACK=$(api POST "/v1/organizations/$SLUG/projects/$PROJECT/deployments/$FIRST/rollback" '{}' |
	field deployment.id)
status=$(await_deployment "$BACK" live 40 || true)

[[ "$status" == "live" ]] &&
	check "the older version is serving again" pass ||
	check "the older version is serving again" fail "status $status"

answer=$(curl -sS --max-time 5 "$VPS_HTTP/" 2>/dev/null)
[[ "$answer" == "version-one" ]] &&
	check "what answers is the version rolled back to" pass ||
	check "what answers is the version rolled back to" fail "answered '$answer'"

images_after=$(ssh "${SSH_OPTS[@]}" -p "$VPS_SSH_PORT" root@localhost \
	'docker images --format "{{.ID}}" | sort -u | wc -l' 2>/dev/null | tr -d ' ')
[[ "${images_before:-0}" == "${images_after:-1}" ]] &&
	check "nothing was rebuilt to go back" pass ||
	check "nothing was rebuilt to go back" fail "images went from $images_before to $images_after"

echo
printf '%d passed' "$passed"
[[ "$failed" -gt 0 ]] && printf ', \033[31m%d failed\033[0m' "$failed"
echo
[[ "$failed" -eq 0 ]] || { echo; echo "the API log ends:"; tail -25 "$LOG"; }
exit $((failed > 0))
