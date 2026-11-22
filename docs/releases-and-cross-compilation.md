# Cross-Platform Releases & Compilation

ProCloud is designed with pure Go (`CGO_ENABLED=0`) and modern pure-Go SQLite (`modernc.org/sqlite`), enabling seamless cross-compilation across multiple operating systems and CPU architectures without complex toolchains.

---

## Supported Target Platforms

| Operating System | Architecture | Target Description | Archive Format |
|---|---|---|---|
| **macOS** | `arm64` | Apple Silicon (M1, M2, M3, M4) | `.tar.gz` and `.zip` |
| **macOS** | `amd64` | Intel x86_64 | `.tar.gz` and `.zip` |
| **Linux** | `amd64` | Standard x86_64 servers | `.tar.gz` and `.zip` |
| **Linux** | `arm64` | 64-bit ARM (AWS Graviton, Ampere, Raspberry Pi 4/5) | `.tar.gz` and `.zip` |

---

## Building Locally

### Standard Build
```bash
make build
# Output placed in bin/procloud
```

### Packaging Multi-Platform Release
To compile all 4 targets, generate tar.gz/zip archives, and calculate SHA-256 checksums:
```bash
make release
# Or pass a specific version tag:
./scripts/build-release.sh v1.0.0
```
Artifacts are generated in the `dist/` directory:
- `procloud_v1.0.0_darwin_arm64.tar.gz` & `.zip`
- `procloud_v1.0.0_darwin_amd64.tar.gz` & `.zip`
- `procloud_v1.0.0_linux_arm64.tar.gz` & `.zip`
- `procloud_v1.0.0_linux_amd64.tar.gz` & `.zip`
- `checksums.txt`

---

## Automated GitHub Actions Releases

ProCloud includes an automated release workflow in `.github/workflows/release.yml`:
- Triggered automatically whenever a tag matching `v*` (e.g. `v1.0.0`) is pushed to the repository.
- Cross-compiles all target binaries.
- Generates SHA-256 checksums.
- Publishes the official GitHub Release with release notes and binary assets.

---

## CLI Flags & Options

```bash
./bin/procloud [options]
```

- `-port <int>`: HTTP server listening port (default: `8080`)
- `-db <path>`: SQLite database path (default: `procloud.db`)
- `-mock`: Run with in-memory mock container runtime (for testing and CI)
- `-version`: Print ProCloud version, git commit SHA, and build date
