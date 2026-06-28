# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a community app store for Umbrel OS. It contains app manifests (umbrel-app.yml + docker-compose.yml) that Umbrel fetches to install apps, plus source code for custom apps.

## Build & Development Commands

All commands run from repository root:

```bash
make build         # Build Go binary (file-downloader)
make run           # Build and run locally on http://localhost:8080
make check         # Run fmt, vet, test, and build (pre-commit check)
make test          # Run tests only
make fmt           # Format Go code
make vet           # Vet Go code
make clean         # Remove build artifacts
```

Cross-compilation:
```bash
make build-linux   # Linux x86_64
make build-arm64   # ARM64 (Raspberry Pi)
```

## Deployment Workflow

```bash
make release       # Push to GitHub → triggers GitHub Actions Docker build
make deploy        # SSH to Umbrel, pull latest image, restart app
```

The deploy target SSHs to `UMBREL_HOST` (default: `umbrel@192.168.2.104`).

## Architecture

### App Store Structure

Each app requires a folder `adamplansky-<app-name>/` containing:
- `umbrel-app.yml` - App manifest (name, description, port, icon URL, etc.)
- `docker-compose.yml` - Container configuration with `app_proxy` service
- `images/icon.svg` - App icon

### Key Umbrel Integration Points

- `app_proxy` service in docker-compose.yml handles reverse proxy routing
- `${APP_DATA_DIR}` environment variable points to persistent app storage
- Port specified in umbrel-app.yml is the external port users access

### Source Code Location

Custom app source code lives in separate folders (e.g., `file-downloader/`) with their own Dockerfile. Docker images are built by GitHub Actions and pushed to ghcr.io.

### Manifest Format

Uses manifestVersion 1. Required fields:
- `id`: Must match folder name (e.g., `adamplansky-file-downloader`)
- `category`: One of Umbrel's categories (files, social, etc.)
- `port`: External access port
- `icon`: Full URL to hosted icon (use GitHub raw content URLs)

## CI/CD

GitHub Actions (`.github/workflows/docker-build.yml`) builds multi-arch Docker images (amd64 + arm64) on push to main or version tags (v*). Images push to GitHub Container Registry (ghcr.io).
