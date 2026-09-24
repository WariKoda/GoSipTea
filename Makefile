BINARY := gosiptea
CMD := ./cmd/gosiptea
PREFIX ?= $(HOME)/.local
ifeq ($(strip $(PREFIX)),)
override PREFIX := $(HOME)/.local
endif

.PHONY: all build test check install update uninstall clean

all: build

build:
	go build -o $(BINARY) $(CMD)

test:
	go test ./...

check:
	go test -race ./...
	go vet ./...

install:
	PREFIX="$(PREFIX)" ./scripts/install-local.sh

update: install

uninstall:
	rm -f "$(PREFIX)/bin/$(BINARY)"
	rm -f "$(PREFIX)/share/applications/gosiptea.desktop"
	@if command -v update-desktop-database >/dev/null 2>&1; then \
		update-desktop-database "$(PREFIX)/share/applications" >/dev/null 2>&1 || true; \
	fi

clean:
	rm -f $(BINARY)
