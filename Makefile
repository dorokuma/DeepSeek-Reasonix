VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOEXE := $(shell go env GOEXE)
.PHONY: build vet fmt test hooks cross clean install

build:
	CGO_ENABLED=0 go build -buildvcs=false -ldflags "$(LDFLAGS)" -o /usr/local/bin/reasonix$(GOEXE) ./cmd/reasonix
	CGO_ENABLED=0 go build -buildvcs=false -ldflags "$(LDFLAGS)" -o /dev/null ./cmd/reasonix-plugin-example

vet:
	go vet ./...

fmt:
	gofmt -w .

test:
	go test ./... -timeout=180s

hooks:
	@git config core.hooksPath .githooks
	@echo "installed: core.hooksPath -> .githooks (pre-commit secret scan; pre-push: go vet)"

cross:
	@mkdir -p dist
	@for p in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -buildvcs=false -ldflags "$(LDFLAGS)" -o dist/reasonix-$$os-$$arch$$ext ./cmd/reasonix; \
	done

clean:
	rm -rf dist

install:
	rm -f /usr/local/bin/reasonix$(GOEXE)
	$(MAKE) build
	@echo "installed to /usr/local/bin/reasonix$(GOEXE)"

