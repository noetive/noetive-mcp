import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { ClaudeCodeAdapter, RunResult, Runner } from "../src/adapters/claudeCode";
import { ClientSpec, SERVER_NAME } from "../src/clients";

// A stand-in for the `claude` binary. Every test states what the CLI does and
// then asserts what the adapter did with that, so nothing here shells out.
function recorder(outcomes: Record<string, RunResult> = {}): { run: Runner; calls: string[][] } {
  const calls: string[][] = [];
  const run: Runner = (command, args) => {
    calls.push([command, ...args]);
    const key = args[0] === "--version" ? "version" : args[1] ?? "";
    return outcomes[key] ?? { status: 0, stdout: "", stderr: "" };
  };
  return { run, calls };
}

const claudeCode: ClientSpec = {
  displayName: "Claude Code",
  configFormat: "json",
  topLevelKey: "mcpServers",
  install: "cli-delegate",
  expandsVariables: true,
  cli: {
    command: "claude",
    args: ["mcp", "add", "--scope", "${scope}", "--transport", "stdio", "${serverName}", "--", "npx", "-y", "${packageName}"],
    removeArgs: ["mcp", "remove", "--scope", "${scope}", "${serverName}"],
  },
  scopes: { local: { path: "${workspace}/.claude.json", default: true } },
  restartHint: "Start a new Claude Code session.",
};

function request(workspace: string, dryRun = false) {
  return { spec: claudeCode, clientId: "claude-code", scope: "local", workspace, entryOptions: {}, dryRun };
}

// Claude Code keys user-scope entries per project inside ~/.claude.json, a
// layout the file path alone does not describe and one that has already changed
// between releases. Delegating means we never encode that private layout.
test("the editor's own CLI is used when it is available", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new ClaudeCodeAdapter(run).install(request(workspace));

  const add = calls.find((c) => c[1] === "mcp" && c[2] === "add");
  assert.ok(add, `expected an mcp add call, got ${JSON.stringify(calls)}`);
  assert.deepEqual(add, [
    "claude", "mcp", "add", "--scope", "local", "--transport", "stdio",
    SERVER_NAME, "--", "npx", "-y", "@noetive/mcp-server",
  ]);
});

// Re-running init is how a user repairs an install. The CLI refuses a name it
// already knows, so without clearing first the second run fails on a setup that
// is actually fine.
test("an existing entry is cleared before it is re-added", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new ClaudeCodeAdapter(run).install(request(workspace));

  const removeIndex = calls.findIndex((c) => c[2] === "remove");
  const addIndex = calls.findIndex((c) => c[2] === "add");
  assert.ok(removeIndex !== -1, "the prior entry was never cleared");
  assert.ok(removeIndex < addIndex, "the entry was cleared after being added, which undoes the install");
});

// A CLI that fails must fail the command. Reporting success would tell the user
// their editor is configured when nothing was written.
test("a failing CLI is reported rather than swallowed", async () => {
  const { run } = recorder({ add: { status: 1, stdout: "", stderr: "scope 'local' is not recognised" } });
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await assert.rejects(
    () => new ClaudeCodeAdapter(run).install(request(workspace)),
    (err: Error) => {
      assert.match(err.message, /scope 'local' is not recognised/);
      return true;
    },
  );
});

// A user can have Claude Code without its CLI on PATH. Refusing to configure
// them at all would be worse than writing the layout we do know, so the adapter
// falls back to editing the file.
test("the file is edited when the CLI is not on PATH", async () => {
  const { run } = recorder({ version: { status: 127, stdout: "", stderr: "command not found" } });
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  const outcome = await new ClaudeCodeAdapter(run).install(request(workspace));

  assert.equal(outcome.target, join(workspace, ".claude.json"));
  const written = JSON.parse(readFileSync(join(workspace, ".claude.json"), "utf8"));
  assert.ok(written.mcpServers[SERVER_NAME], "no entry was written by the fallback");
});

// A dry run must not invoke the CLI. `claude mcp add` writes immediately, so
// running it to preview a change would make the change.
test("a dry run never invokes the CLI", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  const outcome = await new ClaudeCodeAdapter(run).install(request(workspace, true));

  const mutating = calls.filter((c) => c[2] === "add" || c[2] === "remove");
  assert.deepEqual(mutating, [], "a dry run invoked the CLI");
  assert.ok(outcome.diff, "a dry run reported no preview");
});

// Removing must go through the same CLI, or the entry it wrote is left behind
// in a layout the file edit does not fully understand.
test("removal is delegated to the CLI as well", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new ClaudeCodeAdapter(run).remove(request(workspace));

  assert.ok(
    calls.some((c) => c[2] === "remove" && c.includes(SERVER_NAME)),
    `expected an mcp remove call, got ${JSON.stringify(calls)}`,
  );
});

// Status reads the file even when the CLI is present: `claude mcp list` is a
// human-facing format with no stability promise, and parsing it would break on
// a wording change.
test("status reads the file rather than parsing CLI output", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));
  writeFileSync(
    join(workspace, ".claude.json"),
    JSON.stringify({ mcpServers: { [SERVER_NAME]: { command: "npx", args: ["-y", "@noetive/mcp-server"] } } }),
  );

  const report = await new ClaudeCodeAdapter(run).status(request(workspace));

  assert.equal(report.configured, true);
  assert.ok(
    !calls.some((c) => c.includes("list")),
    "status parsed CLI output, which has no stability promise",
  );
});

// An explicitly supplied key has to reach the CLI's environment, or the entry
// it writes references a variable the user never set.
test("an explicit API key is passed to the CLI environment", async () => {
  const calls: NodeJS.ProcessEnv[] = [];
  const run: Runner = (_command, args, env) => {
    if (args[1] === "add") calls.push(env);
    return { status: 0, stdout: "", stderr: "" };
  };
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new ClaudeCodeAdapter(run).install({ ...request(workspace), entryOptions: { apiKey: "keyu_example" } });

  assert.equal(calls.length, 1, "the CLI was not invoked");
  assert.equal(calls[0]?.NOETIVE_KEY_SECRET, "keyu_example");
});

// The fallback path must leave the same artefacts as any other file edit,
// including the backup that makes the write undoable.
test("the fallback still backs up an existing file", async () => {
  const { run } = recorder({ version: { status: 127, stdout: "", stderr: "" } });
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));
  writeFileSync(join(workspace, ".claude.json"), JSON.stringify({ mcpServers: {} }, null, 2));

  const outcome = await new ClaudeCodeAdapter(run).install(request(workspace));

  assert.ok(outcome.backup, "no backup was taken");
  assert.ok(existsSync(outcome.backup!), "the backup path does not exist");
});
