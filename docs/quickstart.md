# ProCloud Quickstart Guide

Get up and running with ProCloud in under 2 minutes.

## Prerequisites
- Linux (RHEL/Rocky/Alma/Fedora, Ubuntu/Debian) or macOS with Podman machine.
- Podman >= 4.4 installed (`podman --version`).
- Go >= 1.20 (if compiling from source).

## Installation

```bash
git clone https://github.com/sanjeevsenapati/procloud.git
cd procloud
go build -o bin/procloud ./cmd/api
```

## Running the Server

```bash
./bin/procloud -port 8080 -db procloud.db
```

Access the ProGit White & Light Blue web console at: `http://localhost:8080`.
Default credentials: `admin` / `admin123`.
