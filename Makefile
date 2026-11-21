BIN := bin/hymREC
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build build-linux build-darwin build-windows vet test test-race coverage run clean fmt fmt-check docs docs-dev docs-build

all: vet build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/hymREC

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/hymREC-linux-amd64 ./cmd/hymREC

build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/hymREC-darwin-amd64 ./cmd/hymREC

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/hymREC-windows-amd64.exe ./cmd/hymREC

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

run: build
	$(BIN) -c config.yaml

# 构建文档静态站点（docs-dev 启动本地预览）
docs: docs-build

docs-build:
	cd docs && npm ci && npm run build

docs-dev:
	cd docs && npm run dev

fmt:
	gofmt -w cmd internal

fmt-check:
	@out="$$(gofmt -l cmd internal)"; \
	if [ -n "$$out" ]; then echo "以下文件未格式化，请执行 make fmt："; echo "$$out"; exit 1; fi

clean:
	rm -rf bin coverage.out coverage.html docs/.vitepress/dist
