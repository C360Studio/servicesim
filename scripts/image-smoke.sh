#!/usr/bin/env bash
#
# Image smoke test.
#
# Turns the container-facing acceptance criteria into assertions instead of
# claims. Every check here maps to a numbered criterion in
# docs/architecture-and-implementation-plan.md:
#
#   2  each provider has its own listener on its real path
#   6  credentials are redacted in the journal
#  10  unmatched traffic fails closed
#  12  the image runs as a non-root user
#
# All assertions run from outside the container: the runtime stage is
# `scratch`, so there is no shell to exec into.
#
# Usage: scripts/image-smoke.sh <image-ref>

set -euo pipefail

IMAGE="${1:?usage: image-smoke.sh <image-ref>}"
NAME="servicesim-smoke-$$"

# Bind to ephemeral host ports so this is safe to run in parallel and on a
# developer machine that already has something on 8080.
ADMIN_PORT=$(( 20000 + RANDOM % 20000 ))
EXA_PORT=$(( ADMIN_PORT + 1 ))
TAVILY_PORT=$(( ADMIN_PORT + 2 ))
PERPLEXITY_PORT=$(( ADMIN_PORT + 3 ))

failures=0

cleanup() {
  if [ "${failures}" -ne 0 ]; then
    echo "--- container logs ---"
    docker logs "${NAME}" 2>&1 | tail -50 || true
  fi
  docker rm -f "${NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

pass() { echo "  ok    $1"; }
fail() { echo "  FAIL  $1"; failures=$((failures + 1)); }

check() {
  local desc="$1" actual="$2" expected="$3"
  if [ "${actual}" = "${expected}" ]; then
    pass "${desc}"
  else
    fail "${desc} (expected '${expected}', got '${actual}')"
  fi
}

echo "==> starting ${IMAGE}"
docker run -d --name "${NAME}" \
  -p "${ADMIN_PORT}:8080" \
  -p "${EXA_PORT}:8081" \
  -p "${TAVILY_PORT}:8082" \
  -p "${PERPLEXITY_PORT}:8083" \
  "${IMAGE}" >/dev/null

# Wait for readiness rather than sleeping a fixed interval. Readiness only
# succeeds once the startup scenario has loaded and validated, so this also
# exercises acceptance criterion 4.
echo "==> waiting for readiness"
ready=0
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${ADMIN_PORT}/readyz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if [ -z "$(docker ps -q -f name="${NAME}")" ]; then
    echo "FAIL: container exited during startup"
    docker logs "${NAME}" 2>&1 | tail -50
    exit 1
  fi
  sleep 0.5
done
[ "${ready}" -eq 1 ] || { echo "FAIL: /readyz never succeeded"; failures=1; exit 1; }

echo "==> criterion 12: runs as a non-root user"
declared_user=$(docker inspect --format '{{.Config.User}}' "${IMAGE}")
check "image declares a non-root USER" "${declared_user}" "65532:65532"

# Config.User could be overridden at run time, so assert on the running process
# too. `docker top` reads the host's process table and needs no in-container shell.
#
# Deliberately NOT `docker top ... -o uid`: passing ps options fails on Docker
# Desktop's Linux VM with "Couldn't find PID field in ps output". The default
# invocation returns a UID column as field 1, which is all this needs.
runtime_uid=$(docker top "${NAME}" 2>/dev/null | awk 'NR==2 {print $1}')
if [ "${runtime_uid}" = "0" ] || [ "${runtime_uid}" = "root" ]; then
  fail "running process is root (uid=${runtime_uid})"
else
  pass "running process is non-root (uid=${runtime_uid})"
fi

echo "==> health and readiness"
check "GET /healthz" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${ADMIN_PORT}/healthz")" "200"
check "GET /readyz" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${ADMIN_PORT}/readyz")" "200"

echo "==> docker HEALTHCHECK reports healthy"
health=""
for _ in $(seq 1 40); do
  health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "${NAME}")
  [ "${health}" = "healthy" ] && break
  sleep 1
done
check "container health status" "${health}" "healthy"

echo "==> criterion 2: each provider answers on its own listener and real path"
check "POST :8081/search (exa)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${EXA_PORT}/search" \
      -H 'content-type: application/json' -H 'x-api-key: smoke-test-key' \
      -d '{"query":"smoke","numResults":1}')" "200"

check "POST :8082/search (tavily)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${TAVILY_PORT}/search" \
      -H 'content-type: application/json' -H 'authorization: Bearer smoke-test-key' \
      -d '{"query":"smoke","max_results":1}')" "200"

check "POST :8081/answer (exa)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${EXA_PORT}/answer" \
      -H 'content-type: application/json' -H 'x-api-key: smoke-test-key' \
      -d '{"query":"smoke"}')" "200"

# Phase 4's three synchronous routes. D-a means /contents and /extract need no
# scenario authoring: happy's /search results are corpus URLs, so naming one
# resolves through the corpus fallback with no `contents:`/`extract:` block.
check "POST :8081/contents (exa)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${EXA_PORT}/contents" \
      -H 'content-type: application/json' -H 'x-api-key: smoke-test-key' \
      -d '{"urls":["https://example.test/report-a"]}')" "200"

check "POST :8081/findSimilar (exa)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${EXA_PORT}/findSimilar" \
      -H 'content-type: application/json' -H 'x-api-key: smoke-test-key' \
      -d '{"url":"https://example.test/report-a"}')" "200"

check "POST :8082/extract (tavily)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${TAVILY_PORT}/extract" \
      -H 'content-type: application/json' -H 'authorization: Bearer smoke-test-key' \
      -d '{"urls":["https://example.test/report-a"]}')" "200"

# The async create routes. The image's default scenario is `happy`, which
# declares both async entries (scenarios/protocol/happy.yaml), so a create
# against either one mints a job and answers 201.
check "POST :8081/agent/runs (exa, async create)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${EXA_PORT}/agent/runs" \
      -H 'content-type: application/json' -H 'x-api-key: smoke-test-key' \
      -d '{"query":"smoke"}')" "201"

check "POST :8082/research (tavily, async create)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${TAVILY_PORT}/research" \
      -H 'content-type: application/json' -H 'authorization: Bearer smoke-test-key' \
      -d '{"input":"smoke"}')" "201"

# All four Perplexity routes: the two Sonar paths (canonical + OpenAI SDK
# alias) and the two Agent API paths. The aliases are not in the vendor's
# OpenAPI document but consumers reach them through the OpenAI SDK, so a
# regression there would be invisible to any spec-driven test.
sonar_body='{"model":"sonar","messages":[{"role":"user","content":"smoke"}]}'
agent_body='{"input":"smoke"}'

check "POST :8083/v1/sonar (perplexity, canonical)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${PERPLEXITY_PORT}/v1/sonar" \
      -H 'content-type: application/json' -H 'authorization: Bearer smoke-test-key' -d "${sonar_body}")" "200"

check "POST :8083/chat/completions (perplexity, SDK alias)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${PERPLEXITY_PORT}/chat/completions" \
      -H 'content-type: application/json' -H 'authorization: Bearer smoke-test-key' -d "${sonar_body}")" "200"

check "POST :8083/v1/agent (perplexity, agent)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${PERPLEXITY_PORT}/v1/agent" \
      -H 'content-type: application/json' -H 'authorization: Bearer smoke-test-key' -d "${agent_body}")" "200"

check "POST :8083/v1/responses (perplexity, agent SDK alias)" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${PERPLEXITY_PORT}/v1/responses" \
      -H 'content-type: application/json' -H 'authorization: Bearer smoke-test-key' -d "${agent_body}")" "200"

echo "==> criterion 6: authentication is validated, not merely accepted"
check "missing credential is 401" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${TAVILY_PORT}/search" \
      -H 'content-type: application/json' -d '{"query":"smoke"}')" "401"

echo "==> criterion 10: unmatched traffic fails closed"
check "unknown path on a provider listener is 404" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${EXA_PORT}/not-a-real-route" -d '{}')" "404"
check "wrong method on a real path is 405" \
  "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${EXA_PORT}/search")" "405"

echo "==> criterion 6: the journal records the calls and redacts credentials"
journal=$(curl -s "http://127.0.0.1:${ADMIN_PORT}/__admin/requests")

if printf '%s' "${journal}" | grep -q 'smoke-test-key'; then
  fail "journal leaked the API key"
else
  pass "journal contains no credential value"
fi

for provider in exa tavily perplexity; do
  if printf '%s' "${journal}" | grep -q "\"provider\":\"${provider}\""; then
    pass "journal recorded a ${provider} request"
  else
    fail "journal has no ${provider} entry"
  fi
done

echo
if [ "${failures}" -ne 0 ]; then
  echo "SMOKE TEST FAILED: ${failures} check(s)"
  exit 1
fi
echo "SMOKE TEST PASSED"
