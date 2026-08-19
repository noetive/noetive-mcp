import assert from "node:assert/strict";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { diff, parseObject, read, removeEntry, restore, setEntry, write } from "../src/configFile";

function scratch(): string {
  return mkdtempSync(join(tmpdir(), "noetive-configfile-"));
}

// A diff that reports no change when there is one makes --dry-run a lie, and
// the whole point of the flag is that a user can trust what it shows.
test("a diff locates the changed region and elides the rest", () => {
  const before = "line 1\nline 2\nline 3\n";
  const after = "line 1\nCHANGED\nline 3\n";

  const rendered = diff(before, after, "config.json");

  assert.match(rendered, /-line 2/);
  assert.match(rendered, /\+CHANGED/);
  assert.doesNotMatch(rendered, /[-+]line 1/, "an unchanged leading line was reported as changed");
  assert.doesNotMatch(rendered, /[-+]line 3/, "an unchanged trailing line was reported as changed");
});

// Identical input must say so rather than rendering an empty hunk that reads
// like a change nobody can see.
test("identical text reports no change", () => {
  assert.match(diff("same\n", "same\n", "config.json"), /no change/);
});

test("a pure addition reports only added lines", () => {
  const rendered = diff("line 1\n", "line 1\nline 2\n", "config.json");

  assert.match(rendered, /\+line 2/);
  // Skipping the ---/+++ header, which is not part of the change itself.
  assert.deepEqual(removals(rendered), [], "an addition reported a removal");
});

test("a pure removal reports only removed lines", () => {
  const rendered = diff("line 1\nline 2\n", "line 1\n", "config.json");

  assert.match(rendered, /-line 2/);
  assert.deepEqual(additions(rendered), [], "a removal reported an addition");
});

/** body lines only: the ---/+++ header is framing, not part of the change. */
function body(rendered: string): string[] {
  return rendered.split("\n").filter((l) => !l.startsWith("---") && !l.startsWith("+++") && !l.startsWith("@@"));
}

function removals(rendered: string): string[] {
  return body(rendered).filter((l) => l.startsWith("-"));
}

function additions(rendered: string): string[] {
  return body(rendered).filter((l) => l.startsWith("+"));
}

// The line number is how a user finds the change in their own file. A wrong one
// sends them to the wrong place in a config they care about.
test("the diff reports the line the change starts on", () => {
  const rendered = diff("a\nb\nc\nd\n", "a\nb\nCHANGED\nd\n", "config.json");

  assert.match(rendered, /@@ line 3 @@/);
});

// A missing file and an empty file are the same situation: there is nothing to
// preserve, and both must yield a document the caller can write into.
test("a missing or empty file reads as an empty object", () => {
  const dir = scratch();
  assert.equal(read(join(dir, "absent.json")), "{}");

  const empty = join(dir, "empty.json");
  writeFileSync(empty, "   \n  ");
  assert.equal(read(empty), "{}");
});

// Editors write comments and trailing commas into their own config. Refusing
// those would refuse the file the editor itself produced.
test("comments and trailing commas parse", () => {
  const parsed = parseObject('{\n  // a note\n  "mcpServers": { "a": {} },\n}', "config.json");

  assert.deepEqual(parsed.mcpServers, { a: {} });
});

// Treating a corrupt config as empty would let the next write discard every
// server the user had, which is the worst thing this code can do.
test("a corrupt file is refused and named", () => {
  assert.throws(() => parseObject('{ "mcpServers": ', "config.json"), /config.json is not valid JSON/);
});

// A top-level array is valid JSON but not a usable config. Accepting it would
// write server entries onto a value the editor cannot read.
test("a top-level array is refused", () => {
  assert.throws(() => parseObject("[1, 2, 3]", "config.json"), /must contain a JSON object/);
});

// An entry has to be readable back as what was written, or the verification
// step after a write is meaningless.
test("an entry is written where it can be read back", () => {
  const text = setEntry("{}", ["mcpServers", "noetive"], { command: "npx" });

  assert.deepEqual(parseObject(text, "config.json"), { mcpServers: { noetive: { command: "npx" } } });
});

test("removing an entry leaves its siblings", () => {
  const before = setEntry(setEntry("{}", ["mcpServers", "noetive"], { command: "npx" }), ["mcpServers", "other"], {
    command: "other",
  });

  const after = parseObject(removeEntry(before, ["mcpServers", "noetive"]), "config.json");

  assert.deepEqual(after.mcpServers, { other: { command: "other" } });
});

// The write is atomic so an interrupted run cannot leave an editor with a
// truncated config and no MCP servers at all.
test("a written file ends up complete and owner-readable", () => {
  const target = join(scratch(), "nested", "config.json");

  write(target, '{"a":1}\n');

  assert.equal(readFileSync(target, "utf8"), '{"a":1}\n');
});

// Restoring is what makes a failed verification recoverable. If it does not put
// the original back exactly, the recovery is worse than the failure.
test("restoring puts back the original bytes", () => {
  const dir = scratch();
  const target = join(dir, "config.json");
  const original = '{\n  // keep me\n  "mcpServers": {}\n}\n';
  writeFileSync(target, original);

  const backup = write(target, "{}\n");
  assert.ok(backup, "no backup was taken");
  restore(target, backup!);

  assert.equal(readFileSync(target, "utf8"), original);
});

// A first write has nothing to back up, and reporting a backup that does not
// exist would make the restore path fail on the one run it matters.
test("a first write reports no backup", () => {
  assert.equal(write(join(scratch(), "config.json"), "{}\n"), undefined);
});

// A file the process cannot read must fail loudly rather than being treated as
// absent — that would replace a config the user still has.
test("an unreadable file is not mistaken for a missing one", { skip: process.platform === "win32" ? "POSIX mode bits" : false }, () => {
  const target = join(scratch(), "config.json");
  writeFileSync(target, '{"mcpServers":{}}');
  chmodSync(target, 0o000);

  try {
    assert.throws(() => read(target));
  } finally {
    chmodSync(target, 0o600);
  }
});
