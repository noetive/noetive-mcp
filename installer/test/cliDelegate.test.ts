import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { CliDelegateAdapter, RunResult, Runner } from "../src/adapters/cliDelegate";
import { API_KEY_ENV } from "../src/serverEntry";
import { ClientSpec, SERVER_NAME, clientSpec } from "../src/clients";

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
    args: ["mcp", "add", "--scope", "${scope}", "--transport", "stdio", "${serverName}", "${env}", "--", "npx", "-y", "${packageName}"],
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
    SERVER_NAME, "--env", `${API_KEY_ENV}=\${${API_KEY_ENV}}`,
    "--", "npx", "-y", "@noetive/mcp-server",
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

  assert.equal(report.configured, "yes");
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

  assert.equal(report.configured, "yes");
  assert.ok(
    configured.calls.some((c) => c[1] === "mcp" && c[2] === "get" && c[3] === SERVER_NAME),
    `status never asked the CLI: ${JSON.stringify(configured.calls)}`,
  );

  const absent = recorder({ get: { status: 1, stdout: "", stderr: "no such server" } });
  const missing = await new CliDelegateAdapter(absent.run).status(codexRequest(mkdtempSync(join(tmpdir(), "noetive-codex-"))));

  assert.equal(missing.configured, "no");
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

/**
 * hermesRequest drives the *shipped* Hermes manifest rather than a fixture.
 *
 * The argv it produces is the whole of what `init --client hermes` does, so a
 * manifest edit that breaks it has nowhere else to show up. `HOME` is swapped
 * for a scratch directory because that manifest's scope is `~/.hermes`, and a
 * regression in the refusal would otherwise write JSON over the config file of
 * whoever is running the suite.
 */
function hermesRequest(home: string, entryOptions = {}) {
  process.env.HOME = home;
  process.env.USERPROFILE = home;
  return {
    spec: clientSpec("hermes"),
    clientId: "hermes",
    scope: "user",
    workspace: mkdtempSync(join(tmpdir(), "noetive-hermes-ws-")),
    entryOptions,
    dryRun: false,
  };
}

async function withScratchHome<T>(body: (home: string) => Promise<T>): Promise<T> {
  const previous = { home: process.env.HOME, profile: process.env.USERPROFILE };
  try {
    return await body(mkdtempSync(join(tmpdir(), "noetive-hermes-home-")));
  } finally {
    process.env.HOME = previous.home;
    process.env.USERPROFILE = previous.profile;
  }
}

// Hermes parses `--args` as argparse's REMAINDER, so it takes everything after
// it and has to come last, with each argument its own word. Joining them into
// one string is accepted silently and writes args: ["-y @noetive/mcp-server"],
// which npx resolves as a package by that literal name and never finds — an
// install that reports success and produces a server that cannot start.
test("the shipped Hermes entry produces the invocation its CLI parses", async () => {
  const calls = await withScratchHome(async (home) => {
    const { run, calls } = recorder();
    await withTerminal(() => new CliDelegateAdapter(run).install(hermesRequest(home)));
    return calls;
  });

  assert.deepEqual(addCall(calls), [
    "hermes", "mcp", "add", SERVER_NAME,
    "--env", `${API_KEY_ENV}=\${${API_KEY_ENV}}`,
    "--command", "npx", "--args", "-y", "@noetive/mcp-server",
  ]);
});

// Hermes' --env is argparse nargs="*", which stores rather than appends: a
// second --env replaces the pairs the first one carried. Repeating the flag
// therefore keeps only the last pair, and the API key is written first, so it
// is the one that disappears — into a server that connects and then refuses
// every call, since Hermes passes a stdio server nothing but its declared env.
test("Hermes takes every environment pair after one flag, not one flag each", async () => {
  const calls = await withScratchHome(async (home) => {
    const { run, calls } = recorder();
    await withTerminal(() =>
      new CliDelegateAdapter(run).install(
        hermesRequest(home, { apiKey: "keyu_example", targeting: { namespace: "team" }, disableGlobalNamespace: true }),
      ),
    );
    return calls;
  });

  const add = addCall(calls)!;
  assert.equal(add.filter((arg) => arg === "--env").length, 1, `--env was repeated: ${JSON.stringify(add)}`);

  const pairs = add.slice(add.indexOf("--env") + 1, add.indexOf("--command"));
  assert.deepEqual(pairs, [
    `${API_KEY_ENV}=keyu_example`,
    "NOETIVE_NAMESPACE=team",
    "NOETIVE_DISABLE_GLOBAL_NS=1",
  ]);
});

// ~/.hermes/config.yaml is the whole agent's configuration, not an MCP file.
// Falling back to the JSON merger would overwrite the model, the profiles and
// the approval settings with a document Hermes cannot parse, so the absence of
// the command has to be a refusal.
test("Hermes refuses rather than writing JSON over the agent's own configuration", async () => {
  await withScratchHome(async (home) => {
    const { run } = recorder({ version: { status: 127, stdout: "", stderr: "command not found" } });
    const request = hermesRequest(home);

    await assert.rejects(
      () => new CliDelegateAdapter(run).install(request),
      (err: Error) => {
        assert.match(err.message, /hermes/);
        assert.match(err.message, /not on PATH/);
        return true;
      },
    );

    // The scope is ~/.hermes/config.yaml, so the home directory is where a
    // regression would land. Asserting on the workspace — as the Codex case
    // legitimately does — would pass no matter what the merger did.
    assert.equal(existsSync(join(home, ".hermes")), false, "a refused install still wrote to the config directory");
  });
});

// A prompting CLI must be given the terminal, or its question goes to a pipe
// the user cannot see and it answers itself.
test("an interactive CLI is run on the terminal, and the availability probe is not", async () => {
  await withScratchHome(async (home) => {
    const modes: { argv: string[]; interactive: boolean | undefined }[] = [];
    const run: Runner = (command, args, _env, interactive) => {
      modes.push({ argv: [command, ...args], interactive });
      return { status: 0, stdout: "", stderr: "" };
    };

    await withTerminal(() => new CliDelegateAdapter(run).install(hermesRequest(home)));

    const add = modes.find((m) => m.argv[2] === "add");
    const probe = modes.find((m) => m.argv[1] === "--version");
    assert.equal(add?.interactive, true, "mcp add was not given the terminal");
    assert.ok(!probe?.interactive, "the --version probe took over the terminal");
  });
});

// Hermes exits zero whether it saved the server or the user cancelled out of
// the tool picker. Reading its output to tell the two apart would bind this
// installer to the layout of somebody else's terminal interface, where a
// cosmetic change turns a working install into a reported failure. So the
// outcome says what we did and declines to say what Hermes did.
test("an interactive install reports what it handed over, not a success it cannot see", async () => {
  const outcome = await withScratchHome(async (home) => {
    const { run } = recorder();
    return withTerminal(() => new CliDelegateAdapter(run).install(hermesRequest(home)));
  });

  assert.ok(outcome.unverified, "an interactive install claimed an outcome it cannot observe");
  assert.match(outcome.unverified!, /does not report back/);
});

// Without a terminal the prompt reads EOF, Hermes takes the cancelling answer
// and exits zero having written nothing — so a piped run would report a
// configured editor that was never configured. The refusal has to name what to
// do instead, or it is just a different way of leaving the user stuck.
test("no terminal refuses the interactive install and hands back the entry", async () => {
  await withScratchHome(async (home) => {
    const { run, calls } = recorder();

    await assert.rejects(
      () => new CliDelegateAdapter(run).install(hermesRequest(home)),
      (err: Error) => {
        assert.match(err.message, /needs a terminal/);
        assert.match(err.message, /nothing was written/);
        assert.match(err.message, /mcp_servers:/, "the refusal did not carry the entry to paste");
        assert.match(err.message, /"-y", "@noetive\/mcp-server"/);
        assert.match(err.message, new RegExp(`${API_KEY_ENV}`));
        return true;
      },
    );

    assert.deepEqual(calls.filter((c) => c[2] === "add"), [], "the CLI was invoked with no terminal to answer it");
  });
});

// --api-key --dry-run is exactly what a careful person runs before committing
// to anything, and the delegated path put the key straight into their
// scrollback while the file path had redacted it for releases.
test("an embedded key is kept off the screen on both the preview and the failure", async () => {
  const workspace = mkdtempSync(join(tmpdir(), "noetive-claude-"));

  const preview = await new CliDelegateAdapter(recorder().run).install({
    ...request(workspace, true),
    entryOptions: { apiKey: "keyu_supersecret" },
  });
  assert.ok(!preview.diff!.includes("keyu_supersecret"), `the preview printed the key: ${preview.diff}`);
  assert.match(preview.diff!, /<your key>/);

  const { run } = recorder({ add: { status: 1, stdout: "", stderr: "nope" } });
  await assert.rejects(
    () => new CliDelegateAdapter(run).install({ ...request(workspace), entryOptions: { apiKey: "keyu_supersecret" } }),
    (err: Error) => {
      assert.ok(!err.message.includes("keyu_supersecret"), `the failure printed the key: ${err.message}`);
      return true;
    },
  );
});

/** withTerminal runs body with stdin and stdout claiming to be a terminal. */
async function withTerminal<T>(body: () => Promise<T>): Promise<T> {
  const previous = { stdin: process.stdin.isTTY, stdout: process.stdout.isTTY };
  process.stdin.isTTY = true;
  process.stdout.isTTY = true;
  try {
    return await body();
  } finally {
    process.stdin.isTTY = previous.stdin;
    process.stdout.isTTY = previous.stdout;
  }
}

/** adjacent reports whether flag is immediately followed by value in argv. */
function adjacent(argv: readonly string[], flag: string, value: string): boolean {
  return argv.some((arg, i) => arg === flag && argv[i + 1] === value);
}

// The real `claude mcp add` declares `-e, --env <env...>`, which is variadic:
// it keeps consuming arguments until the next flag. Spliced in ahead of the
// server name it swallows the name itself, and the CLI refuses the whole
// install with "Invalid environment variable format: noetive". Every editor
// config written through this path depends on the order being the other way
// round, and nothing else in the suite would notice it flipping back.
test("environment pairs come after the server name, never before it", async () => {
  const { run, calls } = recorder();
  await new CliDelegateAdapter(run).install({
    spec: claudeCode,
    clientId: "claude-code",
    scope: "local",
    workspace: mkdtempSync(join(tmpdir(), "noetive-claude-")),
    entryOptions: { targeting: { namespace: "team" }, disableGlobalNamespace: true },
    dryRun: false,
  });

  const add = addCall(calls)!;
  const name = add.indexOf(SERVER_NAME);
  const firstEnv = add.indexOf("--env");
  const terminator = add.indexOf("--");

  assert.ok(name >= 0, `the server name is missing: ${JSON.stringify(add)}`);
  assert.ok(firstEnv >= 0, `no environment was passed: ${JSON.stringify(add)}`);
  assert.ok(name < firstEnv, `the server name must precede --env: ${JSON.stringify(add)}`);
  assert.ok(firstEnv < terminator, `--env must be terminated by --: ${JSON.stringify(add)}`);
});

// The shared-namespace decision reaches the CLI path as well as the file path.
// Deriving the environment twice is how the CLI path came to silently drop
// settings while reporting that it had written them.
test("the shared-namespace decision reaches the CLI as an environment pair", async () => {
  for (const [disabled, expected] of [[true, "1"], [false, "0"]] as const) {
    const { run, calls } = recorder();
    await new CliDelegateAdapter(run).install({
      spec: claudeCode,
      clientId: "claude-code",
      scope: "local",
      workspace: mkdtempSync(join(tmpdir(), "noetive-claude-")),
      entryOptions: { disableGlobalNamespace: disabled },
      dryRun: false,
    });

    assert.ok(
      adjacent(addCall(calls)!, "--env", `NOETIVE_DISABLE_GLOBAL_NS=${expected}`),
      `expected NOETIVE_DISABLE_GLOBAL_NS=${expected}`,
    );
  }
});
