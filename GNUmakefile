PLUGIN_BINARY=nix-driver
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

ifeq ($(shell uname -s),Darwin)
CGO_ENABLED := 0
else
CGO_ENABLED := 1
endif

default: build

.PHONY: build clean test fmt vet

build:
	CGO_ENABLED=$(CGO_ENABLED) go build $(LDFLAGS) -o $(PLUGIN_BINARY) .

clean:
	rm -rf $(PLUGIN_BINARY)

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...
