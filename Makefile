# srm - build/install. The only supported runtime target is Ubuntu x64.
BINARY      := srm
VERSION     ?= dev
PREFIX      ?= /usr/local
LDFLAGS     := -s -w -X main.version=$(VERSION)
# -buildvcs=false: the version is stamped explicitly via LDFLAGS, so we don't need
# Go's VCS stamping - and disabling it keeps builds from failing when .git is absent
# or owned by another user (CI checkouts, release containers).
GOFLAGS     := -buildvcs=false

.PHONY: build build-linux build-windows release release-windows install uninstall test vet tidy clean run

## build: native binary for the current platform (dev convenience)
build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/srm

## build-linux: static linux/amd64 binary (CGO off - all deps are pure Go)
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY) ./cmd/srm

## build-windows: windows/amd64 binary (dev convenience). Same cmd/srm as Linux -
## one codebase cross-compiles to both OSes via build tags (GOOS selects the impl).
build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-win.exe ./cmd/srm

## release: tagged, platform-named static artifact + checksum (e.g. make release VERSION=v1.0.0)
##   → dist/srm-<VERSION>-ubuntu-x64 (+ .sha256). The Ubuntu x64 target.
release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-$(VERSION)-ubuntu-x64 ./cmd/srm
	cd dist && sha256sum $(BINARY)-$(VERSION)-ubuntu-x64 > $(BINARY)-$(VERSION)-ubuntu-x64.sha256
	@echo "built dist/$(BINARY)-$(VERSION)-ubuntu-x64"

## release-windows: tagged windows/amd64 artifact + checksum (e.g. make release-windows VERSION=v1.5.0)
##   → dist/srm-<VERSION>-windows-x64.exe (+ .sha256). Same cmd/srm as the Ubuntu target;
## GOOS=windows selects the Windows impl via build tags. Cross-compiles cleanly from
## linux/amd64 (CGO off, all deps pure Go), so the same CI runner that builds the Ubuntu
## artifact builds this. install.ps1 consumes both files.
release-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-$(VERSION)-windows-x64.exe ./cmd/srm
	cd dist && sha256sum $(BINARY)-$(VERSION)-windows-x64.exe > $(BINARY)-$(VERSION)-windows-x64.exe.sha256
	@echo "built dist/$(BINARY)-$(VERSION)-windows-x64.exe"

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
