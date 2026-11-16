#!/bin/sh
# Sends a Telegram message with morse (https://github.com/leue21/morse) when
# PiPeek reports a problem, keeps that one message current while it lasts, and
# removes it once everything is fine again. Run it every minute from cron or a
# systemd timer:
#
#   * * * * * PATH=$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin /path/to/pipeek-alert.sh
#
# Needs curl, jq and a configured morse. PIPEEK_URL points at the JSON endpoint;
# include the -base-path prefix if you use one. On a hub (-peers), point it at
# api/fleet.json instead, and one job covers every machine.

set -eu

url=${PIPEEK_URL:-http://127.0.0.1:8080/api/sample.json}
label=${PIPEEK_ALERT_LABEL:-pipeek-$(hostname)}
state=${XDG_STATE_HOME:-$HOME/.local/state}/pipeek-alert-$label

# One line per problem; empty when all is well. Under-voltage and a hot CPU
# come from the dashboard's own readings, the rest from PiPeek's warnings
# (a required mount missing, counters unreadable).
json=
if json=$(curl -fsS --max-time 10 "$url"); then
    problems=$(printf '%s' "$json" | jq -r '
        def problems:
            (if .power.under_voltage == true then "⚡ Under-voltage: check the power supply" else empty end),
            (if .temperature.status == "hot" or .temperature.status == "very-hot"
                then "🌡 CPU at \(.temperature.celsius) °C (\(.temperature.status))" else empty end),
            (.warnings[] | "⚠ \(.)");
        if has("hosts") then
            [ .hosts[] | .name as $n |
              if .online then (.sample | problems | "\($n): \(.)")
              else "\($n): not answering\(if .error then " (\(.error))" else "" end)" end ]
        else [ problems ] end
        | join("\n")')
else
    problems="PiPeek is not answering at $url"
fi
title="$(hostname) needs attention"
# On a hub each line names its machine, so the title should not name one.
if [ -n "${json:-}" ] && printf '%s' "$json" | jq -e 'has("hosts")' >/dev/null; then
    title="PiPeek: a machine needs attention"
fi

if [ -n "$problems" ]; then
    # The first send notifies; later runs edit the same message silently.
    morse send --track "$label" "$title" "$problems"
    mkdir -p "$(dirname "$state")"
    touch "$state"
elif [ -e "$state" ]; then
    morse delete --track "$label"
    rm -f "$state"
fi
