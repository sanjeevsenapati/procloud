#!/usr/bin/env bash
set -e

PORT=${1:-8080}
DB_PATH=${2:-"procloud.db"}

echo "=========================================================="
echo " Starting ProCloud Sovereign Cloud Control Plane"
echo " Port:     $PORT"
echo " Database: $DB_PATH"
echo " Web UI:   http://localhost:$PORT"
echo "=========================================================="

# Build latest binary
go build -o bin/procloud ./cmd/api

# Run binary with mock/native auto-detection
./bin/procloud -port "$PORT" -db "$DB_PATH"
