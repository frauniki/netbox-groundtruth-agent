#!/usr/bin/env bash
# Runs the integration test against a throwaway NetBox started with
# netbox-docker. Needs docker compose, git, go and access to Docker Hub.
#
#   ./test/integration/run.sh                 # NetBox version pinned by netbox-docker main
#   NETBOX_DOCKER_REF=3.0.2 VERSION=v4.1-3.0.2 ./test/integration/run.sh   # another release
#   KEEP=1 ./test/integration/run.sh          # leave NetBox running afterwards
#
# VERSION is netbox-docker's image tag variable; pick a NETBOX_DOCKER_REF
# (netbox-docker release) whose compose file matches that image.
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
port=${PORT:-18000}
project=groundtruth-it
ref=${NETBOX_DOCKER_REF:-}
# netbox-docker's compose file uses ${VERSION-default}: an empty value must be unset.
[[ -z ${VERSION:-} ]] && unset VERSION

cleanup() {
  if [[ -z ${KEEP:-} ]]; then
    (cd "$work/netbox-docker" && docker compose -p "$project" down -v >/dev/null 2>&1) || true
    rm -rf "$work"
  else
    echo "NetBox left running at http://127.0.0.1:$port (compose project $project in $work/netbox-docker)"
  fi
}
trap cleanup EXIT

git clone -q --depth 1 ${ref:+--branch "$ref"} https://github.com/netbox-community/netbox-docker.git "$work/netbox-docker"
cd "$work/netbox-docker"

# Random credentials for this throwaway instance. NetBox 4.5+ (netbox-docker
# with API_TOKEN_PEPPER_*) uses v2 tokens: nbt_<12-char key>.<40-char token>;
# older images take a 40-character v1 token in SUPERUSER_API_TOKEN.
# pipefail off: tr ends with SIGPIPE when head has read enough.
rand() { (set +o pipefail; LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c "$1"); }
key=$(rand 12)
secret=$(rand 40)
cat > docker-compose.override.yml <<YAML
services:
  netbox:
    ports:
      - "127.0.0.1:$port:8080"
    healthcheck:
      start_period: 600s # the first start runs all database migrations
    environment:
      SKIP_SUPERUSER: "false"
      SUPERUSER_NAME: admin
      SUPERUSER_EMAIL: admin@example.com
      SUPERUSER_PASSWORD: "$(rand 24)"
      SUPERUSER_API_KEY: "$key"
      SUPERUSER_API_TOKEN: "$secret"
YAML

# "up" can fail while NetBox is still migrating (the worker waits for it to
# be healthy); readiness is checked below instead.
docker compose -p "$project" up -d --quiet-pull || true
echo "waiting for NetBox on port $port ..."
ready=
for _ in $(seq 120); do
  if curl -fsS "http://127.0.0.1:$port/login/" >/dev/null 2>&1; then ready=1; break; fi
  sleep 5
done
if [[ -z $ready ]]; then
  docker compose -p "$project" ps
  docker compose -p "$project" logs --tail 200 netbox
  echo "NetBox did not become ready" >&2
  exit 1
fi

export NETBOX_URL="http://127.0.0.1:$port"
if grep -q API_TOKEN_PEPPER env/netbox.env; then
  export NETBOX_TOKEN="nbt_$key.$secret"
else
  export NETBOX_TOKEN="$secret"
fi

cd "$repo"
go test -tags integration -count=1 -v ./test/integration/
