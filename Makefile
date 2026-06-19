# srm — build/install. The only supported runtime target is Ubuntu x64.
BINARY      := srm
VERSION     ?= dev
PREFIX      ?= /usr/local
LDFLAGS     := -s -w -X main.version=$(VERSION)

.PHONY: build build-linux release install uninstall test vet tidy clean run

## build: native binary for the current platform (dev convenience)
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/srm

## build-linux: static linux/amd64 binary (CGO off — all deps are pure Go)
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY) ./cmd/srm

## release: tagged, platform-named static artifact + checksum (e.g. make release VERSION=v1.0.0)
##   → dist/srm-<VERSION>-ubuntu-x64 (+ .sha256). The only supported target is Ubuntu x64.
release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-$(VERSION)-ubuntu-x64 ./cmd/srm
	cd dist && sha256sum $(BINARY)-$(VERSION)-ubuntu-x64 > $(BINARY)-$(VERSION)-ubuntu-x64.sha256
	@echo "built dist/$(BINARY)-$(VERSION)-ubuntu-x64"

## install: build the linux binary and install it to $(PREFIX)/bin (run on the server)
install: build-linux
	install -d $(PREFIX)/bin
	install -m 0755 dist/$(BINARY) $(PREFIX)/bin/$(BINARY)

uninstall:
	rm -f $(PREFIX)/bin/$(BINARY)

run:
	go run ./cmd/srm

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f $(BINARY) dist/$(BINARY)
