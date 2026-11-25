APP_NAME := daed-next
BUILD_DIR := dist
VERSION ?= v1.0.4
DEFAULT_REPO ?= skylink9119/DaeStudio
LDFLAGS := -s -w -X daed-next/internal/daemon.PanelVersion=$(VERSION) -X daed-next/internal/daemon.DefaultUpdateRepo=$(DEFAULT_REPO)

.PHONY: all build build-all preview test clean

all: build

## Build single self-contained binary for current OS/Arch (embeds web/)
build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/daed-next

## Cross-compile static single binaries for Linux (x86_64 / x86_32 / arm64) / OpenWrt / macOS
build-all:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-linux-amd64 ./cmd/daed-next
	cp -f $(BUILD_DIR)/$(APP_NAME)-linux-amd64 $(BUILD_DIR)/$(APP_NAME)-linux-x86_64
	CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-linux-386 ./cmd/daed-next
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-linux-arm64 ./cmd/daed-next
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-openwrt-mipsle ./cmd/daed-next
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-darwin-arm64 ./cmd/daed-next

## Start instant local preview server (works with Go or zero-dependency Python 3)
preview:
	@if command -v go >/dev/null 2>&1; then \
		go run ./cmd/daed-next -listen 127.0.0.1:2023 -data ./.runtime -config ./.runtime/config.dae; \
	else \
		python3 ./scripts/preview_server.py --host 127.0.0.1 --port 2023; \
	fi

## Run automated verification suite
test:
	go test ./...
	python3 ./tests/test_suite.py

clean:
	rm -rf $(BUILD_DIR) .runtime
