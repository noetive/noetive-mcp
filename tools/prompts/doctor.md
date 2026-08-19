Diagnose this machine's Noetive MCP installation and report the results as a PASS/FAIL table.

Four independent things can be wrong, and they need different fixes. Check them in this order, because each later check assumes the earlier ones passed.

## Checks

1. **Wrapper and binary** — run `npx -y @noetive/mcp-server doctor --json`. It reports where the platform binary resolved from and whether `NOETIVE_KEY_SECRET` is set in this shell. A missing binary means the install did not complete; a missing key means the editor will connect but every call will be refused.

2. **Editor configuration** — the same command lists every detected editor and every scope where it has a `noetive` entry. An editor the user installed but chose not to configure is reported informationally, not as a failure; do not tell them to fix it unless they ask. Only "no editor is configured at all" is a fault.

3. **Server reachable** — call the `noetive_health` tool. Success means this editor reached Noetive Semantik and its key was accepted. A failure here carries the server's own error code and a `request_id`; quote that id when asking for help.

4. **Tools registered** — confirm all five tools are visible: `noetive_publish`, `noetive_search`, `noetive_subscribe`, `noetive_lint`, `noetive_health`. Fewer than five means the editor is running an older build than the one configured.

## Output format

| Check | Status | Detail |
|-------|--------|--------|
| Wrapper and binary | PASS/FAIL | resolved path or the error |
| API key | PASS/FAIL | set, or where to set it |
| Editor configuration | PASS/FAIL/INFO | scope and config path per detected editor |
| Server reachable | PASS/FAIL | response, or code and request_id |
| Tools registered | PASS/FAIL | the tool names seen |

If the API key check reports the literal text `${NOETIVE_KEY_SECRET}` rather than a key, the editor did not substitute the variable — usually because it was started from a desktop launcher, which does not read the user's shell profile. The fix is to launch it from a terminal where the variable is exported, or to re-run `init` with `--api-key`.

For every FAIL, give the one command that fixes it. Do not suggest a fix for a check that passed, and do not speculate past the first failure — a key that cannot be read makes every later result meaningless.
