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

A file and series downloader with web UI. Downloads files directly to your Jellyfin movies folder.

**Features:**
- Web UI with dark theme
- Manual URL downloads
- Series Search tab: TVmaze episode lookup + Webshare matching
- Per-episode candidate selection before downloading
- One-click download for all selected episodes
- Multiple concurrent downloads
- Progress bars with download speed
- Download history
- Cancel downloads (auto-cleanup of partial files)

**Download location:** `/home/umbrel/umbrel/home/Downloads/movies/`

**Webshare login for fast downloads:**

On Umbrel, SSH into the device and create a persistent env file in the app data folder:

```bash
ssh umbrel@umbrel.local
mkdir -p /home/umbrel/umbrel/app-data/adamplansky-file-downloader/data
nano /home/umbrel/umbrel/app-data/adamplansky-file-downloader/data/webshare.env
```

Put this in the file:

```env
WEBSHARE_USERNAME="your-email-or-username"
WEBSHARE_PASSWORD="your-password"
```

Then restart the app from Umbrel UI, or over SSH:

```bash
sudo umbreld client apps.restart.mutate --appId adamplansky-file-downloader
```

The app also supports `/data/.env` inside the container, which maps to:

```bash
/home/umbrel/umbrel/app-data/adamplansky-file-downloader/data/.env
```

Or use an existing Webshare session token instead of username/password:

```env
WEBSHARE_WST="your-webshare-token"
```

For local CLI/development runs, you can still export the same variables in your shell or place them in a local `.env` file.

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
├── csfdmeta/                         # TV series metadata CLI
├── webshare-search/                  # Webshare search/download URL CLI
├── series-linker/                    # CLI pipeline: episodes -> Webshare URLs
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
