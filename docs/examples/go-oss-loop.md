# Example: tracking an automation loop (one story per project, one task per stage)

Again documentation only. A loop that advances projects through stages can mirror its ledger on the board with three commands.
Seeding is idempotent, so the script can run on every tick.

```sh
export AGENTBOARD_URL=http://127.0.0.1:7878 AGENTBOARD_AGENT=loop-driver
agentboard project add LOOP "Automation loop"
epic=$(agentboard task add -p LOOP -ensure -type epic  "Library pipeline")
story=$(agentboard task add -p LOOP -ensure -type story -parent "$epic" "mylib: build and publish v0.1.0")
for stage in validate build verify push ci tag; do
  agentboard task add -p LOOP -ensure -parent "$story" "$stage" >/dev/null
done

# when a stage starts / finishes (the worker uses its own name):
AGENTBOARD_AGENT=mylib-builder agentboard task claim LOOP-4 -lease 1h
AGENTBOARD_AGENT=mylib-builder agentboard task done  LOOP-4
```

Each builder agent heartbeats under its own name, so the board shows who is working on which stage and who has finished.
