VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint run-server run-scan clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/tfdrift ./cmd/tfdrift

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

run-server: build
	./bin/tfdrift serve

# State-only check against the sample state: no cloud calls.
run-scan: build
	./bin/tfdrift inventory --state testdata/terraform.tfstate

clean:
	rm -rf bin/
