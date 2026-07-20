#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
shauth_root=${SHAUTH_SOURCE_DIR:?SHAUTH_SOURCE_DIR must point to a Shauth checkout}
work=$(mktemp -d "${TMPDIR:-/tmp}/intraktible-shauth.XXXXXX")
project="intraktible-shauth-${$}"
network="${project}-network"
postgres="${project}-postgres"
hydra="${project}-hydra"
provider_image="${project}-provider"
port_base=$((38000 + ($$ % 1000) * 10))
postgres_port=$((port_base + 1))
hydra_public_port=$((port_base + 2))
hydra_admin_port=$((port_base + 3))
shauth_port=$((port_base + 4))
app_port=$((port_base + 5))
api_port=$((port_base + 6))
client_id=intraktible-integration
client_secret=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
validation_username='admin'
validation_password=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
relying_party_rejection_sentinel=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
if [ "$relying_party_rejection_sentinel" = "$validation_password" ]; then
	echo "The relying-party rejection sentinel matched the Shauth validation password." >&2
	exit 1
fi
postgres_password=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
hydra_secret=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
shauth_pid=
api_pid=
web_pid=

cleanup() {
	status=$?
	for pid in "$web_pid" "$api_pid" "$shauth_pid"; do
		if [ -n "$pid" ]; then
			kill "$pid" 2>/dev/null || true
		fi
	done
	wait 2>/dev/null || true
	if [ "$status" -ne 0 ]; then
		for log in "$work"/*.log; do
			if [ -f "$log" ]; then
				echo "===== $log =====" >&2
				tail -n 160 "$log" >&2 || true
			fi
		done
		docker logs --tail 160 "$hydra" >&2 2>/dev/null || true
		docker logs --tail 160 "$postgres" >&2 2>/dev/null || true
	fi
	docker rm -f "$hydra" "$postgres" >/dev/null 2>&1 || true
	docker network rm "$network" >/dev/null 2>&1 || true
	docker image rm "$provider_image" >/dev/null 2>&1 || true
	rm -rf "$work"
	trap - EXIT INT TERM
	exit "$status"
}
trap cleanup EXIT INT TERM

wait_http() {
	url=$1
	label=$2
	for _ in $(seq 1 120); do
		if curl --fail --silent --show-error "$url" >/dev/null 2>&1; then
			return
		fi
		sleep 1
	done
	echo "$label did not become ready at $url" >&2
	return 1
}

docker network create "$network" >/dev/null
docker run --detach --name "$postgres" --network "$network" --network-alias postgres \
	--publish "127.0.0.1:${postgres_port}:5432" \
	--env POSTGRES_DB=shauth --env POSTGRES_USER=shauth --env "POSTGRES_PASSWORD=${postgres_password}" \
	postgres:17.5-alpine >/dev/null
for _ in $(seq 1 60); do
	if docker exec "$postgres" psql -U shauth -d shauth -Atc 'SELECT 1' 2>/dev/null | grep -qx 1; then
		break
	fi
	sleep 1
done
docker exec "$postgres" psql -U shauth -d shauth -Atc 'SELECT 1' | grep -qx 1
docker exec "$postgres" createdb -U shauth hydra
docker exec "$postgres" createdb -U shauth intraktible

# Build the exact Shauth revision under test. Its immutable artifact includes the
# audited Ory Hydra provider build used in production, so this relying-party gate
# exercises the provider's signed Back-Channel Logout token contract rather than
# an unpatched upstream container with different claims.
docker build --load --tag "$provider_image" "$shauth_root" >"$work/provider-build.log" 2>&1
hydra_dsn="postgres://shauth:${postgres_password}@postgres:5432/hydra?sslmode=disable"
docker run --rm --network "$network" --entrypoint /hydra --env "DSN=${hydra_dsn}" "$provider_image" \
	migrate sql up --read-from-env --yes >"$work/hydra-migrate.log" 2>&1
docker run --detach --name "$hydra" --network "$network" \
	--add-host host.docker.internal:host-gateway --entrypoint /hydra \
	--publish "127.0.0.1:${hydra_public_port}:4444" --publish "127.0.0.1:${hydra_admin_port}:4445" \
	--volume "${shauth_root}/config/hydra.yaml:/etc/config/hydra.yaml:ro" \
	--env "DSN=${hydra_dsn}" --env "HYDRA_DSN=${hydra_dsn}" \
	--env "URLS_SELF_ISSUER=http://localhost:${hydra_public_port}" \
	--env "URLS_LOGIN=http://localhost:${shauth_port}/oauth/login" \
	--env "URLS_CONSENT=http://localhost:${shauth_port}/oauth/consent" \
	--env "URLS_LOGOUT=http://localhost:${shauth_port}/oauth/logout" \
	--env "URLS_POST_LOGOUT_REDIRECT=http://localhost:${shauth_port}/" \
	--env "SECRETS_SYSTEM_0=${hydra_secret}" \
	"$provider_image" serve all --dev --config /etc/config/hydra.yaml >/dev/null
wait_http "http://localhost:${hydra_public_port}/health/ready" "Ory Hydra"

(cd "$shauth_root" && go build -o "$work/shauth" ./cmd/shauth && go build -o "$work/shauth-migrate" ./cmd/shauth-migrate)
shauth_dsn="postgres://shauth:${postgres_password}@localhost:${postgres_port}/shauth?sslmode=disable"
DATABASE_URL="$shauth_dsn" SHAUTH_MIGRATIONS_DIR="$shauth_root/migrations" "$work/shauth-migrate"
bootstrap_apps=$(printf '[{"slug":"intraktible","name":"Intraktible","description":"Agentic decision platform.","launch_url":"http://localhost:%s/","oidc_client_id":"%s","oidc_client_secret":"%s","redirect_uris":["http://localhost:%s/v1/auth/oidc/shauth/callback"],"post_logout_redirect_uris":["http://localhost:%s/v1/auth/signed-out"],"frontchannel_logout_uri":"http://localhost:%s/v1/auth/oidc/shauth/frontchannel-logout","backchannel_logout_uri":"http://localhost:%s/v1/auth/oidc/shauth/backchannel-logout","health_url":"http://localhost:%s/healthz","monitoring_url":""}]' "$app_port" "$client_id" "$client_secret" "$app_port" "$app_port" "$app_port" "$app_port" "$app_port")
SHAUTH_LISTEN_ADDRESS="127.0.0.1:${shauth_port}" \
	SHAUTH_PUBLIC_URL="http://localhost:${shauth_port}" SHAUTH_ALLOW_INSECURE_COOKIES=true \
	DATABASE_URL="$shauth_dsn" HYDRA_ADMIN_URL="http://localhost:${hydra_admin_port}" \
	HYDRA_PUBLIC_INTERNAL_URL="http://localhost:${hydra_public_port}" \
	GITHUB_CLIENT_ID=test-client GITHUB_CLIENT_SECRET=test-client-secret-not-used \
	GITHUB_DEVELOPER_TEAM=e6qu-org/e6qu-org-members GITHUB_ADMIN_TEAM=e6qu-org/e6qu-org-admins \
	SHAUTH_SES_REGION=eu-west-1 SHAUTH_INVITATION_EMAIL_FROM=no-reply@localhost.test \
	SHAUTH_BOOTSTRAP_ADMIN_EMAIL=admin@localhost.test SHAUTH_BOOTSTRAP_ADMIN_PASSWORD="$validation_password" \
	SHAUTH_BOOTSTRAP_APPS_JSON="$bootstrap_apps" "$work/shauth" >"$work/shauth.log" 2>&1 &
shauth_pid=$!
wait_http "http://localhost:${shauth_port}/healthz" "Shauth"

# The browser uses the public localhost coordinate. Ory Hydra runs in a Docker
# network, so only its delivery coordinate is rewritten to the same host service
# through Docker's host gateway.
client_registration=$(curl --fail --silent --show-error "http://localhost:${hydra_admin_port}/admin/clients/${client_id}")
client_registration=$(printf '%s' "$client_registration" | sed "s#http://localhost:${app_port}/v1/auth/oidc/shauth/backchannel-logout#http://host.docker.internal:${api_port}/v1/auth/oidc/shauth/backchannel-logout#")
curl --fail --silent --show-error --request PUT --header 'Content-Type: application/json' \
	--data "$client_registration" "http://localhost:${hydra_admin_port}/admin/clients/${client_id}" >/dev/null

(cd "$repo_root" && go build -o "$work/intraktible" ./cmd/intraktible)
intraktible_dsn="postgres://shauth:${postgres_password}@localhost:${postgres_port}/intraktible?sslmode=disable"
env -i PATH="$PATH" HOME="${HOME:-/tmp}" TMPDIR="${TMPDIR:-/tmp}" \
	INTRAKTIBLE_POSTGRES_DSN="$intraktible_dsn" INTRAKTIBLE_OIDC_PROVIDERS=shauth \
	INTRAKTIBLE_OIDC_SHAUTH_ISSUER="http://localhost:${hydra_public_port}" \
	INTRAKTIBLE_OIDC_SHAUTH_CLIENT_ID="$client_id" INTRAKTIBLE_OIDC_SHAUTH_CLIENT_SECRET="$client_secret" \
	INTRAKTIBLE_OIDC_SHAUTH_REDIRECT_URL="http://localhost:${app_port}/v1/auth/oidc/shauth/callback" \
	INTRAKTIBLE_OIDC_SHAUTH_POST_LOGOUT_REDIRECT_URL="http://localhost:${app_port}/v1/auth/signed-out" \
	INTRAKTIBLE_OIDC_SHAUTH_ORG=dev INTRAKTIBLE_OIDC_SHAUTH_WORKSPACE=main \
	INTRAKTIBLE_OIDC_SHAUTH_DEFAULT_ROLE=admin INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS=1000 \
	INTRAKTIBLE_LOGIN_RATE_LIMIT_BURST=2000 "$work/intraktible" serve --addr=":${api_port}" \
	--log=postgres --store=postgres --modules=all >"$work/intraktible.log" 2>&1 &
api_pid=$!
wait_http "http://localhost:${api_port}/healthz" "Intraktible API"
intraktible_process=$(ps eww -p "$api_pid" -o command=)
for forbidden in SHAUTH_VALIDATION_USERNAME= SHAUTH_VALIDATION_PASSWORD= "$validation_password"; do
	case $intraktible_process in
	*"$forbidden"*)
		echo "Intraktible received a Shauth validation credential or variable." >&2
		exit 1
		;;
	esac
done

(cd "$repo_root/web" && INTRAKTIBLE_DEV_API_URL="http://localhost:${api_port}" npm run build >/dev/null && \
	INTRAKTIBLE_DEV_API_URL="http://localhost:${api_port}" npx vite preview --port "$app_port" --strictPort) \
	>"$work/web.log" 2>&1 &
web_pid=$!
wait_http "http://localhost:${app_port}/healthz" "Intraktible web UI"

SHAUTH_URL="http://localhost:${shauth_port}" INTRAKTIBLE_URL="http://localhost:${app_port}" \
	SHAUTH_VALIDATION_USERNAME="$validation_username" SHAUTH_VALIDATION_PASSWORD="$validation_password" \
	INTRAKTIBLE_REJECTION_SENTINEL="$relying_party_rejection_sentinel" \
	PLAYWRIGHT_EXECUTABLE_PATH="${PLAYWRIGHT_EXECUTABLE_PATH:-}" \
	node "$repo_root/web/e2e-shauth.mjs"

logout_tokens=$(docker exec "$postgres" psql -U shauth -d intraktible -Atc \
	"SELECT count(*) FROM docs WHERE collection='auth_oidc_logout_replays'")
if [ "$logout_tokens" -lt 4 ]; then
	echo "Ory Hydra delivered ${logout_tokens} verified Back-Channel Logout tokens to Intraktible, want at least 4" >&2
	exit 1
fi
