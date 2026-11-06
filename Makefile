VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64 windows/arm64
EMBED   := internal/remote/bin

.PHONY: test vet build linux-bins dist

test:
	go test ./...

vet:
	go vet ./...

# Binary for a Proxmox VE host (amd64).
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/pk3s-linux-amd64 ./cmd/pk3s

# Linux binaries embedded into every dist build for remote mode (--host).
linux-bins:
	@mkdir -p dist $(EMBED)
	@for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/pk3s-linux-$$arch ./cmd/pk3s || exit 1; \
		gzip -9 -c dist/pk3s-linux-$$arch > $(EMBED)/pk3s-linux-$$arch.gz; \
	done

# All targets with embedded Linux binaries, plus checksums.
dist: linux-bins
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -tags embedlinux -trimpath -ldflags '$(LDFLAGS)' -o dist/pk3s-$$os-$$arch$$ext ./cmd/pk3s || exit 1; \
	done
	cd dist && shasum -a 256 pk3s-* > SHA256SUMS
