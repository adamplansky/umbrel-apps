# Adam's Umbrel App Store

A community app store for Umbrel OS.

## Install on Umbrel

1. Click **⋮** (three dots, top-right corner)
2. Select **Community App Stores**
3. Add: `https://github.com/adamplansky/umbrel-downloader`
4. Click **Add**

Then go to **App Store** → **Adam's Apps** to install apps.

---

## Apps

### File Downloader

A simple file downloader with web UI. Downloads files directly to your Jellyfin movies folder.

**Features:**
- Web UI with dark theme
- Multiple concurrent downloads
- Progress bars with download speed
- Download history
- Cancel downloads (auto-cleanup of partial files)

**Download location:** `/home/umbrel/umbrel/home/Downloads/movies/`

---

### Fizzy

Kanban as it should be. Not as it has been.

**Features:**
- Fresh, modern Kanban board from 37signals (makers of Basecamp and HEY)
- Colorful UI with smooth drag-and-drop
- Auto-closing for inactive cards
- Privacy-first project management
- Perfect for personal use or small teams

**Source:** [github.com/basecamp/fizzy](https://github.com/basecamp/fizzy)

---

## Repository Structure

```
├── umbrel-app-store.yml              # App store manifest
├── adamplansky-file-downloader/      # File Downloader app
│   ├── umbrel-app.yml
│   ├── docker-compose.yml
│   └── images/icon.svg
├── adamplansky-fizzy/                # Fizzy Kanban app
│   ├── umbrel-app.yml
│   ├── docker-compose.yml
│   └── images/icon.svg
├── file-downloader/                  # Source code (File Downloader)
│   ├── main.go
│   ├── go.mod
│   └── Dockerfile
└── Makefile
```

## Development

```bash
make build     # Build binary
make run       # Run locally on http://localhost:8080
make release   # Push to GitHub (triggers Docker build)
make deploy    # Pull image & restart app on Umbrel
make help      # Show all commands
```

## Adding a New App

1. Create app folder: `adamplansky-<app-name>/`
2. Add `umbrel-app.yml`, `docker-compose.yml`, `images/icon.svg`
3. Create source folder: `<app-name>/` with code and Dockerfile
4. Update GitHub Actions if needed
5. `make release` to push
