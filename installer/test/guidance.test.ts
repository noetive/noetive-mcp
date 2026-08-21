import assert from "node:assert/strict";
import { homedir } from "node:os";
import { existsSync, mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, normalize, sep } from "node:path";
import { test } from "node:test";

import { MergeAdapter } from "../src/adapters/generic";
import { configuredScopes } from "../src/cli";
import { assertUsableWorkspace, clientIds, clientSpec, configPath, defaultScope, isInstalled } from "../src/clients";
import { API_KEY_ENV, DASHBOARD_URL, describeKeyHandling } from "../src/serverEntry";

// The published instruction is "run this in your terminal", which for most
// people means their home directory. Copilot's only scope writes into the
// current directory, so that produced ~/.vscode/mcp.json — a file VS Code never
// reads as a workspace config — and reported success. The user was left with a
// config file, an editor with no tools, and nothing connecting the two.
test("a project-scoped install from the home directory is refused", () => {
  const copilot = clientSpec("copilot");

  assert.throws(
    () => assertUsableWorkspace(copilot, defaultScope(copilot), homedir()),
    (err: Error) => {
      assert.match(err.message, /home directory/);
      assert.match(err.message, /project directory/);
      return true;
    },
  );
});

test("a project-scoped install from a real project is allowed", () => {
  const copilot = clientSpec("copilot");
  const project = mkdtempSync(join(tmpdir(), "noetive-project-"));

  assert.doesNotThrow(() => assertUsableWorkspace(copilot, defaultScope(copilot), project));
});

// Cursor's default scope is the user's home config, which is a legitimate place
// to run from. The guard must not fire there.
test("a home-scoped install from the home directory is allowed", () => {
  const cursor = clientSpec("cursor");

  assert.doesNotThrow(() => assertUsableWorkspace(cursor, defaultScope(cursor), homedir()));
});

// Nothing else in the install flow tells a first-time user that an API key
// exists or where to get one. Without it they get an editor that connects
// successfully and then refuses every call.
test("install guidance names where to get a key", () => {
  for (const id of clientIds()) {
    assert.match(describeKeyHandling(clientSpec(id), id), new RegExp(DASHBOARD_URL.replace(/\//g, "\\/")));
  }
});

// A desktop-launched editor does not inherit shell exports, so telling someone
// to export the variable without saying that is advice that quietly fails on
// the most common setup.
test("guidance warns that desktop launchers do not see shell exports", () => {
  const guidance = describeKeyHandling(clientSpec("cursor"), "cursor");

  assert.match(guidance, new RegExp(API_KEY_ENV));
  assert.match(guidance, /desktop/i);
});

// Kiro cannot expand the placeholder at all, so its guidance has to be the
// command that actually works rather than an export that never takes effect.
test("guidance for an editor that cannot expand variables gives the working command", () => {
  const guidance = describeKeyHandling(clientSpec("kiro"), "kiro");

  assert.match(guidance, /--api-key/);
  assert.doesNotMatch(guidance, /export NOETIVE_KEY_SECRET=/);
});

// That guidance ends in a command the user is meant to run. Naming a fixed
// editor in it sends a Codex user to configure Kiro — an editor they may not
// have — while their own install stays keyless and every tool call fails.
test("guidance names the editor being configured, not a fixed one", () => {
  for (const id of clientIds().filter((c) => !clientSpec(c).expandsVariables)) {
    assert.match(describeKeyHandling(clientSpec(id), id), new RegExp(`--client ${id}\\b`));
  }
});

// An editor can be configured per-project rather than globally, and which entry
// applies depends on where the editor was opened. A report that only inspects
// the default scope tells a project-scoped user they are not configured, and
// sends them to fix something that already works.
test("a project-scoped install is found, not only the default scope", async () => {
  const project = mkdtempSync(join(tmpdir(), "noetive-scopes-"));
  const cursor = clientSpec("cursor");

  await new MergeAdapter().install({
    spec: cursor,
    clientId: "cursor",
    scope: "project",
    workspace: project,
    entryOptions: {},
    dryRun: false,
  });

  const found = await configuredScopes("cursor", project);

  assert.deepEqual(
    found.map((f) => f.scope),
    ["project"],
    "expected the project scope to be reported as configured",
  );
});

// The manifest spells paths with forward slashes because vendor documentation
// does. Substituting a Windows workspace into that leaves mixed separators —
// `C:\Users\me\project/.vscode/mcp.json` — which opens files perfectly well and
// then fails every comparison against a path built with join, so `list` and
// `doctor` report an editor as unconfigured while looking straight at its
// config.
test("a resolved config path uses this platform's separator throughout", () => {
  const workspace = mkdtempSync(join(tmpdir(), "noetive-paths-"));

  for (const id of clientIds()) {
    const spec = clientSpec(id);
    for (const scope of Object.keys(spec.scopes)) {
      const resolved = configPath(spec, scope, workspace);

      assert.equal(resolved, normalize(resolved), `${id}/${scope} is not normalized: ${resolved}`);
      if (sep === "\\") {
        assert.ok(!resolved.includes("/"), `${id}/${scope} carries a forward slash: ${resolved}`);
      }
    }
  }
});

// The same path has to come out of the detector, or an editor is "not detected"
// on the machine it is installed on.
test("detection and configuration agree on where a workspace is", () => {
  const workspace = mkdtempSync(join(tmpdir(), "noetive-paths-"));
  const copilot = clientSpec("copilot");

  mkdirSync(join(workspace, ".vscode"), { recursive: true });

  assert.ok(
    isInstalled(copilot, workspace, existsSync),
    "the workspace .vscode directory was not detected",
  );
});
