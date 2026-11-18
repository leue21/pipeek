#!/bin/sh
# Sends a Telegram message with morse (https://github.com/leue21/morse) when
# PiPeek reports a problem and keeps that message current while it lasts. Once
# everything is fine again, the message stays and says so. Run it every minute
# from cron or a systemd timer:
#
#   * * * * * PATH=$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin /path/to/pipeek-alert.sh
#
# Needs curl, jq and a configured morse. PIPEEK_URL points at the JSON endpoint;
# include the -base-path prefix if you use one. On a hub (-peers), point it at
# api/fleet.json instead, and one job covers every machine.
# PIPEEK_ALERT_TOPIC posts into a topic of a Telegram group with topics: a
# name from morse's config, or a topic id.
# PIPEEK_ALERT_EVERY is the least time in seconds between two new messages
# (default 1800). A problem that comes back sooner reopens the last message
# silently instead of notifying again.

set -eu

url=${PIPEEK_URL:-http://127.0.0.1:8080/api/sample.json}
label=${PIPEEK_ALERT_LABEL:-pipeek-$(hostname)}
topic=${PIPEEK_ALERT_TOPIC:-}
every=${PIPEEK_ALERT_EVERY:-1800}
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
fine="$(hostname) is fine again"
# On a hub each line names its machine, so the title should not name one.
if [ -n "${json:-}" ] && printf '%s' "$json" | jq -e 'has("hosts")' >/dev/null; then
    title="PiPeek: a machine needs attention"
    fine="PiPeek: all machines are fine again"
fi

# The state file holds the last message: its id, when it was sent, whether
# the problem is still open, and the problems it last showed. An empty or
# older file counts as no message.
id='' sent=0 open=0 last=''
if [ -s "$state" ]; then
    { read -r id sent open; last=$(cat); } <"$state" || true
fi
case "$id$sent$open" in *[!0-9]*) id='' sent=0 open=0 last='' ;; esac
now=$(date +%s)

save() {
    mkdir -p "$(dirname "$state")"
    printf '%s %s %s\n%s\n' "$id" "$sent" "$1" "$2" >"$state.tmp"
    mv "$state.tmp" "$state"
}

if [ -n "$problems" ]; then
    # Edits never notify, so an open problem, or one back within $every of
    # the last message, only rewrites that message.
    if [ -n "$id" ] && { [ "$open" = 1 ] || [ $((now - sent)) -lt "$every" ]; }; then
        if [ "$problems" = "$last" ] && [ "$open" = 1 ] ||
            morse edit "$id" "$title" "$problems" >/dev/null; then
            save 1 "$problems"
            exit 0
        fi
        # The message is gone (deleted in the chat): send a new one, but not
        # sooner than $every after the last.
        if [ $((now - sent)) -lt "$every" ]; then
            save 1 "$problems"
            exit 0
        fi
    fi
    if [ -n "$topic" ]; then
        out=$(morse send --json --topic "$topic" "$title" "$problems")
    else
        out=$(morse send --json "$title" "$problems")
    fi
    id=$(printf '%s' "$out" | jq -r .message_id) sent=$now
    save 1 "$problems"
elif [ "$open" = 1 ]; then
    # Keep the message as a record of what happened, marked as resolved.
    morse edit "$id" "✅ $fine" "Since $(date +%H:%M). Was:
$last" >/dev/null || true
    save 0 "$last"
fi
