BIN := bin/miner
PKG := ./...

.PHONY: all build test test-short test-race vet fmt clean

all: fmt vet test build

build:
	@mkdir -p bin
	go build -o $(BIN) ./cmd/miner

# Full suite, including the synthetic-broadcast acceptance scenarios.
test:
	go test $(PKG)

# Pure logic only: FFT, normalisation, clustering rules, classification.
test-short:
	go test -short $(PKG)

test-race:
	go test -race $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -w .

clean:
	rm -rf bin
