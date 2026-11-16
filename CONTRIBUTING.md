# Contributing to PiPeek

Thanks for wanting to help. Bug reports, ideas and pull requests are all welcome.

## Reporting a problem

Open an [issue](https://github.com/leue21/pipeek/issues/new/choose). The bug
template asks for `pipeek -version`, your board and OS, and `uname -m`. With
those, most problems can be reproduced.

## What fits

PiPeek is meant to stay small: one static binary, the Go standard library
only, no database, no JavaScript build step, and nothing loaded from a CDN. A
new metric or setting is easiest to accept when it reads from `/proc` or
`/sys`, costs little per sample, and doesn't need a new dependency. If you're
unsure, open an issue first to discuss it.

## Development

Requires Linux, Go 1.24 or newer, and Make.

```sh
make build        # bin/pipeek
make test         # go test and go vet
make test-race    # race detector, on a host that supports it
gofmt -l .        # should print nothing
./bin/pipeek -listen :8080
```

CI runs all of these on every pull request and also builds every release
architecture (`make release` does the same locally). For changes to the page,
an optional browser check lives in `scripts/browser-check.cjs`; see the README.

## Pull requests

- Keep each pull request to one change, with tests where the code is testable.
- Update the README when a flag, metric or install step changes.
- For visual changes, include a screenshot. Check both themes and a narrow
  (phone-width) window.

By contributing, you agree that your work is licensed under the
[MIT License](LICENSE).
