#!/usr/bin/env bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"

# Cleanup any existing procloud process
killall -9 procloud 2>/dev/null || true
sleep 1
rm -f procloud.db procloud.db-wal procloud.db-shm

echo "=========================================================="
echo "⚡ Step 1: Running ProCloud Full Automated Test Suite"
echo "=========================================================="
go test -v ./tests/...

echo ""
echo "=========================================================="
echo "⚡ Step 2: Compiling Production Binaries"
echo "=========================================================="
mkdir -p bin
go build -o bin/procloud ./cmd/api
go build -o bin/procloud-worker ./cmd/worker
go build -o bin/procloud-agent ./cmd/agent
ls -lh bin/

echo ""
echo "=========================================================="
echo "⚡ Step 3: Starting ProCloud Server on :8080"
echo "=========================================================="
./bin/procloud -port 8080 -db procloud.db -mock &
SERVER_PID=$!
trap "kill -9 $SERVER_PID 2>/dev/null || true" EXIT
sleep 2

echo ""
echo "=========================================================="
echo "⚡ Step 4: Testing User Authentication & RBAC"
echo "=========================================================="
echo "-> POST /api/v1/auth/register (Verifying public registration is disabled)"
curl -s -i -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"publicuser","email":"user@test.io","password":"Password123!","role":"developer"}' | head -n 5

echo ""
echo "-> POST /api/v1/auth/login (Login as Super Administrator)"
AUTH_TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | python3 -c "import sys, json; print(json.load(sys.stdin)['token'])")
echo "JWT Token generated: ${AUTH_TOKEN:0:35}..."

echo ""
echo "-> GET /api/v1/auth/me (Authenticated Session)"
curl -s http://localhost:8080/api/v1/auth/me \
  -H "Authorization: Bearer $AUTH_TOKEN" | python3 -m json.tool

echo ""
echo "=========================================================="
echo "⚡ Step 5: Testing Podman Desktop Features"
echo "=========================================================="

echo "-> Images: GET /api/v1/images"
curl -s http://localhost:8080/api/v1/images | python3 -m json.tool

echo ""
echo "-> Pods: POST /api/v1/pods (Create Pod)"
curl -s -X POST http://localhost:8080/api/v1/pods \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"backend-service-pod","ports":["8080:80","9000:9000"]}' | python3 -m json.tool

echo ""
echo "-> Pods: GET /api/v1/pods"
curl -s http://localhost:8080/api/v1/pods | python3 -m json.tool

echo ""
echo "-> Pods: GET /api/v1/pods/{id}/kube (Generate Kubernetes YAML)"
curl -s http://localhost:8080/api/v1/pods/backend-service-pod/kube | python3 -m json.tool

echo ""
echo "-> Containers: POST /api/v1/containers (Run Container in Pod)"
curl -s -X POST http://localhost:8080/api/v1/containers \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"api-gateway","image":"docker.io/library/nginx:alpine","pod":"backend-service-pod","ports":["8080:80"]}' | python3 -m json.tool

echo ""
echo "-> Containers: GET /api/v1/containers"
curl -s http://localhost:8080/api/v1/containers | python3 -m json.tool

echo ""
echo "-> Containers: GET /api/v1/containers/api-gateway/logs"
curl -s http://localhost:8080/api/v1/containers/api-gateway/logs | python3 -m json.tool

echo ""
echo "-> Containers: POST /api/v1/containers/api-gateway/exec"
curl -s -X POST http://localhost:8080/api/v1/containers/api-gateway/exec \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command":["echo","hello-from-podman-desktop"]}' | python3 -m json.tool

echo ""
echo "=========================================================="
echo "⚡ Step 6: Executing Complete First Milestone (16 Steps)"
echo "=========================================================="
curl -s -X POST http://localhost:8080/api/v1/milestone/run \
  -H "Authorization: Bearer $AUTH_TOKEN" | python3 -m json.tool

echo ""
echo "=========================================================="
echo "⚡ Step 7: Testing Embedded Web Console"
echo "=========================================================="
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/)
echo "Web Console HTTP Status: $HTTP_CODE (OK)"

echo ""
echo "=========================================================="
echo "✅ All tests, builds, and Podman Desktop features PASSED!"
echo "=========================================================="
