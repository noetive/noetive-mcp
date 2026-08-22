import { existsSync, readdirSync, readFileSync, rmSync, statSync } from "node:fs";
import { join, relative, sep } from "node:path";

import { ClientSpec, skillsPath } from "./clients";
import { write } from "./configFile";

/**
 * SkillDocument is one skill as it ships: a SKILL.md and whatever reference
 * files it loads on demand.
 */
export interface SkillDocument {
  readonly name: string;
  readonly description: string;
  /** Paths relative to the skill's own directory, including SKILL.md. */
  readonly files: readonly SkillFile[];
}

export interface SkillFile {
  readonly relative: string;
  readonly contents: string;
}

/** SkillOutcome reports what an install or removal did. */
export interface SkillOutcome {
  /** The directory skills were written to, or undefined when there is none. */
  readonly target?: string;
  readonly written: readonly string[];
  readonly unchanged: readonly string[];
  /** Set when this editor has no skills directory, saying so in words. */
  readonly unsupported?: string;
}

/**
 * bundledSkills reads the skills packed into this npm package.
 *
 * Read from disk rather than compiled in, because the same files are generated
 * into the plugin payloads by `go run ./packaging/emit` and a second copy
 * embedded in TypeScript would be a second thing to keep in step. Resolved the
 * same way the package manifest is: two levels above the compiled module.
 */
export function bundledSkills(root: string = join(__dirname, "..", "..", "skills")): SkillDocument[] {
  if (!existsSync(root)) return [];

  return readdirSync(root, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => read(join(root, entry.name), entry.name))
    .filter((skill): skill is SkillDocument => skill !== undefined);
}

/**
 * read loads one skill directory.
 *
 * A directory with no SKILL.md is skipped rather than reported. It is not a
 * skill, and the only way one appears here is a stale directory left by an
 * older layout, which is not the user's problem to hear about mid-install.
 */
function read(dir: string, name: string): SkillDocument | undefined {
  const files: SkillFile[] = [];

  for (const path of walk(dir)) {
    files.push({ relative: relative(dir, path).split(sep).join("/"), contents: readFileSync(path, "utf8") });
  }

  const skill = files.find((f) => f.relative === "SKILL.md");
  if (!skill) return undefined;

  return { name, description: describe(skill.contents), files };
}

function walk(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    return entry.isDirectory() ? walk(path) : [path];
  });
}

/**
 * describe pulls the description out of the frontmatter, which is what the
 * checkbox list shows beside each name.
 *
 * A deliberately shallow read rather than a YAML parse: this needs one scalar
 * from a document the emitter wrote, and a parser dependency to get it would
 * cost more than a missing hint in a prompt.
 */
function describe(skill: string): string {
  const match = /^---\r?\n([\s\S]*?)\r?\n---/.exec(skill);
  if (!match) return "";

  const line = /^description:\s*(.*)$/m.exec(match[1]!);
  if (!line) return "";

  return line[1]!.trim().replace(/^['"]|['"]$/g, "");
}

/**
 * skillTarget is where this editor reads skills from, or undefined when it has
 * nowhere to read them from.
 */
export function skillTarget(spec: ClientSpec, scope: string, workspace: string): string | undefined {
  return skillsPath(spec, scope, workspace);
}

/**
 * installSkills writes the chosen skills into the editor's skills directory.
 *
 * Unchanged files are left alone rather than rewritten. Re-running `init` is
 * routine, and rewriting an identical file would replace the user's backup with
 * a copy of the same content, quietly discarding whatever they had edited two
 * runs ago.
 */
export function installSkills(
  spec: ClientSpec,
  scope: string,
  workspace: string,
  skills: readonly SkillDocument[],
  options: { readonly dryRun?: boolean } = {},
): SkillOutcome {
  const target = skillTarget(spec, scope, workspace);
  if (!target) {
    return {
      written: [],
      unchanged: [],
      unsupported: `${spec.displayName} has no skills directory, so none were installed. The tools carry their own instructions.`,
    };
  }

  const written: string[] = [];
  const unchanged: string[] = [];

  for (const skill of skills) {
    for (const file of skill.files) {
      const path = join(target, skill.name, ...file.relative.split("/"));

      if (existsSync(path) && readFileSync(path, "utf8") === file.contents) {
        unchanged.push(path);
        continue;
      }

      written.push(path);
      if (!options.dryRun) write(path, file.contents);
    }
  }

  return { target, written, unchanged };
}

/**
 * removeSkills deletes the skill directories this installer wrote.
 *
 * Only directories whose names it recognises, and only when a SKILL.md is
 * present. `remove` is documented as touching the noetive entry and nothing
 * else, and a user's own skill that happens to sit in the same directory is not
 * ours to delete.
 */
export function removeSkills(
  spec: ClientSpec,
  scope: string,
  workspace: string,
  skills: readonly SkillDocument[],
  options: { readonly dryRun?: boolean } = {},
): SkillOutcome {
  const target = skillTarget(spec, scope, workspace);
  if (!target) return { written: [], unchanged: [] };

  const removed: string[] = [];

  for (const skill of skills) {
    const dir = join(target, skill.name);
    if (!existsSync(join(dir, "SKILL.md"))) continue;
    if (!statSync(dir).isDirectory()) continue;

    removed.push(dir);
    if (!options.dryRun) rmSync(dir, { recursive: true, force: true });
  }

  return { target, written: removed, unchanged: [] };
}
