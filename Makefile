BIN_DIR    := bin
RELEASE_DIR := release
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_FLAGS := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"
COMPOSE_FILE := docker-compose.yml
HOST_IP := $(shell hostname -I 2>/dev/null | awk '{print $$1}' || ip route get 1 | awk '{print $$NF;exit}' || echo "localhost")

.PHONY: build build-linux openwrt test test-short test-e2e test-e2e-encrypted fuzz \
            lint vet tidy encrypt clean docker-build docker-e2e image-to-bin release \
            compose-down clean-images clean-all nuke check hooks android-init android-aar android-apk android-keystore \
            compose-android compose-stop android-deploy

# Build binaries
build:
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-client ./cmd/client
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-server ./cmd/server
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-encrypt ./cmd/encrypt

build-linux: build
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

test-e2e: docker-build
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

tidy:
	go mod tidy -e

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
	@trap "podman rm $(CID) >/dev/null 2>&1 || true" EXIT; \
	podman cp $(CID):/usr/local/bin/flowdav-client $(BIN_DIR)/flowdav-client && \
	podman cp $(CID):/usr/local/bin/flowdav-server $(BIN_DIR)/flowdav-server && \
	podman cp $(CID):/usr/local/bin/flowdav-encrypt $(BIN_DIR)/flowdav-encrypt && \
	podman rm $(CID) && \
	chmod +x $(BIN_DIR)/flowdav-client $(BIN_DIR)/flowdav-server $(BIN_DIR)/flowdav-encrypt && \
	echo "Extracted binaries from image to $(BIN_DIR)/"

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
	rm -rf android/app/build android/.gradle

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

# Quick Android test env: single WebDAV + flowdav-server
compose-android: docker-build
	podman-compose -f $(COMPOSE_FILE) down -v 2>/dev/null || true
	podman-compose -f $(COMPOSE_FILE) up -d webdav-single flow-server
	@echo ""
	@echo "=== Android test environment ==="
	@echo "WebDAV:   http://$(HOST_IP):8080 (user: test, pass: test)"
	@echo "APK:      bin/flowdav-android.apk"
	@echo "Deploy:   make android-deploy"
	@echo ""

compose-stop:
	podman-compose -f $(COMPOSE_FILE) down -v

# Build + deploy APK and config to Android device (USB/WiFi/emulator)
android-deploy: compose-android android-apk
	@if [ "$(HOST_IP)" = "localhost" ]; then \
		echo "Cannot detect host IP. Set HOST_IP manually: make android-deploy HOST_IP=192.168.x.x"; exit 1; \
	fi; \
	echo '{"storage_type":"webdav","webdav":{"url":"http://$(HOST_IP):8080","login":"test","token":"test"},"listen_addr":"127.0.0.1:1080","enc_key":"LNhqOtYLNlyjITlHEqgg8XErz1g7bVXId5COVR5cAgY=","hmac_key":"N5woOb7wjtlUaj2D0ZPhS7Lynm+xUd4Jr/2hhfoLung=","log_level":"info"}' | \
		FLOWDAV_PASSWORD=secret go run ./cmd/encrypt > /tmp/flowdav-config.enc 2>/dev/null; \
	ADB=$$(which adb 2>/dev/null || true); \
	if [ -n "$$ADB" ] && $$ADB get-state 2>/dev/null | grep -q device; then \
		$$ADB install -r $(BIN_DIR)/flowdav-android.apk && \
		$$ADB push /tmp/flowdav-config.enc /sdcard/Download/flowdav-config.enc && \
		rm -f /tmp/flowdav-config.enc && \
		echo "" && \
		echo "=== Deployed via adb ==="; \
	else \
		echo ""; \
		echo "=== Ready (no adb device found) ==="; \
		echo "APK:  $(BIN_DIR)/flowdav-android.apk"; \
		echo "Config: /tmp/flowdav-config.enc (password: secret)"; \
		echo ""; \
		echo "Manual install:"; \
		echo "  adb install -r $(BIN_DIR)/flowdav-android.apk"; \
		echo "  adb push /tmp/flowdav-config.enc /sdcard/Download/flowdav-config.enc"; \
	fi; \
	echo "WebDAV: http://$(HOST_IP):8080 (user: test, pass: test)"

# Android AAR via gomobile (requires NDK + gomobile)
#   ANDROID_HOME must be set. Install gomobile: go install golang.org/x/mobile/cmd/gomobile@latest
#   Then: gomobile init
ANDROID_SDK_HOME ?= $(HOME)/.android-sdk
SDK_TOOLS_URL := https://dl.google.com/android/repository/commandlinetools-linux-14742923_latest.zip

android-init:
	@echo "=== Installing Android SDK + NDK (Linux) ==="
	@cd /tmp && \
		curl -sLo cmdline-tools.zip "$(SDK_TOOLS_URL)" && \
		unzip -q cmdline-tools.zip && \
		mkdir -p $(ANDROID_SDK_HOME)/cmdline-tools && \
		mv cmdline-tools $(ANDROID_SDK_HOME)/cmdline-tools/latest && \
		yes | $(ANDROID_SDK_HOME)/cmdline-tools/latest/bin/sdkmanager --sdk_root=$(ANDROID_SDK_HOME) \
			"platforms;android-36" "build-tools;36.0.0" "ndk;27.0.12077973" && \
		rm -f /tmp/cmdline-tools.zip && \
		ln -sf $(ANDROID_SDK_HOME)/ndk/27.0.12077973 $(ANDROID_SDK_HOME)/ndk-bundle && \
		go install golang.org/x/mobile/cmd/gomobile@latest && \
		gomobile init && \
		echo "" && \
		echo "=== DONE ===" && \
		echo "Add to ~/.bashrc:" && \
		echo '  export ANDROID_HOME=$(ANDROID_SDK_HOME)' && \
		echo '  export PATH="$$ANDROID_HOME/cmdline-tools/latest/bin:$$PATH"' && \
		echo "  export PATH=\"$$HOME/go/bin:$$PATH\""

android-aar:
	mkdir -p android/app/libs
	ANDROID_HOME=$(ANDROID_SDK_HOME) PATH="$(HOME)/go/bin:$(PATH)" \
	gomobile bind -target=android -androidapi 26 -javapkg com.flowdav.app \
		-o android/app/libs/flowdav.aar \
		-trimpath -ldflags="-s -w" \
		./cmd/android

# Android APK (AAR + Gradle assemble debug)
# Requires: ANDROID_HOME + OpenJDK 17+
android-apk: android-aar
	cd android && ANDROID_HOME=$(ANDROID_SDK_HOME) ./gradlew assembleDebug --no-daemon
	@cp android/app/build/outputs/apk/debug/app-debug.apk bin/flowdav-android.apk
	@echo "APK: bin/flowdav-android.apk"

# Generate Android release keystore and print GitHub Secrets
android-keystore:
	bash scripts/gen-keystore.sh

# Install pre-commit hooks
check: vet lint build test

hooks:
	@cp .githooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Installed .githooks/pre-commit -> .git/hooks/pre-commit"

# Alias
clean-all: nuke


.DEFAULT_GOAL := build
