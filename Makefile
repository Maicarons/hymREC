BIN := bin/hymREC
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test test-race vet coverage run clean docs docs-dev fmt

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

docs-dev:
	cd docs && npm run dev

docs-build:
	cd docs && npm install && npm run build

fmt:
	gofmt -w cmd internal
