BIN_DIR    := bin
RELEASE_DIR := release
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_FLAGS := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"
COMPOSE_FILE := docker-compose.yml
HOST_IP := $(shell ip route get 1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($$i=="src") print $$(i+1)}' || hostname -I 2>/dev/null | awk '{print $$1}' || echo "localhost")

.PHONY: build openwrt test test-short test-e2e test-e2e-encrypted fuzz \
            lint vet tidy encrypt clean docker-build docker-e2e image-to-bin release \
            compose-down clean-images nuke check hooks android-init android-aar android-apk android-keystore \
            compose-android android-deploy

# Build binary
build:
	mkdir -p $(BIN_DIR)
	go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav ./cmd/flowdav

openwrt:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build $(BUILD_FLAGS) -o $(BIN_DIR)/flowdav-mipsle ./cmd/flowdav

# Run tests
test:
	go test -race -count=1 -timeout 120s ./...

test-short:
	go test -short -count=1 ./...

test-e2e: docker-build
	bash scripts/test_e2e.sh

test-e2e-encrypted: docker-build
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
	@F="$(FILE)"; \
	if [ -z "$$F" ]; then F="config.json"; fi; \
	FLOWDAV_PASSWORD="$${FLOWDAV_PASSWORD:-}" go run ./cmd/flowdav -e "$$F"; \
	echo "Done: $$F.enc (chmod 600)"

# Container
docker-build:
	podman build -t localhost/flowdav:latest .

docker-e2e: test-e2e

# Build image and extract binaries to host
image-to-bin: docker-build
	@mkdir -p $(BIN_DIR) && \
	CID=$$(podman create localhost/flowdav:latest) && \
	trap "podman rm $$CID >/dev/null 2>&1 || true" EXIT; \
	podman cp $$CID:/usr/local/bin/flowdav $(BIN_DIR)/flowdav && \
	podman rm $$CID && \
	chmod +x $(BIN_DIR)/flowdav && \
	echo "Extracted binary from image to $(BIN_DIR)/flowdav"

# Release archives (matches CI: ./github/workflows/release.yml)
release:
	$(MAKE) build
	rm -rf $(RELEASE_DIR) && mkdir -p $(RELEASE_DIR)
	FOLDER=flowdav-$(VERSION)-linux-amd64; \
	mkdir -p $(RELEASE_DIR)/$$FOLDER; \
	cp $(BIN_DIR)/flowdav \
		$(RELEASE_DIR)/$$FOLDER/; \
	cp README.md $(RELEASE_DIR)/$$FOLDER/; \
	cp configs/flowdav.json.example $(RELEASE_DIR)/$$FOLDER/; \
	cd $(RELEASE_DIR) && tar -czf $$FOLDER.tar.gz $$FOLDER && \
	rm -rf $$FOLDER

clean:
	rm -rf $(BIN_DIR) $(RELEASE_DIR)
	rm -rf configs/flowdav_test_*.json configs/.env
	rm -f configs/client-android.json.enc
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
	@echo "Environment reset complete. Run 'make docker-build && make test-e2e' to rebuild."

# Quick Android test env: single WebDAV + flowdav-server
compose-android: docker-build
	@bash scripts/gen-test-configs.sh "$(HOST_IP)" && \
	podman-compose -f $(COMPOSE_FILE) down -v 2>/dev/null || true; \
	podman-compose -f $(COMPOSE_FILE) up -d webdav-single flow-server; \
	echo ""; \
	echo "=== Android test environment ==="; \
	echo "WebDAV:   http://$(HOST_IP):8080 (user: test, pass: test)"; \
	echo "Config:   configs/flowdav_test.json"; \
	echo "Deploy:   make android-deploy"

# Build APK, generate Android config, deploy if adb available
android-deploy: android-apk
	@if [ ! -f configs/flowdav_test.json ]; then \
		echo "Run 'make compose-android' first."; exit 1; \
	fi; \
	cp configs/flowdav_test.json configs/client-android.json; \
	echo "Config: configs/client-android.json (plaintext)"; \
	cp configs/flowdav_test.json configs/client-android.json; \
	FLOWDAV_PASSWORD=secret go run ./cmd/flowdav -e configs/client-android.json; \
	echo "Config: configs/client-android.json.enc (password: secret)"; \
	ADB=$$(which adb 2>/dev/null || true); \
	if [ -n "$$ADB" ] && $$ADB get-state 2>/dev/null | grep -q device; then \
		$$ADB install -r -d $(BIN_DIR)/flowdav-android.apk && \
		$$ADB push configs/client-android.json /sdcard/Download/flowdav-config.json && \
		$$ADB push configs/client-android.json.enc /sdcard/Download/flowdav-config.enc && \
		echo "=== Deployed via adb ==="; \
	else \
		echo "=== Ready (no adb device) — copy configs/client-android.json or client-android.json.enc manually ==="; \
	fi

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
	@mkdir -p $(BIN_DIR)
	@cp android/app/build/outputs/apk/debug/app-debug.apk $(BIN_DIR)/flowdav-android.apk
	@echo "APK: $(BIN_DIR)/flowdav-android.apk"

# Generate Android release keystore and print GitHub Secrets
android-keystore:
	bash scripts/gen-keystore.sh

# Install pre-commit hooks
check: vet lint build test

hooks:
	@cp .githooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Installed .githooks/pre-commit -> .git/hooks/pre-commit"

.DEFAULT_GOAL := build
