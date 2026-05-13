BINARY  := k8said
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install clean vet lint

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

install:
	go install -trimpath -ldflags="$(LDFLAGS)" .

clean:
	rm -f $(BINARY)

vet:
	go vet ./...

lint: vet
	golangci-lint run ./...
