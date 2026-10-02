# RKCAN build
#   make            build all target binaries into build/
#   make package    build release packages (dist/rkcan-<ver>-linux-<arch>.tar.gz)
#   make test       vet + unit tests
#   make receiver   build the PC-side UDP receiver (needs cmake)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO      ?= go
LDFLAGS := -s -w -X main.version=$(VERSION)
ARCHES  := arm64 arm32 amd64

DEPLOY_FILES := deploy/install.sh deploy/uninstall.sh deploy/rkcan.conf deploy/rkcan-can-setup \
                deploy/rkcan-ctl deploy/rkcan.service deploy/rkcan-can.service deploy/rkcan.init

.PHONY: all build test vet fmt package receiver clean $(ARCHES:%=build/rkcan-linux-%)

all: build

build: $(ARCHES:%=build/rkcan-linux-%)

build/rkcan-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ .

build/rkcan-linux-arm32:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ .

build/rkcan-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ .

vet:
	$(GO) vet ./...

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

test: fmt vet
	$(GO) test -race ./...

package: build
	@mkdir -p dist
	@for a in $(ARCHES); do \
		d=rkcan-$(VERSION)-linux-$$a; \
		rm -rf dist/$$d; mkdir -p dist/$$d; \
		cp build/rkcan-linux-$$a dist/$$d/rkcan; \
		cp $(DEPLOY_FILES) README.md dist/$$d/; \
		chmod 755 dist/$$d/rkcan dist/$$d/*.sh dist/$$d/rkcan-can-setup dist/$$d/rkcan-ctl dist/$$d/rkcan.init; \
		tar -C dist -czf dist/$$d.tar.gz $$d; \
		rm -rf dist/$$d; \
		echo "dist/$$d.tar.gz"; \
	done

receiver:
	cmake -S UdpCanFdReceiver -B UdpCanFdReceiver/build -DCMAKE_BUILD_TYPE=Release
	cmake --build UdpCanFdReceiver/build --config Release

clean:
	rm -rf build dist UdpCanFdReceiver/build
