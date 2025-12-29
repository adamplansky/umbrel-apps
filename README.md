# Umbrel Downloader

A simple file downloader with web UI for Umbrel OS. Downloads files directly to your Jellyfin movies folder.

## Features

- Web UI with dark theme
- Multiple concurrent downloads
- Progress bars with download speed
- Download history
- Cancel downloads (auto-cleanup of partial files)

## Install on Umbrel

### 1. Add Community App Store

In Umbrel UI:
1. Click **⋮** (three dots, top-right corner)
2. Select **Community App Stores**
3. Add: `https://github.com/adamplansky/umbrel-downloader`
4. Click **Add**

### 2. Install the App

1. Go to **App Store**
2. Find **Adam's Apps** section
3. Click **File Downloader** → **Install**

## Updating

After making changes:

```bash
make release   # Push to GitHub, triggers Docker build
# Wait ~2 min for build to complete
make deploy    # Pull new image and restart app on Umbrel
```

Check build status: https://github.com/adamplansky/umbrel-downloader/actions

## Download Location

Files are downloaded to:
```
/home/umbrel/umbrel/home/Downloads/movies/
```

Same folder as Jellyfin - downloaded movies appear automatically.

## Local Development

```bash
make run       # Build and run on http://localhost:8080
```

## Commands

```bash
make help      # Show all commands
make build     # Build binary
make run       # Run locally on :8080
make release   # Push to GitHub (triggers Docker build)
make deploy    # Pull image & restart app on Umbrel
```
