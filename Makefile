VERSION := 0.1.$(shell git rev-list --count a9682280b407c90abb337f2a68ebd5a527b9d086..HEAD 2>/dev/null || echo 0)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOEXE := $(shell go env GOEXE)
.PHONY: build vet fmt test hooks cross clean install

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o reasonix$(GOEXE) ./cmd/reasonix
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/reasonix-plugin-example$(GOEXE) ./cmd/reasonix-plugin-example

vet:
	go vet ./...

fmt:
	gofmt -w .

test:
	go test ./... -timeout=180s

hooks:
	@git config core.hooksPath .githooks
	@echo "installed: core.hooksPath -> .githooks (pre-push runs go vet)"

cross:
	@mkdir -p dist
	@for p in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o dist/reasonix-$$os-$$arch$$ext ./cmd/reasonix; \
	done

clean:
	rm -rf bin dist

install: build
	rm -f /usr/local/bin/reasonix$(GOEXE)
	cp reasonix$(GOEXE) /usr/local/bin/reasonix$(GOEXE)
	@echo "installed to /usr/local/bin/reasonix$(GOEXE)"

