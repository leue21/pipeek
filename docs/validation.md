# Validation — 2026-09-14

Test environment: Linux arm64, Go 1.26.5. Default 3-second sampling, 10-minute history, root filesystem, automatic interface selection.

- `go test ./...` and `go vet ./...`: passed. Includes counter resets, missing metrics, memory accounting, interface filters, ring retention, chart gaps, concurrent HTTP readers, escaping, and root/nested reverse proxy paths.
- `go test -race ./...`: could not execute on this host. ThreadSanitizer reports `unsupported VMA range`, found 47 / supported 48. The race check remains outstanding on a compatible host. Since November 2026, CI runs `make test-race` on every push and pull request (GitHub Actions, linux/amd64).
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

## Pi readings and JSON API — 2026-11-16

Raspberry Pi 5 Model B (4 GB), Raspberry Pi OS 64-bit, kernel 6.18. The new build (load average, CPU frequency, fan, under-voltage, disk I/O rates and `/api/sample.json`) ran next to the v0.2.0 service, with both polled every 3 seconds as an open browser tab would, for 60 seconds:

| Build | Resident memory | CPU (one core) |
|---|---|---|
| v0.2.0 | 13.5 MB | 0.12% |
| New readings | 14.5 MB | 0.08% |

The CPU difference is within measurement noise (both are a handful of clock ticks over the minute). The stripped arm64 binary grew from 8,061,090 to 8,126,626 bytes. All readings come from `/proc` and `/sys` with ordinary read access; the service's sandbox (`DynamicUser`, `ProtectSystem=strict`) is unchanged. The full Raspberry Pi throttling flags (`vcgencmd get_throttled`) need root access to `/dev/vcio` and are deliberately not read.

Verified on the device: CPU frequency 1.7 of 2.4 GHz, load average, under-voltage monitor (`rpi_volt`, reading OK), and read/write rates for the root filesystem on a USB SSD and for a USB hard drive. This board has no fan sensor, so the fan reading is absent, as intended.

## Hub mode — 2026-11-16

Same Raspberry Pi 5. A hub with two peers (this Pi's own PiPeek through `tailscale serve` HTTPS, and an address that refuses connections) ran next to the single v0.3.1 service, both polled every 3 seconds for 60 seconds:

| Build | Resident memory | CPU (one core) |
|---|---|---|
| v0.3.1, single | 13.7 MB | 0.15% |
| Hub, 2 peers | 16.5 MB | 0.13% |

The difference is mostly Go's HTTPS client. A PiPeek without `-peers` starts no poller. The binary grew from 8,126,626 to 8,650,914 bytes, because the HTTPS client is now compiled in. The unreachable peer showed as "Never reached" and did not delay the reachable one. The service sandbox already allows outgoing IPv4/IPv6, so the unit file is unchanged.
