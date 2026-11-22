#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# ProCloud Cross-Platform Release Packaging Script
# Targets: macOS (arm64, amd64) and Linux (arm64, amd64)
# ==============================================================================

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

TAG="${1:-v1.0.0}"
VERSION="${TAG#v}"
DIST_DIR="${ROOT_DIR}/dist"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X 'main.Version=${VERSION}' -X 'main.GitCommit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'"

echo "=========================================================="
echo " Packaging ProCloud Release ${TAG}"
echo " Commit: ${COMMIT} | Date: ${BUILD_DATE}"
echo "=========================================================="

rm -rf "${DIST_DIR}"
mkdir -p "${DIST_DIR}"

PLATFORMS=(
  "darwin/arm64:macOS Apple Silicon (M1/M2/M3/M4)"
  "darwin/amd64:macOS Intel x86_64"
  "linux/amd64:Linux x86_64"
  "linux/arm64:Linux aarch64"
)

for ENTRY in "${PLATFORMS[@]}"; do
  PLATFORM="${ENTRY%%:*}"
  DESC="${ENTRY#*:}"
  GOOS="${PLATFORM%/*}"
  GOARCH="${PLATFORM#*/}"
  
  ARCHIVE_NAME="procloud_${TAG}_${GOOS}_${GOARCH}"
  TARGET_DIR="${DIST_DIR}/${ARCHIVE_NAME}"
  mkdir -p "${TARGET_DIR}"

  echo "--> Building ${DESC} (${GOOS}/${GOARCH})..."
  CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" go build -ldflags "${LDFLAGS}" -o "${TARGET_DIR}/procloud" ./cmd/api

  # Copy documentation and licensing
  [ -f README.md ] && cp README.md "${TARGET_DIR}/"
  [ -f LICENSE ] && cp LICENSE "${TARGET_DIR}/"

  # Create tar.gz archive
  (cd "${DIST_DIR}" && tar -czvf "${ARCHIVE_NAME}.tar.gz" "${ARCHIVE_NAME}")
  
  # Create zip archive for macOS/Windows convenience
  (cd "${DIST_DIR}" && zip -rq "${ARCHIVE_NAME}.zip" "${ARCHIVE_NAME}")

  rm -rf "${TARGET_DIR}"
  echo "    Packaged: ${ARCHIVE_NAME}.tar.gz and .zip"
done

echo "--> Generating SHA-256 Checksums..."
(
  cd "${DIST_DIR}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum procloud_* > checksums.txt
  else
    shasum -a 256 procloud_* > checksums.txt
  fi
)

echo "=========================================================="
echo " Successfully built release artifacts in ${DIST_DIR}:"
ls -lh "${DIST_DIR}"
echo "=========================================================="
