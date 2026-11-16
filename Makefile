.DEFAULT_GOAL := build

GO ?= go
SUDO ?=
DESTDIR ?=
# Stamped into the binary so `pipeek -version` names its release. Outside a
# checkout there is no tag to find, and pipeek falls back to Go's build info.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS ?= -s -w -X main.version=$(VERSION)

.PHONY: build test test-race release install clean

# Go's build cache makes repeated builds inexpensive and tracks embedded assets.
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o bin/pipeek .

test:
	$(GO) test ./...
	$(GO) vet ./...

# Every release archive and SHA256SUMS, as the release workflow builds them.
release:
	GO=$(GO) scripts/build-release.sh $(VERSION) dist

# Separate because some ARM kernels cannot run ThreadSanitizer.
test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

# Install through a new inode so upgrading a running executable is safe.
# Existing systemd drop-ins (listen address, required mounts, etc.) are retained.
install: build
	$(SUDO) install -d "$(DESTDIR)/usr/local/bin" "$(DESTDIR)/etc/systemd/system"
	$(SUDO) install -m 0755 bin/pipeek "$(DESTDIR)/usr/local/bin/pipeek.new"
	$(SUDO) mv -f "$(DESTDIR)/usr/local/bin/pipeek.new" "$(DESTDIR)/usr/local/bin/pipeek"
	$(SUDO) install -m 0644 deploy/pipeek.service "$(DESTDIR)/etc/systemd/system/pipeek.service"
	@if [ -z "$(DESTDIR)" ]; then \
		$(SUDO) systemctl daemon-reload || exit $$?; \
		echo "Installed PiPeek. Start with: sudo systemctl enable --now pipeek"; \
		echo "If already running, load the new binary with: sudo systemctl restart pipeek"; \
	else \
		echo "Staged PiPeek under $(DESTDIR)"; \
	fi

clean:
	rm -rf bin dist
