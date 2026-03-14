VERSION  ?= dev
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X github.com/nogo/herald/cmd.version=$(VERSION) \
            -X github.com/nogo/herald/cmd.commit=$(COMMIT) \
            -X github.com/nogo/herald/cmd.date=$(DATE)

.PHONY: build test clean fix

build:
	go build -ldflags "$(LDFLAGS)" -o herald .

test:
	go test ./...

clean:
	rm -f herald

fix:
	go fix ./...
