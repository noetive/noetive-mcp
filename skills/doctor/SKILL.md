---
name: doctor
description: Run diagnostics on the noetive-mcp plugin installation
disable-model-invocation: true
---

Run the following diagnostic checks for the noetive-mcp plugin and report results as a PASS/FAIL table.

## Checks

1. **Binary exists** - Verify `${CLAUDE_PLUGIN_ROOT}/bin/noetive-mcp` exists and is executable.
2. **plugin.json valid** - Verify `${CLAUDE_PLUGIN_ROOT}/.claude-plugin/plugin.json` is valid JSON and contains a `name` field.
3. **MCP config valid** - Verify `${CLAUDE_PLUGIN_ROOT}/.mcp.json` is valid JSON and its `mcpServers.noetive-mcp.command` references `${CLAUDE_PLUGIN_ROOT}/bin/noetive-mcp`.
4. **Hooks config valid** - Verify `${CLAUDE_PLUGIN_ROOT}/hooks/hooks.json` exists and is valid JSON.
5. **MCP server responds** - Call the `hello_world` tool via the MCP server as an end-to-end health check.

## Output format

Present results as a table:

| Check | Status | Detail |
|-------|--------|--------|
| Binary exists | PASS/FAIL | path or error |
| plugin.json valid | PASS/FAIL | detail |
| MCP config valid | PASS/FAIL | detail |
| Hooks config valid | PASS/FAIL | detail |
| MCP server responds | PASS/FAIL | response or error |

For any FAIL, include a remediation step below the table explaining how to fix the issue (e.g. run `make build` if the binary is missing).
