import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { CliDelegateAdapter, RunResult, Runner } from "../src/adapters/cliDelegate";
import { API_KEY_ENV } from "../src/serverEntry";
import { ClientSpec, SERVER_NAME } from "../src/clients";

// A stand-in for the editor's binary. Every test states what the CLI does and
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
    args: ["mcp", "add", "--scope", "${scope}", "--transport", "stdio", "${env}", "${serverName}", "--", "npx", "-y", "${packageName}"],
    removeArgs: ["mcp", "remove", "--scope", "${scope}", "${serverName}"],
    envArg: "--env",
    fallback: "file-merge",
  },
  scopes: { local: { path: "${workspace}/.claude.json", default: true } },
  restartHint: "Start a new Claude Code session.",
};

// Codex keeps its servers in TOML, so there is no file this installer can edit.
// Its entry is the reason fallback and statusArgs exist.
const codex: ClientSpec = {
  displayName: "Codex",
  configFormat: "toml",
  topLevelKey: "mcp_servers",
  install: "cli-delegate",
  expandsVariables: false,
  cli: {
    command: "codex",
    args: ["mcp", "add", "${serverName}", "${env}", "--", "npx", "-y", "${packageName}"],
    removeArgs: ["mcp", "remove", "${serverName}"],
    statusArgs: ["mcp", "get", "${serverName}"],
    envArg: "--env",
    fallback: "none",
  },
  scopes: { user: { path: "${workspace}/config.toml", default: true } },
  restartHint: "Start a new Codex session.",
};

function request(workspace: string, dryRun = false, spec: ClientSpec = claudeCode) {
  return { spec, clientId: "claude-code", scope: "local", workspace, entryOptions: {}, dryRun };
}

function codexRequest(workspace: string, dryRun = false) {
  return { spec: codex, clientId: "codex", scope: "user", workspace, entryOptions: {}, dryRun };
}

function addCall(calls: string[][]): string[] | undefined {
  return calls.find((c) => c[1] === "mcp" && c[2] === "add");
}

// Claude Code keys user-scope entries per project inside ~/.claude.json, a
// layout the file path alone does not describe and one that has already changed
// between releases. Delegating means we never encode that private layout.
test("the editor's own CLI is used when it is available", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new CliDelegateAdapter(run).install(request(workspace));

  const add = addCall(calls);
  assert.ok(add, `expected an mcp add call, got ${JSON.stringify(calls)}`);
  assert.deepEqual(add, [
    "claude", "mcp", "add", "--scope", "local", "--transport", "stdio",
    "--env", `${API_KEY_ENV}=\${${API_KEY_ENV}}`,
    SERVER_NAME, "--", "npx", "-y", "@noetive/mcp-server",
  ]);
});

// Re-running init is how a user repairs an install. The CLI refuses a name it
// already knows, so without clearing first the second run fails on a setup that
// is actually fine.
test("an existing entry is cleared before it is re-added", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new CliDelegateAdapter(run).install(request(workspace));

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
    () => new CliDelegateAdapter(run).install(request(workspace)),
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

  const outcome = await new CliDelegateAdapter(run).install(request(workspace));

  assert.equal(outcome.target, join(workspace, ".claude.json"));
  const written = JSON.parse(readFileSync(join(workspace, ".claude.json"), "utf8"));
  assert.ok(written.mcpServers[SERVER_NAME], "no entry was written by the fallback");
});

// A dry run must not invoke the CLI. `claude mcp add` writes immediately, so
// running it to preview a change would make the change.
test("a dry run never invokes the CLI", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  const outcome = await new CliDelegateAdapter(run).install(request(workspace, true));

  const mutating = calls.filter((c) => c[2] === "add" || c[2] === "remove");
  assert.deepEqual(mutating, [], "a dry run invoked the CLI");
  assert.ok(outcome.diff, "a dry run reported no preview");
});

// Removing must go through the same CLI, or the entry it wrote is left behind
// in a layout the file edit does not fully understand.
test("removal is delegated to the CLI as well", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new CliDelegateAdapter(run).remove(request(workspace));

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

  const report = await new CliDelegateAdapter(run).status(request(workspace));

  assert.equal(report.configured, true);
  assert.ok(
    !calls.some((c) => c.includes("list")),
    "status parsed CLI output, which has no stability promise",
  );
});

// The key has to land in the entry the CLI writes, which means it has to be an
// argument. Setting it in the environment of the `claude` process instead looks
// right from inside this adapter and configures nothing: the CLI does not copy
// ambient variables into a server entry, so the user was told their key had
// been written while every tool call came back unauthorized.
test("an explicit API key is written into the entry as an argument", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new CliDelegateAdapter(run).install({ ...request(workspace), entryOptions: { apiKey: "keyu_example" } });

  const add = addCall(calls);
  assert.ok(add, "the CLI was not invoked");
  assert.ok(
    adjacent(add, "--env", `${API_KEY_ENV}=keyu_example`),
    `the key never reached the entry: ${JSON.stringify(add)}`,
  );
});

// The namespace triple travels the same road as the key and was dropped by the
// same bug. An install that silently forgets --namespace leaves every tool call
// having to name one, which is exactly what configuring it was meant to avoid.
test("targeting options are written into the entry as arguments", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new CliDelegateAdapter(run).install({
    ...request(workspace),
    entryOptions: { targeting: { namespace: "team", model: "Qwen3-Embedding-4B", dimensions: "1024" } },
  });

  const add = addCall(calls);
  assert.ok(add, "the CLI was not invoked");
  assert.ok(adjacent(add, "--env", "NOETIVE_NAMESPACE=team"), `namespace missing: ${JSON.stringify(add)}`);
  assert.ok(adjacent(add, "--env", "NOETIVE_MODEL=Qwen3-Embedding-4B"), `model missing: ${JSON.stringify(add)}`);
  assert.ok(adjacent(add, "--env", "NOETIVE_DIMENSIONS=1024"), `dimensions missing: ${JSON.stringify(add)}`);
});

// Every one of these CLIs takes its flags before the `--` that introduces the
// launch command. A flag placed after it is handed to npx instead of the
// editor, which fails in a way that points at the wrong thing entirely.
test("env flags are placed before the launch command", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  await new CliDelegateAdapter(run).install({ ...request(workspace), entryOptions: { apiKey: "keyu_example" } });

  const add = addCall(calls)!;
  assert.ok(add.indexOf("--env") < add.indexOf("--"), `--env came after the separator: ${JSON.stringify(add)}`);
});

// Nothing to pass must produce no flag at all. A bare `--env` with no pair
// would be rejected by the CLI, turning a perfectly ordinary install — an
// editor that cannot expand variables, run without --api-key — into a failure.
test("no environment to set produces no env flag", async () => {
  const { run, calls } = recorder();
  const workspace = mkdtempSync(join(tmpdir(), "noetive-codex-"));

  await new CliDelegateAdapter(run).install(codexRequest(workspace));

  const add = addCall(calls);
  assert.deepEqual(add, ["codex", "mcp", "add", SERVER_NAME, "--", "npx", "-y", "@noetive/mcp-server"]);
});

// Codex keeps its servers in TOML. Falling back to the JSON merger would
// replace a working config.toml with a JSON document Codex cannot read, so the
// absence of the command has to be a refusal.
test("an editor with no writable config refuses rather than falling back", async () => {
  const { run } = recorder({ version: { status: 127, stdout: "", stderr: "command not found" } });
  const workspace = mkdtempSync(join(tmpdir(), "noetive-codex-"));

  await assert.rejects(
    () => new CliDelegateAdapter(run).install(codexRequest(workspace)),
    (err: Error) => {
      assert.match(err.message, /codex/);
      assert.match(err.message, /not on PATH/);
      return true;
    },
  );

  assert.deepEqual(readdirSync(workspace), [], "a refused install still wrote to disk");
});

// The same refusal applies to removal: a fallback that "removes" the entry from
// a JSON file Codex never reads would report success and leave the real entry
// in place.
test("removal refuses too when there is no writable config", async () => {
  const { run } = recorder({ version: { status: 127, stdout: "", stderr: "command not found" } });
  const workspace = mkdtempSync(join(tmpdir(), "noetive-codex-"));

  await assert.rejects(() => new CliDelegateAdapter(run).remove(codexRequest(workspace)), /not on PATH/);
});

// With no file to read, status is the CLI's exit code. Reporting "not
// configured" instead would make `doctor` fail on a working Codex install and
// send the user to fix something that is already right.
test("status falls to the CLI's exit code when there is no file to read", async () => {
  const configured = recorder();
  const report = await new CliDelegateAdapter(configured.run).status(codexRequest(mkdtempSync(join(tmpdir(), "noetive-codex-"))));

  assert.equal(report.configured, true);
  assert.ok(
    configured.calls.some((c) => c[1] === "mcp" && c[2] === "get" && c[3] === SERVER_NAME),
    `status never asked the CLI: ${JSON.stringify(configured.calls)}`,
  );

  const absent = recorder({ get: { status: 1, stdout: "", stderr: "no such server" } });
  const missing = await new CliDelegateAdapter(absent.run).status(codexRequest(mkdtempSync(join(tmpdir(), "noetive-codex-"))));

  assert.equal(missing.configured, false);
});

// The fallback path must leave the same artefacts as any other file edit,
// including the backup that makes the write undoable.
test("the fallback still backs up an existing file", async () => {
  const { run } = recorder({ version: { status: 127, stdout: "", stderr: "" } });
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));
  writeFileSync(join(workspace, ".claude.json"), JSON.stringify({ mcpServers: {} }, null, 2));

  const outcome = await new CliDelegateAdapter(run).install(request(workspace));

  assert.ok(outcome.backup, "no backup was taken");
  assert.ok(existsSync(outcome.backup!), "the backup path does not exist");
});

/** adjacent reports whether flag is immediately followed by value in argv. */
function adjacent(argv: readonly string[], flag: string, value: string): boolean {
  return argv.some((arg, i) => arg === flag && argv[i + 1] === value);
}
