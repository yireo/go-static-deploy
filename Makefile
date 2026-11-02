VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/go-static-deploy ./cmd/go-static-deploy

test:
	go vet ./...
	go test ./...

dist:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/go-static-deploy-linux-amd64 ./cmd/go-static-deploy
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/go-static-deploy-linux-arm64 ./cmd/go-static-deploy
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/go-static-deploy-darwin-arm64 ./cmd/go-static-deploy

clean:
	rm -rf bin dist
