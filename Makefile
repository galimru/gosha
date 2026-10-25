# Builds gosha from this checkout. `make install` puts it where install.sh
# does, so it replaces a released binary in place.

VERSION ?= $(shell git describe --tags --always --dirty)
PREFIX ?= $(if $(filter 0,$(shell id -u)),/usr/local/bin,$(HOME)/.local/bin)

.PHONY: build install test

build:
	go build -ldflags "-X main.version=$(VERSION)" -o gosha ./cmd/gosha

# Written next to the target, then renamed over it, like install.sh.
install: build
	mkdir -p "$(PREFIX)"
	cp gosha "$(PREFIX)/.gosha.new"
	mv -f "$(PREFIX)/.gosha.new" "$(PREFIX)/gosha"
	@echo "installed $(PREFIX)/gosha $(VERSION)"

test:
	go test ./...
