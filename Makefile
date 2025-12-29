.PHONY: build clean docker docker-push push deploy run test fmt vet check

# Source directory
SRC_DIR=file-downloader
BINARY=$(SRC_DIR)/downloader

# Docker image
REGISTRY?=ghcr.io
IMAGE_NAME?=$(REGISTRY)/$(shell basename $(CURDIR))
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Umbrel SSH
UMBREL_HOST?=umbrel@192.168.2.104

# Build flags
LDFLAGS=-ldflags="-s -w -X main.Version=$(VERSION)"

# Default target
all: build

# Build binary
build:
	cd $(SRC_DIR) && go build $(LDFLAGS) -o downloader .

# Build for Linux (useful for Docker/Umbrel)
build-linux:
	cd $(SRC_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o downloader .

# Build for ARM64 (Raspberry Pi)
build-arm64:
	cd $(SRC_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o downloader .

# Run locally
run: build
	./$(BINARY) -web :8080

# Clean build artifacts
clean:
	rm -f $(BINARY)
	cd $(SRC_DIR) && go clean

# Build Docker image
docker:
	docker build -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest $(SRC_DIR)

# Build multi-arch Docker image
docker-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest $(SRC_DIR)

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

# Restart app on Umbrel (after adding community app store)
deploy:
	@echo "=== Updating app on Umbrel ($(UMBREL_HOST)) ==="
	@echo ""
	@echo "[1/2] Pulling latest image..."
	ssh -t $(UMBREL_HOST) "sudo docker pull ghcr.io/adamplansky/umbrel-downloader:latest"
	@echo ""
	@echo "[2/2] Restarting app..."
	ssh -t $(UMBREL_HOST) "sudo umbreld client apps.restart.mutate --appId adamplansky-file-downloader 2>/dev/null || echo 'App not installed yet'"
	@echo ""
	@echo "=== Done! ==="

# Push to GitHub (triggers Docker build via GitHub Actions)
release:
	git push origin main
	@echo ""
	@echo "Pushed to GitHub. Docker image will be built automatically."
	@echo "Once complete, run 'make deploy' to update Umbrel."

# Format code
fmt:
	cd $(SRC_DIR) && go fmt ./...

# Vet code
vet:
	cd $(SRC_DIR) && go vet ./...

# Run tests
test:
	cd $(SRC_DIR) && go test -v ./...

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
