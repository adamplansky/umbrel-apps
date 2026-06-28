# Adam's Umbrel App Store

A community app store for Umbrel OS.

## Install on Umbrel

1. Click **⋮** (three dots, top-right corner)
2. Select **Community App Stores**
3. Add: `https://github.com/adamplansky/umbrel-apps`
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
ssh umbrel@umbrel.home.arpa
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

**Updating the app on Umbrel:**

This app is published as `ghcr.io/adamplansky/umbrel-apps:latest`.

```bash
sudo docker pull ghcr.io/adamplansky/umbrel-apps:latest
sudo umbreld client apps.restart.mutate --appId adamplansky-file-downloader
```

If the UI still looks old after pulling the image, uninstall and reinstall the app from the Umbrel UI so Umbrel reloads the updated `docker-compose.yml` from this app store.

By default `make deploy` connects to `umbrel@umbrel.home.arpa`. If your Umbrel uses a different hostname or IP, override it:

```bash
make deploy UMBREL_HOST=umbrel@your-umbrel-hostname-or-ip
```

If you are already SSHed into Umbrel, run the Docker commands directly on Umbrel instead of `make deploy` from your workstation.

If `make deploy` appears stuck during the pull step, run the same steps manually to see whether SSH or `sudo` is waiting for input:

```bash
ssh -tt -o ConnectTimeout=10 umbrel@umbrel.home.arpa "echo connected"
ssh -tt -o ConnectTimeout=10 umbrel@umbrel.home.arpa "sudo -v"
ssh -tt -o ConnectTimeout=10 umbrel@umbrel.home.arpa "sudo docker pull ghcr.io/adamplansky/umbrel-apps:latest"
ssh -tt -o ConnectTimeout=10 umbrel@umbrel.home.arpa "sudo umbreld client apps.restart.mutate --appId adamplansky-file-downloader"
```

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
