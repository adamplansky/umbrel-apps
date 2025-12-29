.PHONY: build clean docker docker-push push pushtoumbrel deploy run test fmt vet check

# Binary name
BINARY=downloader
MODULE=umbrel-downloader

# Docker image
REGISTRY?=ghcr.io
IMAGE_NAME?=$(REGISTRY)/$(shell basename $(CURDIR))
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Umbrel SSH
UMBREL_HOST?=umbrel@192.168.2.104
UMBREL_APP_DIR=/home/umbrel/umbrel/app-stores/local-apps/file-downloader

# Build flags
LDFLAGS=-ldflags="-s -w -X main.Version=$(VERSION)"

# Default target
all: build

# Build binary
build:
	go build $(LDFLAGS) -o $(BINARY) .

# Build for Linux (useful for Docker/Umbrel)
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY) .

# Build for ARM64 (Raspberry Pi)
build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY) .

# Run locally
run: build
	./$(BINARY) -web :8080

# Clean build artifacts
clean:
	rm -f $(BINARY) $(MODULE)
	go clean

# Build Docker image
docker:
	docker build -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest .

# Build multi-arch Docker image
docker-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest .

# Push Docker image to registry
docker-push: docker
	docker push $(IMAGE_NAME):$(VERSION)
	docker push $(IMAGE_NAME):latest

# Git push
push:
	git push origin main

# Push with tags
push-tags:
	git push origin main --tags

# Install to local Umbrel instance (run on Umbrel)
pushtoumbrel:
	./install-local.sh

# Deploy to Umbrel via SSH - copies config and restarts app
# Image is built by GitHub Actions and pulled from ghcr.io
deploy:
	@echo "=== Deploying to Umbrel ($(UMBREL_HOST)) ==="
	@echo ""
	@echo "[1/3] Setting up app store..."
	ssh $(UMBREL_HOST) "mkdir -p $(UMBREL_APP_DIR)"
	ssh $(UMBREL_HOST) "test -f /home/umbrel/umbrel/app-stores/local-apps/umbrel-app-store.yml || echo -e 'id: local-apps\nname: Local Apps' > /home/umbrel/umbrel/app-stores/local-apps/umbrel-app-store.yml"
	scp umbrel-app-local/docker-compose.yml umbrel-app-local/umbrel-app.yml $(UMBREL_HOST):$(UMBREL_APP_DIR)/
	@echo ""
	@echo "[2/3] Pulling latest image..."
	ssh $(UMBREL_HOST) "docker pull ghcr.io/adamplansky/umbrel-downloader:latest"
	@echo ""
	@echo "[3/3] Restarting app..."
	ssh $(UMBREL_HOST) "cd ~/umbrel && sudo scripts/app restart local-apps-file-downloader 2>/dev/null || echo 'Not installed yet - install from App Store -> Local Apps'"
	@echo ""
	@echo "=== Done! ==="
	@echo "Downloads go to: /home/umbrel/umbrel/home/Downloads/movies/"

# Push to GitHub (triggers Docker build via GitHub Actions)
release:
	git push origin main
	@echo ""
	@echo "Pushed to GitHub. Docker image will be built automatically."
	@echo "Once complete, run 'make deploy' to update Umbrel."

# Format code
fmt:
	go fmt ./...

# Vet code
vet:
	go vet ./...

# Run tests
test:
	go test -v ./...

# Full check before commit
check: fmt vet test build

# Show help
help:
	@echo "Available targets:"
	@echo ""
	@echo "Development:"
	@echo "  build          - Build binary"
	@echo "  run            - Build and run with web UI on :8080"
	@echo "  clean          - Remove build artifacts"
	@echo "  fmt            - Format code"
	@echo "  vet            - Vet code"
	@echo "  test           - Run tests"
	@echo "  check          - Run fmt, vet, test, build"
	@echo ""
	@echo "Docker:"
	@echo "  docker         - Build Docker image locally"
	@echo "  docker-multiarch - Build multi-arch Docker image"
	@echo ""
	@echo "Umbrel deployment:"
	@echo "  release        - Push to GitHub (triggers image build)"
	@echo "  deploy         - Pull image & restart app on Umbrel ($(UMBREL_HOST))"
	@echo ""
	@echo "Workflow: make changes -> make release -> wait for build -> make deploy"
