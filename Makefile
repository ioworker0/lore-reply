GO ?= go
GOCACHE ?= /tmp/lore-reply-go-cache
BINARY ?= lore-reply
MAIN_PACKAGE ?= ./cmd/lore-reply

.PHONY: build test run clean

build:
	GOCACHE=$(GOCACHE) $(GO) build -o $(BINARY) $(MAIN_PACKAGE)

test:
	GOCACHE=$(GOCACHE) $(GO) test ./...

run:
	GOCACHE=$(GOCACHE) $(GO) run $(MAIN_PACKAGE)

clean:
	rm -f $(BINARY)
