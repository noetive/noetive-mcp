import assert from "node:assert/strict";
import { test } from "node:test";

import { clientIds, clientSpec, defaultScope, PACKAGE_NAME } from "../src/clients";
import { API_KEY_ENV, buildEntry, describeKeyHandling } from "../src/serverEntry";

// The package name is a public contract: it appears in the commands on
// noetive.io/mcp, in dev-docs, and inside the Add to Kiro deeplink. If the
// manifest and the published package ever disagree, every advertised install
// command fails.
test("the manifest names the package advertised to users", () => {
  assert.equal(PACKAGE_NAME, "@noetive/mcp-server");
});

// An editor spawns the server with no terminal attached. Without -y, npx blocks
// on an install prompt nobody can answer, and the user sees an editor that
// never connects rather than an error.
test("the written command runs npx non-interactively", () => {
  const entry = buildEntry(clientSpec("cursor"));

  assert.equal(entry.command, "npx");
  assert.deepEqual(entry.args, ["-y", PACKAGE_NAME]);
});

// The key must not be written to a config file just because it happens to be in
// the environment: those files get synced, committed and screen-shared.
test("no API key is written unless one is passed explicitly", () => {
  const entry = buildEntry(clientSpec("cursor"));

  assert.equal(entry.env?.[API_KEY_ENV], `\${${API_KEY_ENV}}`);
});

test("an explicitly passed API key is written literally", () => {
  const entry = buildEntry(clientSpec("cursor"), { apiKey: "keyu_example" });

  assert.equal(entry.env?.[API_KEY_ENV], "keyu_example");
});

// Kiro does not expand ${VAR} in env. Writing the placeholder anyway would put
// the literal string "${NOETIVE_KEY_SECRET}" on the wire as an API key, which
// fails with an unauthorized error that points nowhere useful.
test("no placeholder is written for an editor that cannot expand it", () => {
  const entry = buildEntry(clientSpec("kiro"));

  assert.equal(entry.env?.[API_KEY_ENV], undefined);
});

// Writing a key into a dotfile is a decision with consequences the user should
// hear about once, at the moment they make it.
test("embedding a key warns about version control", () => {
  assert.match(describeKeyHandling(clientSpec("cursor"), "cursor", { apiKey: "keyu_x" }), /version control/);
});

// Routing defaults are the operator naming a namespace, which is different from
// the server guessing one. They belong in the config the editor launches with.
test("configured targeting is written into the environment block", () => {
  const entry = buildEntry(clientSpec("cursor"), {
    targeting: { namespace: "incidents", model: "model-a", dimensions: "1024" },
  });

  assert.equal(entry.env?.NOETIVE_NAMESPACE, "incidents");
  assert.equal(entry.env?.NOETIVE_MODEL, "model-a");
  assert.equal(entry.env?.NOETIVE_DIMENSIONS, "1024");
});

// Every advertised editor must be present and answerable, since each has a
// published install command or button pointing at it.
test("every advertised client is in the manifest with a usable default scope", () => {
  for (const id of clientIds()) {
    const spec = clientSpec(id);
    const scope = defaultScope(spec);
    assert.ok(spec.scopes[scope], `${id} has no scope named ${scope}`);
    assert.ok(spec.restartHint.length > 0, `${id} has no restart hint`);
  }

  // Pinned rather than derived: the set is what noetive.io/mcp publishes a
  // command for, so gaining or losing one is a change to a public promise and
  // should not pass quietly.
  assert.deepEqual(clientIds().sort(), ["antigravity", "claude-code", "codex", "copilot", "cursor", "kiro"]);
});

// The file merger writes JSON. Pointing it at an editor whose config is TOML
// would replace a working config.toml with a JSON document that editor cannot
// read — and the merger's own read-back check would pass, because the JSON it
// wrote is exactly the JSON it looks for. The schema states this; nothing runs
// the schema, so it is asserted here.
test("an editor whose config is not JSON is never handed to the file merger", () => {
  for (const id of clientIds()) {
    const spec = clientSpec(id);
    if (spec.configFormat === "json" || spec.configFormat === "jsonc") continue;

    assert.equal(spec.install, "cli-delegate", `${id} keeps ${spec.configFormat} but is configured by file merge`);
    assert.equal(spec.cli?.fallback, "none", `${id} keeps ${spec.configFormat} but falls back to the JSON merger`);
  }
});

// A CLI-configured editor that never passes the key configures a server that
// cannot authenticate, and says nothing about it. The placeholder is what
// carries the environment into the command, so its absence is a silent one.
test("every delegated install has a way to pass the environment", () => {
  for (const id of clientIds()) {
    const spec = clientSpec(id);
    if (spec.install !== "cli-delegate") continue;

    assert.ok(spec.cli?.envArg, `${id} delegates to a CLI with no envArg, so the API key can never reach it`);
    assert.ok(spec.cli?.args.includes("${env}"), `${id} declares an envArg but no \${env} placeholder to splice it into`);
  }
});

// Copilot is the one editor that does not use mcpServers. Getting this wrong
// writes a valid-looking file that Copilot ignores entirely.
test("copilot writes under servers, not mcpServers", () => {
  assert.equal(clientSpec("copilot").topLevelKey, "servers");
  assert.equal(clientSpec("cursor").topLevelKey, "mcpServers");
});
