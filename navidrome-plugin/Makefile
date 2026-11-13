GO ?= go
PYTHON ?= python3
DIST := ../build/navidrome-plugin
PACKAGE := $(DIST)/navidrome-nowplaying.ndp

.PHONY: all test verify clean
all: $(PACKAGE)

test:
	$(GO) test ./...
	$(GO) vet ./...
	$(PYTHON) -m unittest discover -s tests -v

verify: $(PACKAGE)
	$(PYTHON) verify_package.py $(PACKAGE)

$(DIST)/plugin.wasm: $(wildcard *.go) go.mod go.sum
	mkdir -p $(DIST)
	GOOS=wasip1 GOARCH=wasm $(GO) build -trimpath -buildmode=c-shared -o $@ .

$(PACKAGE): $(DIST)/plugin.wasm manifest.json $(wildcard cmd/package/*.go)
	$(GO) run ./cmd/package manifest.json $(DIST)/plugin.wasm $@

clean:
	rm -f $(DIST)/plugin.wasm $(PACKAGE)
