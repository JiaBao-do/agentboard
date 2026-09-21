# Example: reporting a Claude Code session to the board

This is documentation only: agentboard's core knows nothing about Claude Code. Any tool that can run a command can do this.

Give each session a name and let hooks keep it visible. In `.claude/settings.json` of your project:

```json
{
  "env": { "AGENTBOARD_URL": "http://127.0.0.1:7878", "AGENTBOARD_AGENT": "my-project-claude" },
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "agentboard agent heartbeat -kind claude-code" }] }],
    "PostToolUse":  [{ "hooks": [{ "type": "command", "command": "agentboard agent heartbeat" }] }],
    "Stop":         [{ "hooks": [{ "type": "command", "command": "agentboard task comment $TASK 'turn finished'" }] }]
  }
}
```

- `PostToolUse` runs often, so the agent stays online and its leases stay alive while it works.
- Set `TASK` (or pass `-task ID` to the heartbeat) to the task the session is on, so the board shows what it is doing.
- Claim before you start (`agentboard task claim ID -lease 30m`) and finish with `agentboard task done ID`.
- If a session dies, its lease expires and the task returns to `todo` on its own.

Check the exact hook schema in the Claude Code documentation for your version; only the `agentboard` commands here are
part of this project.
