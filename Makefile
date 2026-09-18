PLUGIN_ID := codex-limiter
VERSION ?= 0.0.3
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
DIST_DIR ?= dist
PLATFORM_DIR := $(DIST_DIR)/$(GOOS)-$(GOARCH)

ifeq ($(GOOS),darwin)
LIB_EXT := dylib
else ifeq ($(GOOS),windows)
LIB_EXT := dll
else
LIB_EXT := so
endif

LIBRARY := $(PLATFORM_DIR)/$(PLUGIN_ID).$(LIB_EXT)
ARCHIVE := $(PLUGIN_ID)_$(VERSION)_$(GOOS)_$(GOARCH).zip

.PHONY: test vet build package clean

test:
	go test ./...

vet:
	go vet ./...

build:
	mkdir -p "$(PLATFORM_DIR)"
	CGO_ENABLED=1 GOOS="$(GOOS)" GOARCH="$(GOARCH)" go build \
		-trimpath \
		-buildmode=c-shared \
		-ldflags="-s -w -X main.pluginVersion=$(VERSION)" \
		-o "$(LIBRARY)" .
	rm -f "$(PLATFORM_DIR)/$(PLUGIN_ID).h"

package: build
	rm -f "$(DIST_DIR)/$(ARCHIVE)" "$(DIST_DIR)/$(ARCHIVE).sha256"
	cd "$(PLATFORM_DIR)" && zip -q -9 "../$(ARCHIVE)" "$(PLUGIN_ID).$(LIB_EXT)"
	cd "$(DIST_DIR)" && if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum "$(ARCHIVE)" > "$(ARCHIVE).sha256"; \
	else \
		shasum -a 256 "$(ARCHIVE)" > "$(ARCHIVE).sha256"; \
	fi

clean:
	rm -rf "$(DIST_DIR)"
