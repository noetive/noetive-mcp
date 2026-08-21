import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { CliDelegateAdapter, RunResult, Runner, systemRunner } from "../src/adapters/cliDelegate";
import { MergeAdapter } from "../src/adapters/generic";
import { ClientSpec, SERVER_NAME } from "../src/clients";
import { run } from "../src/cli";

const cursor: ClientSpec = {
  displayName: "Cursor",
  configFormat: "json",
  topLevelKey: "mcpServers",
  install: "file-merge",
  expandsVariables: true,
  scopes: { project: { path: "${workspace}/mcp.json", default: true } },
  restartHint: "Reload Cursor.",
};

function request(spec: ClientSpec, workspace: string, dryRun = false) {
  return { spec, clientId: "test", scope: "project", workspace, entryOptions: {}, dryRun };
}

function scratch(contents?: string): string {
  const dir = mkdtempSync(join(tmpdir(), "noetive-outcomes-"));
  if (contents !== undefined) writeFileSync(join(dir, "mcp.json"), contents);
  return dir;
}

// The scope name and the server name reaching this runner originate in user
// input. Through a shell, a semicolon or a backtick in any of them would
// execute as a command; without one they can only ever be arguments.
test("arguments are never interpreted by a shell", () => {
  const injected = "harmless; echo INJECTED";

  const result = systemRunner(process.execPath, ["-p", "process.argv[1]", injected], process.env);

  assert.equal(result.status, 0, `the probe did not run: ${result.stderr}`);
  assert.match(result.stdout, /harmless; echo INJECTED/, "the argument did not arrive verbatim");
  assert.doesNotMatch(result.stdout, /^INJECTED$/m, "the argument was executed as a command");
});

// `changed` is what the command tells the user: "Configured Cursor" versus
// "already configured. Nothing to do." Reporting the wrong one sends someone
// looking for a change that never happened, or vice versa.
test("a first install reports a change and a repeat does not", async () => {
  const workspace = scratch();
  const adapter = new MergeAdapter();

  assert.equal((await adapter.install(request(cursor, workspace))).changed, true);
  assert.equal((await adapter.install(request(cursor, workspace))).changed, false);
});

test("a dry run reports the change it would make", async () => {
  const outcome = await new MergeAdapter().install(request(cursor, scratch(), true));

  assert.equal(outcome.changed, true, "a dry run reported nothing to do");
});

// Removing something present is a change; removing something absent is not.
// A cleanup script keys its output off exactly this.
test("removal reports a change only when there was something to remove", async () => {
  const workspace = scratch();
  const adapter = new MergeAdapter();
  await adapter.install(request(cursor, workspace));

  assert.equal((await adapter.remove(request(cursor, workspace))).changed, true);
  assert.equal((await adapter.remove(request(cursor, workspace))).changed, false);
});

// A config file that does not exist yet is the ordinary first-run state, and it
// must not read as configured — that is the branch `doctor` uses to decide
// whether anything is set up at all.
test("a missing config is reported as unconfigured", async () => {
  const report = await new MergeAdapter().status(request(cursor, scratch()));

  assert.equal(report.configured, false);
});

// A configured editor has to report the command it will actually run, since
// that is what a user compares against what they expected.
test("a configured editor reports the command it will run", async () => {
  const workspace = scratch();
  await new MergeAdapter().install(request(cursor, workspace));

  const report = await new MergeAdapter().status(request(cursor, workspace));

  assert.equal(report.configured, true);
  assert.match(report.detail ?? "", /npx -y @noetive\/mcp-server/);
});

// A server map that is an array is valid JSON and not a usable config. Reading
// an entry out of it would report an editor as configured on a file no editor
// can load.
test("a server map that is not an object is not read as configured", async () => {
  const workspace = scratch(JSON.stringify({ mcpServers: [{ [SERVER_NAME]: { command: "npx" } }] }));

  const report = await new MergeAdapter().status(request(cursor, workspace));

  assert.equal(report.configured, false);
});

// `list` distinguishes configured from not. Reporting everything as configured
// would tell a user with nothing installed that they are done.
test("list distinguishes a configured editor from an unconfigured one", async () => {
  const workspace = scratch();
  const cwd = process.cwd();
  process.chdir(workspace);
  try {
    const before: string[] = [];
    await run(["list", "--json"], (l) => before.push(l), () => {});
    const parsed = JSON.parse(before.join("\n")) as { client: string; configured: boolean }[];

    assert.ok(
      parsed.every((row) => row.configured === false),
      "an editor was reported as configured before anything was installed",
    );
  } finally {
    process.chdir(cwd);
  }
});

// doctor's editor rows drive the same distinction, and its exit code depends on
// whether any editor is configured at all.
test("doctor reports no editor as configured on a clean machine", async () => {
  const workspace = scratch();
  const cwd = process.cwd();
  process.chdir(workspace);
  try {
    const out: string[] = [];
    await run(["doctor", "--json"], (l) => out.push(l), () => {});
    const checks = JSON.parse(out.join("\n")) as { name: string; status: string }[];

    assert.ok(
      !checks.some((c) => c.name === "Cursor" && c.status === "pass"),
      "an unconfigured editor was reported as passing",
    );
  } finally {
    process.chdir(cwd);
  }
});

// The CLI is used only when it is actually present. Treating a missing binary
// as available makes every install fail with a spawn error instead of falling
// back to the file the adapter knows how to write.
test("a client with no CLI configured falls back to the file", async () => {
  const runner: Runner = () => ({ status: 0, stdout: "", stderr: "" });
  const withoutCli: ClientSpec = { ...cursor, install: "cli-delegate" };

  const workspace = scratch();
  const outcome = await new CliDelegateAdapter(runner).install(request(withoutCli, workspace));

  assert.equal(outcome.target, join(workspace, "mcp.json"), "the file fallback was not used");
});

// A remove that the CLI rejects has not changed anything, and saying otherwise
// tells a user their entry is gone when it is still there.
test("a rejected CLI removal reports no change", async () => {
  const outcomes: Record<string, RunResult> = { remove: { status: 1, stdout: "", stderr: "no such server" } };
  const runner: Runner = (_c, args) => {
    if (args[0] === "--version") return { status: 0, stdout: "", stderr: "" };
    return outcomes[args[1] ?? ""] ?? { status: 0, stdout: "", stderr: "" };
  };

  const claudeCode: ClientSpec = {
    ...cursor,
    displayName: "Claude Code",
    install: "cli-delegate",
    cli: {
      command: "claude",
      args: ["mcp", "add", "${serverName}"],
      removeArgs: ["mcp", "remove", "${serverName}"],
    },
  };

  const outcome = await new CliDelegateAdapter(runner).remove(request(claudeCode, scratch()));

  assert.equal(outcome.changed, false, "a rejected removal was reported as a change");
});
