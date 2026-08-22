import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { clientSpec } from "../src/clients";
import { backupPath } from "../src/configFile";
import { bundledSkills, installSkills, removeSkills, skillTarget } from "../src/skills";

/** library writes a skill tree of the shape the emitter produces. */
function library(): string {
  const root = mkdtempSync(join(tmpdir(), "noetive-skills-"));

  mkdirSync(join(root, "semql", "references"), { recursive: true });
  writeFileSync(join(root, "semql", "SKILL.md"), "---\nname: semql\ndescription: Write SemQL. And more.\n---\n\nbody\n");
  writeFileSync(join(root, "semql", "references", "grammar.md"), "the grammar\n");

  mkdirSync(join(root, "doctor"), { recursive: true });
  writeFileSync(join(root, "doctor", "SKILL.md"), "---\nname: doctor\ndescription: Diagnose\n---\n\nbody\n");

  return root;
}

/** workspace is a scratch project directory to install into. */
function workspace(): string {
  return mkdtempSync(join(tmpdir(), "noetive-ws-"));
}

const claudeCode = clientSpec("claude-code");

// The skill body tells its reader to open references/<file>. Shipping the body
// without the references produces a skill that instructs its reader to read
// something that is not there.
test("a skill and its references are read together", () => {
  const skills = bundledSkills(library());

  const semql = skills.find((s) => s.name === "semql");
  assert.ok(semql, "expected the semql skill");
  assert.deepEqual(
    semql.files.map((f) => f.relative).sort(),
    ["SKILL.md", "references/grammar.md"],
  );
});

// The description is what the checkbox list shows beside each name, so a skill
// with no readable description is one the user picks blind.
test("the description is read out of the frontmatter", () => {
  const skills = bundledSkills(library());

  assert.equal(skills.find((s) => s.name === "semql")?.description, "Write SemQL. And more.");
});

// A directory with no SKILL.md is not a skill. Reporting it would turn a stale
// directory from an older layout into an error the user cannot act on.
test("a directory that is not a skill is skipped", () => {
  const root = library();
  mkdirSync(join(root, "leftovers"), { recursive: true });
  writeFileSync(join(root, "leftovers", "notes.md"), "not a skill");

  assert.deepEqual(
    bundledSkills(root).map((s) => s.name).sort(),
    ["doctor", "semql"],
  );
});

// A package with no skills packed into it must not fail the install. The
// wrapper is published from a build, and a build that skipped the emit step
// should degrade to configuring the server rather than refusing to run.
test("a missing skills directory reads as no skills", () => {
  assert.deepEqual(bundledSkills(join(tmpdir(), "noetive-does-not-exist")), []);
});

test("skills are written under the editor's skills directory", () => {
  const ws = workspace();
  const skills = bundledSkills(library());

  const outcome = installSkills(claudeCode, "project", ws, skills);

  const target = skillTarget(claudeCode, "project", ws)!;
  assert.equal(outcome.target, target);
  assert.equal(readFileSync(join(target, "semql", "references", "grammar.md"), "utf8"), "the grammar\n");
  assert.ok(existsSync(join(target, "doctor", "SKILL.md")));
});

// Re-running init is routine. Rewriting an identical file would replace the
// user's backup with a copy of the same content, quietly discarding whatever
// they had edited two runs ago.
test("an unchanged skill is left alone rather than rewritten", () => {
  const ws = workspace();
  const skills = bundledSkills(library());

  installSkills(claudeCode, "project", ws, skills);
  const second = installSkills(claudeCode, "project", ws, skills);

  assert.deepEqual(second.written, [], "nothing should have been rewritten");
  assert.ok(second.unchanged.length > 0);
  assert.equal(
    existsSync(backupPath(join(second.target!, "semql", "SKILL.md"))),
    false,
    "an unchanged file should not have produced a backup",
  );
});

// An edited skill is backed up before being replaced, on the same terms as a
// config file: the useful operation is undoing the last change.
test("replacing an edited skill leaves a backup", () => {
  const ws = workspace();
  const skills = bundledSkills(library());
  const target = skillTarget(claudeCode, "project", ws)!;

  installSkills(claudeCode, "project", ws, skills);
  writeFileSync(join(target, "semql", "SKILL.md"), "edited by hand");
  installSkills(claudeCode, "project", ws, skills);

  assert.equal(readFileSync(backupPath(join(target, "semql", "SKILL.md")), "utf8"), "edited by hand");
});

// --dry-run has to mean nothing was written here too, or the flag people use to
// see what would happen is the one that makes it happen.
test("a dry run reports the files it would write and writes none", () => {
  const ws = workspace();
  const skills = bundledSkills(library());

  const outcome = installSkills(claudeCode, "project", ws, skills, { dryRun: true });

  assert.ok(outcome.written.length > 0, "expected the files to be reported");
  assert.equal(existsSync(join(outcome.target!, "semql", "SKILL.md")), false, "nothing should have been written");
});

// The instruction formats in circulation are not interchangeable. An editor
// with no skills directory is told so, rather than given files in a shape it
// silently ignores.
test("an editor with no skills directory is told so rather than written to", () => {
  const outcome = installSkills(clientSpec("codex"), "user", workspace(), bundledSkills(library()));

  assert.deepEqual(outcome.written, []);
  assert.equal(outcome.target, undefined);
  assert.match(outcome.unsupported ?? "", /no skills directory/);
});

// Skills came in with the entry, so they go out with it. Leaving them behind
// means an editor still loading instructions for tools it no longer has.
test("remove takes the skills out with the entry", () => {
  const ws = workspace();
  const skills = bundledSkills(library());
  const target = skillTarget(claudeCode, "project", ws)!;

  installSkills(claudeCode, "project", ws, skills);
  removeSkills(claudeCode, "project", ws, skills);

  assert.equal(existsSync(join(target, "semql")), false);
  assert.equal(existsSync(join(target, "doctor")), false);
});

// `remove` is documented as touching the noetive entry and nothing else. A
// user's own skill that happens to share the directory is not ours to delete.
test("remove leaves skills this installer did not write", () => {
  const ws = workspace();
  const skills = bundledSkills(library());
  const target = skillTarget(claudeCode, "project", ws)!;

  installSkills(claudeCode, "project", ws, skills);
  mkdirSync(join(target, "their-own-skill"), { recursive: true });
  writeFileSync(join(target, "their-own-skill", "SKILL.md"), "theirs");

  removeSkills(claudeCode, "project", ws, skills);

  assert.equal(readFileSync(join(target, "their-own-skill", "SKILL.md"), "utf8"), "theirs");
});

test("a dry-run removal deletes nothing", () => {
  const ws = workspace();
  const skills = bundledSkills(library());
  const target = skillTarget(claudeCode, "project", ws)!;

  installSkills(claudeCode, "project", ws, skills);
  const outcome = removeSkills(claudeCode, "project", ws, skills, { dryRun: true });

  assert.ok(outcome.written.length > 0);
  assert.ok(existsSync(join(target, "semql", "SKILL.md")));
});

// Scopes say which config file to edit. An editor that reads skills from one
// place regardless is not served by refusing to write them because the user
// picked a different config scope.
test("a scope with no skills entry falls back to one the editor does declare", () => {
  const ws = workspace();

  assert.ok(skillTarget(claudeCode, "local", ws));
  assert.ok(skillTarget(claudeCode, "user", ws));
});

// The skills the wrapper ships are generated by the emitter into a directory
// npm can actually pack. A build that resolves nothing here publishes a
// wrapper that silently installs no skills at all.
test("the packed skills directory carries the emitted skills", () => {
  const packed = bundledSkills();

  assert.ok(packed.length > 0, "expected skills packed beside the compiled module");
  for (const name of ["doctor", "semql", "semantik"]) {
    assert.ok(packed.some((s) => s.name === name), `expected the ${name} skill to be packed`);
  }
  const semql = packed.find((s) => s.name === "semql")!;
  assert.ok(
    semql.files.some((f) => f.relative.startsWith("references/")),
    "expected semql to carry its references",
  );
});
