import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { MergeAdapter } from "../src/adapters/generic";
import { ClientSpec, SERVER_NAME } from "../src/clients";
import { backupPath } from "../src/configFile";

const cursor: ClientSpec = {
  displayName: "Cursor",
  configFormat: "json",
  topLevelKey: "mcpServers",
  install: "file-merge",
  expandsVariables: true,
  scopes: { project: { path: "${workspace}/mcp.json", default: true } },
  restartHint: "Reload Cursor.",
};

const copilot: ClientSpec = {
  displayName: "GitHub Copilot",
  configFormat: "jsonc",
  topLevelKey: "servers",
  install: "file-merge",
  expandsVariables: true,
  entryExtras: { type: "stdio" },
  scopes: { workspace: { path: "${workspace}/mcp.json", default: true } },
  restartHint: "Reload the window.",
};

function workspaceWith(contents?: string): string {
  const dir = mkdtempSync(join(tmpdir(), "noetive-installer-"));
  if (contents !== undefined) writeFileSync(join(dir, "mcp.json"), contents);
  return dir;
}

function request(spec: ClientSpec, workspace: string, dryRun = false) {
  const scope = Object.keys(spec.scopes)[0]!;
  return { spec, clientId: "test", scope, workspace, entryOptions: {}, dryRun };
}

// The single most damaging thing an installer can do is lose a user's other MCP
// servers. Every sibling entry and unrelated top-level key must survive.
test("merging preserves sibling servers and unrelated keys", async () => {
  const workspace = workspaceWith(
    JSON.stringify({ mcpServers: { other: { command: "other-server", args: ["--flag"] } }, theme: "dark" }, null, 2),
  );

  await new MergeAdapter().install(request(cursor, workspace));

  const after = JSON.parse(readFileSync(join(workspace, "mcp.json"), "utf8"));
  assert.deepEqual(after.mcpServers.other, { command: "other-server", args: ["--flag"] });
  assert.equal(after.theme, "dark");
  assert.ok(after.mcpServers[SERVER_NAME]);
});

// A user's comments are their notes about their own setup. Re-serialising the
// document would silently delete them, so the write is a range edit.
test("comments in a JSONC config survive the write", async () => {
  const workspace = workspaceWith(`{
  // keep this note
  "servers": {
    "other": { "command": "other-server" }
  }
}
`);

  await new MergeAdapter().install(request(copilot, workspace));

  const after = readFileSync(join(workspace, "mcp.json"), "utf8");
  assert.match(after, /\/\/ keep this note/);
  assert.match(after, /other-server/);
});

// Re-running init is the normal way a user upgrades or repairs an install. It
// must converge, not accumulate.
test("running init twice produces an identical file", async () => {
  const workspace = workspaceWith();
  const adapter = new MergeAdapter();

  await adapter.install(request(cursor, workspace));
  const first = readFileSync(join(workspace, "mcp.json"), "utf8");

  const second = await adapter.install(request(cursor, workspace));
  assert.equal(second.changed, false, "the second run reported a change");
  assert.equal(readFileSync(join(workspace, "mcp.json"), "utf8"), first);
});

// A dry run exists so a user can see what will happen to a file they care
// about. If it writes anything, it is not a dry run.
test("a dry run writes nothing and shows the change", async () => {
  const workspace = workspaceWith();

  const outcome = await new MergeAdapter().install(request(cursor, workspace, true));

  assert.equal(existsSync(join(workspace, "mcp.json")), false);
  assert.match(outcome.diff ?? "", new RegExp(SERVER_NAME));
});

// Removing must take out the Noetive entry and nothing else, or uninstalling
// Noetive would break the user's other tools.
test("remove deletes only the noetive entry", async () => {
  const workspace = workspaceWith(
    JSON.stringify({ mcpServers: { other: { command: "other-server" } } }, null, 2),
  );
  const adapter = new MergeAdapter();

  await adapter.install(request(cursor, workspace));
  await adapter.remove(request(cursor, workspace));

  const after = JSON.parse(readFileSync(join(workspace, "mcp.json"), "utf8"));
  assert.equal(after.mcpServers[SERVER_NAME], undefined);
  assert.deepEqual(after.mcpServers.other, { command: "other-server" });
});

// A corrupt config is the one case where writing is more dangerous than not
// writing: parsing it as empty would discard every server the user had.
test("a corrupt config is refused rather than overwritten", async () => {
  const workspace = workspaceWith(`{ "mcpServers": { "other": `);
  const before = readFileSync(join(workspace, "mcp.json"), "utf8");

  await assert.rejects(() => new MergeAdapter().install(request(cursor, workspace)), /not valid JSON/);
  assert.equal(readFileSync(join(workspace, "mcp.json"), "utf8"), before);
});

// The backup is what makes the write undoable. Without it a failed verification
// leaves the user with no way back.
test("writing an existing config leaves a backup of the original", async () => {
  const workspace = workspaceWith(JSON.stringify({ mcpServers: {} }, null, 2));
  const target = join(workspace, "mcp.json");
  const original = readFileSync(target, "utf8");

  const outcome = await new MergeAdapter().install(request(cursor, workspace));

  assert.equal(outcome.backup, backupPath(target));
  assert.equal(readFileSync(backupPath(target), "utf8"), original);
});

// Client-specific keys come from the manifest, so an editor that needs `type`
// or `autoApprove` is a data change rather than a code change.
test("manifest entry extras are written into the server entry", async () => {
  const workspace = workspaceWith();

  await new MergeAdapter().install(request(copilot, workspace));

  const after = JSON.parse(readFileSync(join(workspace, "mcp.json"), "utf8"));
  assert.equal(after.servers[SERVER_NAME].type, "stdio");
});

// A directory that does not exist yet is the normal first-install case for
// Kiro and Copilot; failing there would block a clean machine.
test("a missing config directory is created", async () => {
  const dir = mkdtempSync(join(tmpdir(), "noetive-installer-"));
  const nested: ClientSpec = { ...cursor, scopes: { project: { path: "${workspace}/deep/nested/mcp.json", default: true } } };

  await new MergeAdapter().install(request(nested, dir));

  assert.ok(existsSync(join(dir, "deep", "nested", "mcp.json")));
});

// status is what `list` and `doctor` report from; it must not claim an editor
// is configured when the file says otherwise.
test("status reports an unconfigured editor as unconfigured", async () => {
  const workspace = workspaceWith(JSON.stringify({ mcpServers: { other: {} } }));
  mkdirSync(join(workspace, ".cursor"), { recursive: true });

  const report = await new MergeAdapter().status(request(cursor, workspace));

  assert.equal(report.configured, false);
});

// Verification after a write is what makes the backup useful. A write that
// succeeded but produced an unusable entry is worse than a failed write,
// because nothing tells the user until their editor silently has no tools.
test("a write that does not produce a usable entry is rolled back", async () => {
  const workspace = workspaceWith(JSON.stringify({ mcpServers: { other: { command: "other" } } }, null, 2));
  const target = join(workspace, "mcp.json");
  const original = readFileSync(target, "utf8");

  // A manifest whose extras blank out the command: entryExtras are merged over
  // the entry, so a bad manifest entry writes a file that parses and describes
  // no runnable server. This is the shape verification exists to catch.
  const badManifest: ClientSpec = { ...cursor, entryExtras: { command: "" } };

  await assert.rejects(
    () => new MergeAdapter().install(request(badManifest, workspace)),
    /does not contain a usable/,
  );
  assert.equal(readFileSync(target, "utf8"), original, "the previous file was not restored");
});

// status is what `list` and `doctor` report from. Claiming an editor is
// configured when the entry is malformed sends a user looking for a problem
// somewhere else entirely.
test("status does not report a malformed entry as configured", async () => {
  const scenarios: Record<string, unknown> = {
    "entry is not an object": { mcpServers: { [SERVER_NAME]: "npx" } },
    "server map is an array": { mcpServers: [{ [SERVER_NAME]: {} }] },
    "no server map at all": { theme: "dark" },
  };

  for (const [name, contents] of Object.entries(scenarios)) {
    const workspace = workspaceWith(JSON.stringify(contents));
    const report = await new MergeAdapter().status(request(cursor, workspace));

    assert.equal(report.configured, false, `${name}: reported as configured`);
  }
});

// A corrupt file must not read as "configured" either, or doctor reports a
// healthy install on a config the editor cannot load.
test("status reports a corrupt config as unconfigured with the reason", async () => {
  const workspace = workspaceWith('{ "mcpServers": ');

  const report = await new MergeAdapter().status(request(cursor, workspace));

  assert.equal(report.configured, false);
  assert.match(report.detail ?? "", /not valid JSON/);
});

// Removing from a file that was never created is a no-op, not a failure — an
// idempotent cleanup script must not fail on the second run.
test("removing from a missing file reports no change", async () => {
  const workspace = mkdtempSync(join(tmpdir(), "noetive-installer-"));

  const outcome = await new MergeAdapter().remove(request(cursor, workspace));

  assert.equal(outcome.changed, false);
});

// A dry-run removal must leave the entry in place, or --dry-run removes the
// thing it was asked to preview removing.
test("a dry-run removal writes nothing", async () => {
  const workspace = workspaceWith();
  const adapter = new MergeAdapter();
  await adapter.install(request(cursor, workspace));
  const afterInstall = readFileSync(join(workspace, "mcp.json"), "utf8");

  const outcome = await adapter.remove(request(cursor, workspace, true));

  assert.equal(outcome.changed, true, "a dry run reported nothing to do");
  assert.equal(readFileSync(join(workspace, "mcp.json"), "utf8"), afterInstall);
});
