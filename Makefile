.DEFAULT_GOAL := build

GO ?= go
SUDO ?=
DESTDIR ?=
LDFLAGS ?= -s -w

.PHONY: build test test-race install clean

# Go's build cache makes repeated builds inexpensive and tracks embedded assets.
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o bin/pipeek .

test:
	$(GO) test ./...
	$(GO) vet ./...

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
	rm -rf bin
