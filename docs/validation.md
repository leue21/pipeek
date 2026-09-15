# Validation — 2026-09-14

Test environment: Linux arm64, Go 1.26.5. Default 3-second sampling, 10-minute history, root filesystem, automatic interface selection.

- `go test ./...` and `go vet ./...`: passed. Includes counter resets, missing metrics, memory accounting, interface filters, ring retention, chart gaps, concurrent HTTP readers, escaping, and root/nested reverse proxy paths.
- `go test -race ./...`: could not execute on this host. ThreadSanitizer reports `unsupported VMA range`, found 47 / supported 48. The race check remains outstanding on a compatible host.
- Statically linked Linux arm64, ARMv7, and ARMv6 builds: compiled successfully. ARMv6/v7 execution was not tested on those devices.
- nginx 1.26.3: both supplied configurations passed `nginx -t` with local test ports/log paths. Actual HTTP and browser checks passed through a temporary nginx server at `/` and `/pipeek/`.
- systemd: the supplied unit passed `systemd-analyze verify` using the local build path for ExecStart. The service was subsequently installed, enabled on boot, and verified through its HTTPS reverse proxy.
- Chromium / Playwright: desktop dark/light themes, persisted theme choice, 390-pixel mobile layout without horizontal overflow, live updates, hidden-page polling pause/resume, offline status and recovery passed. Visibility was simulated with a `document.hidden` override plus a visibility event. No unexpected JavaScript/CSP errors or external asset requests.

Two static-build processes consumed 11,552 and 11,728 KiB RSS and approximately 0.067% of one CPU core each over a 30-second check with browser activity. The stripped arm64 binary was approximately 7.7 MiB. These are short local measurements, not a guarantee for other hardware or workloads.

The tests used temporary nginx and browser dependencies outside the source tree. No global nginx configuration was changed. Browser tools are not runtime dependencies.

The Catppuccin Mocha update also passed browser checks for palette colors, theme toggling/persistence, and mobile layout.

[Dashboard preview](dashboard.png)

## Reliability fixes — 2026-09-15

- Go tests and vet passed, including active-request draining, shutdown deadlines, required mounts absent at startup, mount loss/recovery, filesystem identity changes, and server-computed sample age.
- Chromium regression checks passed with browser clocks shifted ±60 seconds, a hung polling request, repeated frozen samples, and recovery after those failures.
- In an isolated Linux mount namespace, unmounting a temporary test filesystem changed `/healthz` from 200 to 503. The host's mounts were not changed.
- Linux ARMv6 and ARMv7 builds compiled successfully; native Linux arm64 was built and deployed.
- The race detector limitation described above remains unresolved.
