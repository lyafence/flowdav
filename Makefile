BIN_DIR    := bin
RELEASE_DIR := release
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_FLAGS := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"
COMPOSE_FILE := docker-compose.yml

.PHONY: build test lint encrypt clean docker-build docker-e2e image-to-bin release \
        compose-down clean-images clean-all nuke check hooks android-aar android-apk

# Build binaries
build:
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-client ./cmd/client
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-server ./cmd/server
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-encrypt ./cmd/encrypt
build-all: build
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-client-linux-amd64 ./cmd/client
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-server-linux-amd64 ./cmd/server
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-encrypt-linux-amd64 ./cmd/encrypt

openwrt:
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-client-mipsle ./cmd/client
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-server-mipsle ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-encrypt-mipsle ./cmd/encrypt

# Run tests
test:
	go test -race -count=1 -timeout 120s ./...

test-short:
	go test -short -count=1 ./...

test-e2e:
	bash scripts/test_e2e.sh

test-e2e-encrypted:
	bash scripts/test_e2e.sh --encrypted

fuzz:
	go test -fuzztime=30s -fuzz FuzzEnvelopeUnmarshalBinary ./internal/transport/
	go test -fuzztime=30s -fuzz FuzzEnvelopeDecode ./internal/transport/
	go test -fuzztime=30s -fuzz FuzzDecodeEnvelopeWithCryptoNoCrypto ./internal/transport/

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

# Release archives (matches CI: ./github/workflows/release.yml)
release:
	$(MAKE) build
	rm -rf $(RELEASE_DIR)/*
	FOLDER=flowdav-$(VERSION)-linux-amd64; \
	mkdir -p $(RELEASE_DIR)/$$FOLDER; \
	cp $(BIN_DIR)/flowdav-client $(BIN_DIR)/flowdav-server $(BIN_DIR)/flowdav-encrypt \
		$(RELEASE_DIR)/$$FOLDER/; \
	cp README.md $(RELEASE_DIR)/$$FOLDER/; \
	cp configs/flowdav_client.json.example configs/flowdav_server.json.example \
		$(RELEASE_DIR)/$$FOLDER/; \
	cd $(RELEASE_DIR) && tar -czf $$FOLDER.tar.gz $$FOLDER && \
	rm -rf $$FOLDER

clean:
	rm -rf $(BIN_DIR) $(RELEASE_DIR)
	rm -f configs/flowdav_test_*.json configs/.env
	rm -f android/app/libs/flowdav.aar

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

# Android AAR via gomobile (requires NDK + gomobile)
#   ANDROID_HOME must be set. Install gomobile: go install golang.org/x/mobile/cmd/gomobile@latest
#   Then: gomobile init
android-aar:
	go get golang.org/x/mobile@latest
	gomobile bind -target=android -androidapi 21 -javapkg com.flowdav.app \
		-o android/app/libs/flowdav.aar \
		-trimpath -ldflags="-s -w" \
		./cmd/android

# Android APK (AAR + Gradle assemble)
# Requires: ANDROID_HOME + OpenJDK 17+
# Builds debug APK (auto-signed). For release APK, set up signing key in build.gradle.kts.
android-apk: android-aar
	cd android && ./gradlew assembleDebug --no-daemon
	@echo "APK: android/app/build/outputs/apk/debug/app-debug.apk"

# Install pre-commit hooks
check: vet lint build test

hooks:
	@cp .githooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Installed .githooks/pre-commit -> .git/hooks/pre-commit"

# Alias
clean-all: nuke


.DEFAULT_GOAL := build
