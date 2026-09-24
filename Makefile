PLUGIN_NAME := orangeguard
VERSION ?= 0.1.0
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

ifeq ($(GOOS),darwin)
EXT := dylib
else ifeq ($(GOOS),windows)
EXT := dll
else
EXT := so
endif

DIST := dist/$(GOOS)/$(GOARCH)
# The plugin ID is the file name without extension, so the artifact must be
# named exactly orangeguard.<ext> to match plugins.configs.orangeguard.
ARTIFACT := $(DIST)/$(PLUGIN_NAME).$(EXT)
LDFLAGS := -X main.pluginVersion=$(VERSION)

# INSTALL_DIR must match plugins.dir in the cpa config.yaml.
INSTALL_DIR ?= $(HOME)/.cli-proxy-api/plugins/$(GOOS)/$(GOARCH)

.PHONY: all build test vet fmt install e2e clean

all: vet test build

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

build:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 go build -buildmode=c-shared -ldflags "$(LDFLAGS)" -o $(ARTIFACT) .
	@rm -f $(DIST)/$(PLUGIN_NAME).h
	@echo "built $(ARTIFACT)"

install: build
	@mkdir -p $(INSTALL_DIR)
	cp $(ARTIFACT) $(INSTALL_DIR)/
	@echo "installed to $(INSTALL_DIR)/$(notdir $(ARTIFACT))"

# End-to-end test against a real CLIProxyAPI built from the pinned SDK version.
e2e: build
	./test/e2e/run.sh $(ARTIFACT)

clean:
	rm -rf dist test/e2e/.work
