# Classy Cash (Go). The legacy PHP sources remain in the tree as the parity reference.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG      = github.com/classy-cash/app/internal/version
LDFLAGS  = -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)
DIST     = dist

.PHONY: build run test race vet fmt check release docker clean
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/classycash ./cmd/classycash
run:
	CLASSYCASH_ENV=dev CLASSYCASH_BASE_URL=http://127.0.0.1:8080 CLASSYCASH_DB_PATH=data/dev.db go run ./cmd/classycash serve
test:
	go test -count=1 ./...
race:
	go test -race -count=1 ./...
vet:
	go vet ./...
fmt:
	gofmt -s -w cmd internal assets migrations
check: vet race
	@test -z "$$(gofmt -s -l cmd internal assets migrations)" || (echo "run make fmt"; exit 1)
# Release artifacts: static binaries for the FROG (amd64/arm64) plus checksums (see scripts/release.sh).
release:
	VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE) sh scripts/release.sh
docker:
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t classycash:$(VERSION) .
clean:
	rm -rf bin $(DIST)
