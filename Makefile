BIN_DIR    := bin
RELEASE_DIR := release
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_FLAGS := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"
COMPOSE_FILE := docker-compose.yml

.PHONY: build test lint encrypt clean docker-build docker-e2e image-to-bin release \
        compose-down clean-images clean-all nuke

# Build binaries
build:
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-client ./cmd/client
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-server ./cmd/server
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-encrypt ./cmd/encrypt
build-all: build
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-client-linux-amd64 ./cmd/client
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-server-linux-amd64 ./cmd/server
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-encrypt-linux-amd64 ./cmd/encrypt

# Run tests
test:
	go test -race -count=1 -timeout 120s ./...

test-short:
	go test -short -count=1 ./...

test-e2e:
	bash scripts/test_e2e.sh

test-e2e-encrypted:
	bash scripts/test_e2e.sh --encrypted

# Static analysis
lint:
	golangci-lint run ./...

vet:
	go vet ./...

# Encrypt config
# Usage: make encrypt FILE=config.json          # prompts for password
# Usage: FLOWDAV_PASSWORD=secret make encrypt    # env var
encrypt:
	@if [ -z "$${FLOWDAV_PASSWORD}" ]; then \
		read -s -p "Master password: " FLOWDAV_PASSWORD; echo; \
	fi; \
	F="$(FILE)"; \
	if [ -z "$$F" ]; then F="config.json"; fi; \
	go run ./cmd/encrypt --gen-keys < "$$F" > "$${F}.enc"; \
	echo "Encrypted: $$F.enc (chmod 600)"

# Container
docker-build:
	podman build -t localhost/flowdav:latest .

docker-e2e: docker-build
	bash scripts/test_e2e.sh

# Build image and extract binaries to host
image-to-bin: docker-build
	$(eval CID := $(shell podman create localhost/flowdav:latest))
	podman cp $(CID):/usr/local/bin/flowdav-client $(BIN_DIR)/flowdav-client
	podman cp $(CID):/usr/local/bin/flowdav-server $(BIN_DIR)/flowdav-server
	podman cp $(CID):/usr/local/bin/flowdav-encrypt $(BIN_DIR)/flowdav-encrypt
	podman rm $(CID)
	chmod +x $(BIN_DIR)/flowdav-client $(BIN_DIR)/flowdav-server $(BIN_DIR)/flowdav-encrypt
	@echo "Extracted binaries from image to $(BIN_DIR)/"

# Release archives
release:
	mkdir -p $(RELEASE_DIR)
	rm -rf $(RELEASE_DIR)/*
	$(MAKE) build-all
	tar -czf $(RELEASE_DIR)/flowdav-$(VERSION)-linux-amd64.tar.gz \
		-C $(BIN_DIR) flowdav-client-linux-amd64 flowdav-server-linux-amd64 \
		-C $(CURDIR) README.md configs/flowdav_client.json.example configs/flowdav_server.json.example

clean:
	rm -rf $(BIN_DIR) $(RELEASE_DIR)
	rm -f configs/flowdav_test_*.json configs/.env

# Podman Compose lifecycle
compose-down:
	podman-compose -f $(COMPOSE_FILE) down -v

clean-images:
	podman rmi -f localhost/flowdav:latest 2>/dev/null || true
	podman image prune -f 2>/dev/null || true

# Full environment reset
nuke: compose-down clean-images clean
	podman system prune -f 2>/dev/null || true
	@echo "Environment reset complete. Run 'make docker-build && make docker-e2e' to rebuild."

# Alias
clean-all: nuke


.DEFAULT_GOAL := build
