# shell-agent (PowerShell): the whole agent lifecycle with the agentboard CLI only.
#
#   agentboard serve                          # in another terminal
#   $env:AGENTBOARD_AGENT = "shell-bot"; ./examples/shell-agent/agent.ps1
$ErrorActionPreference = "Stop"
if (-not $env:AGENTBOARD_AGENT) { $env:AGENTBOARD_AGENT = "shell-bot" }   # who you are
if (-not $env:AGENTBOARD_URL)   { $env:AGENTBOARD_URL = "http://127.0.0.1:7878" }

agentboard project add DEMO "Demo project" | Out-Null                     # idempotent
$id = (agentboard task add -p DEMO -ensure "Rotate the logs" | Select-Object -First 1).Trim()

agentboard agent heartbeat -kind shell -meta host=example | Out-Null
agentboard task claim $id -lease 5m | Out-Null; "$id claimed"
agentboard agent heartbeat -task $id | Out-Null                           # extends the lease

agentboard task update $id -status review | Out-Null; "$id review"
agentboard task comment $id "logs rotated, waiting for a look" | Out-Null
agentboard task done $id | Out-Null; "$id done"
