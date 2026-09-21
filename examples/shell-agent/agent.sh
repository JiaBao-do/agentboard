#!/bin/sh
# shell-agent: the whole agent lifecycle with the agentboard CLI only.
#
#   agentboard serve &                       # in another terminal
#   AGENTBOARD_AGENT=shell-bot sh examples/shell-agent/agent.sh
#
# Expected output (ids depend on what is already on the board):
#   DEMO-1 claimed
#   DEMO-1 review
#   DEMO-1 done
set -eu
: "${AGENTBOARD_AGENT:=shell-bot}"      # who you are: every write is attributed to this name
export AGENTBOARD_AGENT
export AGENTBOARD_URL="${AGENTBOARD_URL:-http://127.0.0.1:7878}"

agentboard project add DEMO "Demo project" >/dev/null       # idempotent
id=$(agentboard task add -p DEMO -ensure "Rotate the logs")  # idempotent: same title, same task

agentboard agent heartbeat -kind shell -meta host=example >/dev/null
agentboard task claim "$id" -lease 5m >/dev/null && echo "$id claimed"
agentboard agent heartbeat -task "$id" >/dev/null            # extends the lease

agentboard task update "$id" -status review >/dev/null && echo "$id review"
agentboard task comment "$id" "logs rotated, waiting for a look" >/dev/null
agentboard task done "$id" >/dev/null && echo "$id done"
