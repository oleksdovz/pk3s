VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64 windows/arm64

.PHONY: test vet build dist

test:
	go test ./...

vet:
	go vet ./...

# Binary for a Proxmox VE host (amd64).
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/pk3s-linux-amd64 ./cmd/pk3s

# All targets, with checksums.
dist:
	@mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/pk3s-$$os-$$arch$$ext ./cmd/pk3s || exit 1; \
	done
	cd dist && shasum -a 256 pk3s-* > SHA256SUMS
