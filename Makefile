TOOLCTL_GO ?= go
TOOLCTL_BUILD_CACHE ?= $(CURDIR)/.cache/go-build
TOOLCTL_DIST ?= $(CURDIR)/dist
TOOLCTL_VERSION ?= dev
TOOLCTL_COMMIT ?= unknown
TOOLCTL_BUILD_TIME ?= unknown

TOOLCTL_LDFLAGS = -X main.version=$(TOOLCTL_VERSION) -X main.commit=$(TOOLCTL_COMMIT) -X main.buildTime=$(TOOLCTL_BUILD_TIME)

.PHONY: fmt test test-race vet verify build-linux

fmt:
	$(TOOLCTL_GO) fmt ./...

test:
	GOCACHE=$(TOOLCTL_BUILD_CACHE) $(TOOLCTL_GO) test ./...

test-race:
	GOCACHE=$(TOOLCTL_BUILD_CACHE) $(TOOLCTL_GO) test -race ./...

vet:
	GOCACHE=$(TOOLCTL_BUILD_CACHE) $(TOOLCTL_GO) vet ./...

verify: test test-race vet build-linux

build-linux:
	mkdir -p $(TOOLCTL_DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOCACHE=$(TOOLCTL_BUILD_CACHE) $(TOOLCTL_GO) build -trimpath -ldflags "$(TOOLCTL_LDFLAGS)" -o $(TOOLCTL_DIST)/toolctl_linux_amd64 ./cmd/toolctl
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOCACHE=$(TOOLCTL_BUILD_CACHE) $(TOOLCTL_GO) build -trimpath -ldflags "$(TOOLCTL_LDFLAGS)" -o $(TOOLCTL_DIST)/toolctl_linux_arm64 ./cmd/toolctl
