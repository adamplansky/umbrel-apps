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
- SSH key configured (or you'll be prompted for password)

### One-command deploy

```bash
make deploy
```

This will:
1. Copy source files to Umbrel
2. Build Docker image on the device
3. Install the app to local app store
4. Restart the app

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

## Other Commands

```bash
make help      # Show all available commands
make build     # Build binary
make docker    # Build Docker image locally
make clean     # Remove build artifacts
```
