# Umbrel Downloader

A simple file downloader with web UI for Umbrel OS. Downloads files directly to your Jellyfin movies folder.

## Features

- Web UI with dark theme
- Multiple concurrent downloads
- Progress bars with download speed
- Download history
- Cancel downloads (auto-cleanup of partial files)

## Deploy to Umbrel

### Prerequisites

- SSH access to your Umbrel device
- GitHub repository with Actions enabled

### Workflow

```bash
# 1. Make your changes, then push to GitHub
make release

# 2. Wait for GitHub Actions to build the image (~2 min)
#    Check: https://github.com/adamplansky/umbrel-downloader/actions

# 3. Deploy to Umbrel
make deploy
```

**First time?** After `make deploy`, go to Umbrel App Store → Local Apps → Install "File Downloader"

### Configuration

Edit `Makefile` to change your Umbrel host:
```makefile
UMBREL_HOST?=umbrel@192.168.2.104
```

Or override on command line:
```bash
make deploy UMBREL_HOST=umbrel@your-ip
```

## Download Location

Files are downloaded to:
```
/home/umbrel/umbrel/home/Downloads/movies/
```

This is the same folder used by Jellyfin, so downloaded movies appear automatically.

## Local Development

```bash
# Build and run locally
make run

# Open http://localhost:8080
```

## Commands

```bash
make help      # Show all available commands
make build     # Build binary
make run       # Run locally on :8080
make release   # Push to GitHub (triggers Docker build)
make deploy    # Deploy to Umbrel
```
