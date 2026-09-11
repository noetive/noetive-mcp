import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { adapterFor } from "./adapters";
import {
  assertUsableWorkspace,
  clientIds,
  clientSpec,
  configPath,
  defaultScope,
  isInstalled,
  PACKAGE_NAME,
  SERVER_NAME,
} from "./clients";
import { interview } from "./interview";
import { Cancelled, interactive, Prompter, terminalPrompter } from "./prompt";
import { resolveBinary } from "./resolveBinary";
import { API_KEY_ENV, describeKeyHandling, EntryOptions } from "./serverEntry";
import { bundledSkills, installSkills, removeSkills, SkillDocument, skillTarget } from "./skills";

/**
 * packageVersion reads the published version from the package manifest, which
 * sits two levels above the compiled module.
 *
 * It is read on demand rather than at import time so a layout change surfaces
 * on `--version` alone instead of breaking every command.
 */
function packageVersion(): string {
  const manifest = join(__dirname, "..", "..", "package.json");
  return JSON.parse(readFileSync(manifest, "utf8")).version as string;
}

const USAGE = `noetive-mcp: connect your AI editor to Noetive Semantik

Usage:
  npx ${PACKAGE_NAME}                          serve over stdio (what editors run)
  npx ${PACKAGE_NAME} init                     configure the editor found here
  npx ${PACKAGE_NAME} init --client <id>       configure a named editor
  npx ${PACKAGE_NAME} remove --client <id>     remove the ${SERVER_NAME} entry
  npx ${PACKAGE_NAME} list                     show every editor and its status
  npx ${PACKAGE_NAME} doctor                   diagnose an installation

Clients:
${clientIds().map((id) => `  ${id.padEnd(14)}${clientSpec(id).displayName}`).join("\n")}

Run \`init\` in a terminal and it asks for what it needs. Pass the flags below to
answer ahead of time; anything you pass is not asked about again, and with
--yes nothing is asked at all.

Options:
  --client <id>        editor to configure; detected when omitted
  --scope <name>       where to write; defaults per editor (see list)
  --api-key <key>      write the key into the config instead of referencing ${API_KEY_ENV}
  --namespace <name>   namespace for tool calls that do not name one
  --model <name>       embedding model that namespace is provisioned with
  --dimensions <n>     dimensions for that model
  --disable-global-ns  refuse calls that route to the shared "global" namespace
  --allow-global-ns    leave the shared namespace available
  --skills <list>      skills to install: all, none, or a comma-separated list
  --yes                accept the defaults and ask nothing
  --dry-run            print what would change and exit
  --json               machine-readable output
  --version            print the version
`;

interface Options {
  readonly command: string;
  readonly flags: Map<string, string | boolean>;
}

/** run executes a CLI invocation and returns the process exit code. */
export async function run(argv: readonly string[], out: Writer = console.log, err: Writer = console.error): Promise<number> {
  let options: Options;
  try {
    options = parse(argv);
  } catch (e) {
    err((e as Error).message);
    return 2;
  }

  if (options.flags.has("version")) {
    out(packageVersion());
    return 0;
  }

  try {
    switch (options.command) {
      case "init":
      case "add":
        return await install(options, out);
      case "remove":
        return await uninstall(options, out);
      case "list":
        return await list(options, out);
      case "doctor":
        return await doctor(options, out);
      case "help":
        out(USAGE);
        return 0;
      default:
        err(`unknown command ${JSON.stringify(options.command)}\n\n${USAGE}`);
        return 2;
    }
  } catch (e) {
    // Ctrl-C during the interview is an answer, not a fault. Nothing has been
    // written at that point, so there is nothing to explain and nothing to
    // undo; saying "noetive-mcp: cancelled" would read as a failure.
    if (e instanceof Cancelled) {
      err(`Cancelled. Nothing was written.`);
      return 130;
    }
    err(`noetive-mcp: ${(e as Error).message}`);
    return 1;
  }
}

async function install(options: Options, out: Writer): Promise<number> {
  const workspace = process.cwd();
  const asking = shouldAsk(options);

  const clientId = stringFlag(options, "client") ?? (await chooseClient(options, workspace));
  const spec = clientSpec(clientId);
  const scope = stringFlag(options, "scope") ?? defaultScope(spec);

  assertUsableWorkspace(spec, scope, workspace);

  const available = bundledSkills();
  const installable = skillTarget(spec, scope, workspace) ? available : [];

  // The key is embedded only when the user asks for it by name. Reading it out
  // of the ambient environment and writing it to disk would turn an exported
  // shell variable into a file that gets synced, committed or screen-shared.
  const given = {
    ...optional("apiKey", stringFlag(options, "api-key")),
    ...optional("namespace", stringFlag(options, "namespace")),
    ...optional("model", stringFlag(options, "model")),
    ...optional("dimensions", stringFlag(options, "dimensions")),
    ...optional("disableGlobalNamespace", sharedNamespaceFlag(options)),
    ...optional("skills", chosenSkills(options, installable)),
    expandsVariables: spec.expandsVariables,
    offeredSkills: installable.map((skill) => ({ value: skill.name, label: skill.name, hint: summarize(skill) })),
  };

  // Everything unanswered defaults to unset, which is what every release before
  // this one wrote. An install nobody could be asked about must not acquire
  // settings nobody chose.
  const answers = asking
    ? await interview(prompterFor(options), given)
    : { ...given, skills: given.skills ?? [] };

  const entryOptions: EntryOptions = {
    ...optional("apiKey", answers.apiKey),
    targeting: {
      ...optional("namespace", answers.namespace),
      ...optional("model", answers.model),
      ...optional("dimensions", answers.dimensions),
    },
    ...optional("disableGlobalNamespace", answers.disableGlobalNamespace),
  };

  const dryRun = options.flags.has("dry-run");
  const outcome = await adapterFor(spec).install({ spec, clientId, scope, workspace, entryOptions, dryRun });

  const chosen = available.filter((skill) => answers.skills.includes(skill.name));
  const skills = installSkills(spec, scope, workspace, chosen, { dryRun });

  if (options.flags.has("json")) {
    out(JSON.stringify({ client: clientId, scope, ...outcome, skills }, null, 2));
    return 0;
  }

  if (outcome.diff) {
    out(outcome.diff);
    for (const path of skills.written) out(`  would write ${path}`);
    out(`\nDry run: nothing was written.`);
    return 0;
  }

  if (!outcome.changed) {
    out(`${spec.displayName} already has ${SERVER_NAME} configured at ${outcome.target}.`);
  } else if (outcome.unverified) {
    // Deliberately not "Configured". The editor's own CLI ran and the user
    // answered it; whether it saved anything is on their screen, not in
    // anything this command can read without tying itself to that CLI's layout.
    out(`Handed ${SERVER_NAME} to ${outcome.target}. ${outcome.unverified}`);
  } else {
    out(`Configured ${spec.displayName} at ${outcome.target}.`);
  }

  reportSkills(skills, out);

  if (outcome.changed) {
    out(describeKeyHandling(spec, clientId, entryOptions));
    out(spec.restartHint);
  }
  out(`Check it worked: npx ${PACKAGE_NAME} doctor`);
  return 0;
}

/**
 * shouldAsk decides whether there is anyone to ask.
 *
 * `--json` and `--dry-run` are excluded alongside the terminal check because
 * both are how a script drives this. A prompt in front of either is not a
 * question, it is a process that never exits, and the caller is a CI job or an
 * editor with no way to notice.
 */
function shouldAsk(options: Options): boolean {
  if (options.flags.has("yes") || options.flags.has("json") || options.flags.has("dry-run")) return false;
  return interactive();
}

/** prompterFor exists so tests can drive the interview without a terminal. */
let prompterFor: (options: Options) => Prompter = () => terminalPrompter();

/** usePrompter replaces the prompter, for tests. */
export function usePrompter(build: (options: Options) => Prompter): void {
  prompterFor = build;
}

/**
 * sharedNamespaceFlag reads the pair of flags that answer the same question.
 *
 * Two flags rather than one that takes a value, because `--disable-global-ns`
 * reads as an instruction and `--disable-global-ns=false` reads as a puzzle.
 * Passing both is refused rather than resolved: whichever one it picked would
 * be the opposite of what half the readers of that command line expect.
 */
function sharedNamespaceFlag(options: Options): boolean | undefined {
  const close = options.flags.has("disable-global-ns");
  const open = options.flags.has("allow-global-ns");

  if (close && open) {
    throw new Error("--disable-global-ns and --allow-global-ns say opposite things; pass one");
  }
  if (close) return true;
  if (open) return false;
  return undefined;
}

/**
 * chosenSkills reads --skills, which accepts `all`, `none` or a list of names.
 *
 * A name that matches nothing is an error rather than a silent omission: a
 * typo would otherwise report a successful install of a skill that was never
 * written.
 */
function chosenSkills(options: Options, available: readonly SkillDocument[]): string[] | undefined {
  const raw = stringFlag(options, "skills");
  if (raw === undefined) return undefined;

  const value = raw.trim().toLowerCase();
  if (value === "all") return available.map((skill) => skill.name);
  if (value === "none" || value === "") return [];

  const names = raw.split(",").map((name) => name.trim()).filter(Boolean);
  const unknown = names.filter((name) => !available.some((skill) => skill.name === name));
  if (unknown.length > 0) {
    const offered = available.map((skill) => skill.name).join(", ") || "none for this editor";
    throw new Error(`unknown skill${unknown.length > 1 ? "s" : ""} ${unknown.join(", ")}; available: ${offered}`);
  }
  return names;
}

/** summarize shortens a skill description to something a list can carry. */
function summarize(skill: SkillDocument): string {
  const sentence = skill.description.split(". ")[0] ?? "";
  return sentence.length > 72 ? `${sentence.slice(0, 69)}...` : sentence;
}

function reportSkills(outcome: ReturnType<typeof installSkills>, out: Writer): void {
  if (outcome.unsupported) {
    out(outcome.unsupported);
    return;
  }
  if (outcome.written.length > 0) {
    out(`Installed ${outcome.written.length} skill file${outcome.written.length > 1 ? "s" : ""} into ${outcome.target}.`);
  } else if (outcome.unchanged.length > 0) {
    out(`Skills at ${outcome.target} are already up to date.`);
  }
}

/** optional builds a single-key object, or nothing when the value is unset. */
function optional<K extends string, V>(key: K, value: V | undefined): { [P in K]?: V } {
  return (value === undefined ? {} : { [key]: value }) as { [P in K]?: V };
}

/**
 * chooseClient picks the editor to configure when --client was omitted.
 *
 * Several detected editors is genuinely ambiguous. With a terminal that is a
 * question worth asking; without one it stays what it was, a refusal listing
 * the candidates, because picking for someone writes into an editor they did
 * not mean to configure.
 */
async function chooseClient(options: Options, workspace: string): Promise<string> {
  const found = clientIds().filter((id) => isInstalled(clientSpec(id), workspace, existsSync));

  if (found.length === 1) return found[0]!;
  if (found.length === 0) {
    throw new Error(`no supported editor was detected here; name one with --client: ${clientIds().join(", ")}`);
  }
  if (!shouldAsk(options)) {
    throw new Error(`several editors are installed; name one with --client: ${found.join(", ")}`);
  }

  return await prompterFor(options).select(
    "Which editor should this configure?",
    found.map((id) => ({ value: id, label: clientSpec(id).displayName, hint: id })),
  );
}

async function uninstall(options: Options, out: Writer): Promise<number> {
  const clientId = requireString(options, "client");
  const spec = clientSpec(clientId);
  const scope = stringFlag(options, "scope") ?? defaultScope(spec);
  const workspace = process.cwd();
  const dryRun = options.flags.has("dry-run");

  const outcome = await adapterFor(spec).remove({ spec, clientId, scope, workspace, dryRun });

  // Skills came in with the entry, so they go out with it. Leaving them behind
  // means an editor that still loads instructions for tools it no longer has.
  const skills = removeSkills(spec, scope, workspace, bundledSkills(), { dryRun });

  if (options.flags.has("json")) {
    out(JSON.stringify({ client: clientId, scope, ...outcome, skills }, null, 2));
    return 0;
  }

  out(outcome.changed ? `Removed ${SERVER_NAME} from ${outcome.target}.` : `${SERVER_NAME} was not configured at ${outcome.target}.`);
  for (const dir of skills.written) {
    out(dryRun ? `  would remove ${dir}` : `  removed ${dir}`);
  }
  return 0;
}

async function list(options: Options, out: Writer): Promise<number> {
  const workspace = process.cwd();
  const rows = [];

  for (const id of clientIds()) {
    const spec = clientSpec(id);
    const scope = defaultScope(spec);
    const installed = isInstalled(spec, workspace, existsSync);
    const { found, undetermined } = await configuredScopes(id, workspace);

    rows.push({
      client: id,
      displayName: spec.displayName,
      installed,
      configured: found.length > 0,
      undetermined: found.length === 0 && undetermined.length > 0,
      scopes: found,
      defaultTarget: configPath(spec, scope, workspace),
    });
  }

  if (options.flags.has("json")) {
    out(JSON.stringify(rows, null, 2));
    return 0;
  }

  for (const row of rows) {
    const state = row.configured
      ? "configured"
      : row.undetermined
        ? "cannot tell"
        : row.installed
          ? "not configured"
          : "not detected";
    const where = row.configured ? row.scopes.map((s) => `${s.scope}: ${s.target}`).join(", ") : row.defaultTarget;
    out(`${row.displayName.padEnd(26)} ${state.padEnd(15)} ${where}`);
  }
  return 0;
}

/**
 * doctor reports what an installation actually looks like, so a user whose
 * editor shows no Noetive tools can tell which independent thing is wrong: the
 * binary, the key, the editor config, or the broker.
 *
 * Only genuine faults fail. An editor the user has installed but deliberately
 * did not configure is reported, not failed — a doctor that is permanently red
 * for a Cursor user who does not use Copilot teaches people to ignore it, which
 * costs more than the check is worth.
 */
async function doctor(options: Options, out: Writer): Promise<number> {
  const checks: Check[] = [];

  try {
    checks.push({ name: "binary", status: "pass", detail: resolveBinary() });
  } catch (e) {
    checks.push({ name: "binary", status: "fail", detail: firstLine(e as Error) });
  }

  const workspace = process.cwd();
  const editors: Check[] = [];
  for (const id of clientIds()) {
    const spec = clientSpec(id);
    if (!isInstalled(spec, workspace, existsSync)) continue;

    // Every scope, not just the default. A user who installed with
    // --scope project is configured, and a report that only looks at the
    // global file tells them they are not — sending them to fix something
    // that already works.
    const { found, undetermined } = await configuredScopes(id, workspace);
    if (found.length > 0) {
      editors.push({
        name: spec.displayName,
        status: "pass",
        detail: found.map((f) => `${f.scope}: ${f.target}`).join(", "),
      });
      continue;
    }

    // Never "not configured" for an editor nothing can answer for. Prescribing
    // init there tells a user whose install already works to run it again, and
    // keeps telling them, which is how a report teaches people to ignore it.
    editors.push({
      name: spec.displayName,
      status: "info",
      detail:
        undetermined.length > 0
          ? `cannot be checked from here; look for ${SERVER_NAME} in ${undetermined.map((u) => u.target).join(", ")}`
          : `not configured; run: npx ${PACKAGE_NAME} init --client ${id}`,
      undetermined: undetermined.length > 0,
    });
  }

  // No configured editor at all is a real fault: nothing can reach Noetive.
  // One configured editor and three ignored ones is a working setup. An editor
  // that cannot be checked is not evidence of a fault either way, so it holds
  // the failure back rather than triggering it.
  const configured = editors.filter((e) => e.status === "pass");
  const unanswerable = editors.filter((e) => e.undetermined);
  if (editors.length > 0 && configured.length === 0 && unanswerable.length === 0) {
    checks.push({
      name: "editors",
      status: "fail",
      detail: `no editor is configured; run: npx ${PACKAGE_NAME} init --client ${clientIds()[0]}`,
    });
  }

  // The key is only a fault where it is actually needed. An editor configured
  // with --api-key carries its own, and does not depend on this shell.
  const shellKey = (process.env[API_KEY_ENV] ?? "").trim();
  // An editor that might be configured needs the key just as much as one that
  // demonstrably is, so it counts here too: calling the key a fault on a
  // working install is the same mistake in the other direction.
  checks.push(keyCheck(shellKey, configured.length > 0 || unanswerable.length > 0));

  checks.push(...editors);

  if (options.flags.has("json")) {
    out(JSON.stringify(checks, null, 2));
    return exitCode(checks);
  }

  for (const check of checks) {
    out(`${label(check.status)}  ${check.name.padEnd(26)} ${check.detail}`);
  }
  out(``);
  out(`To check the broker itself, ask your agent to call the noetive_health tool.`);
  return exitCode(checks);
}

interface Check {
  readonly name: string;
  readonly status: "pass" | "fail" | "info";
  readonly detail: string;
  /**
   * Set on an editor check that could not be answered either way, which is
   * neither a pass nor a fault and must not be counted as one.
   */
  readonly undetermined?: boolean;
}

/**
 * configuredScopes finds every scope where an editor already has a noetive
 * entry.
 *
 * An editor can legitimately be configured in more than one place — a global
 * entry and a per-project one — and which of them applies depends on where the
 * editor was opened, not on which this command considers the default.
 */
export async function configuredScopes(clientId: string, workspace: string): Promise<EditorStatus> {
  const spec = clientSpec(clientId);
  const adapter = adapterFor(spec);
  const found: ScopeTarget[] = [];
  const undetermined: ScopeTarget[] = [];

  for (const scope of Object.keys(spec.scopes)) {
    const report = await adapter.status({ spec, clientId, scope, workspace });
    if (report.configured === "yes") found.push({ scope, target: report.target });
    else if (report.configured === "unknown") undetermined.push({ scope, target: report.target });
  }
  return { found, undetermined };
}

export interface ScopeTarget {
  readonly scope: string;
  readonly target: string;
}

/**
 * EditorStatus separates the scopes an editor is configured in from the ones
 * nothing can answer for.
 *
 * Folding the second into "not configured" is what makes a report actively
 * misleading rather than merely incomplete: it sends a user to configure
 * something that may already be right, and does it every single run.
 */
export interface EditorStatus {
  readonly found: readonly ScopeTarget[];
  readonly undetermined: readonly ScopeTarget[];
}

function keyCheck(shellKey: string, anyEditorConfigured: boolean): Check {
  if (!shellKey) {
    return {
      name: "api key",
      status: anyEditorConfigured ? "info" : "fail",
      detail: `${API_KEY_ENV} is not set in this shell. Editors launched from here will not authenticate; editors configured with --api-key carry their own.`,
    };
  }
  // A shell that exports the placeholder rather than a key is a real fault, and
  // one that otherwise only shows up as an "unauthorized" from the server.
  if (shellKey.startsWith("$") || shellKey.startsWith("%")) {
    return { name: "api key", status: "fail", detail: `${API_KEY_ENV} contains the literal text ${shellKey}, not a key.` };
  }
  return { name: "api key", status: "pass", detail: `${API_KEY_ENV} is set in this shell` };
}

function label(status: Check["status"]): string {
  return status === "pass" ? "PASS" : status === "fail" ? "FAIL" : "  · ";
}

function exitCode(checks: readonly Check[]): number {
  return checks.some((c) => c.status === "fail") ? 1 : 0;
}

/** firstLine keeps a multi-line error from breaking the aligned report. */
function firstLine(err: Error): string {
  return err.message.split("\n")[0] ?? err.message;
}

export type Writer = (line: string) => void;

/**
 * parse splits argv into a command and flags, accepting both `--flag value`
 * and `--flag=value`. An unknown flag is an error rather than being ignored,
 * because a silently-dropped --scope writes to the wrong file.
 */
export function parse(argv: readonly string[]): Options {
  const known = new Set([
    "client", "scope", "api-key", "namespace", "model", "dimensions", "skills",
    "disable-global-ns", "allow-global-ns", "yes",
    "dry-run", "json", "version", "help",
  ]);
  const boolean = new Set([
    "disable-global-ns", "allow-global-ns", "yes",
    "dry-run", "json", "version", "help",
  ]);

  const flags = new Map<string, string | boolean>();
  let command = "help";
  let seenCommand = false;

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i]!;

    if (!arg.startsWith("-")) {
      if (!seenCommand) {
        command = arg;
        seenCommand = true;
      }
      continue;
    }

    const body = arg.replace(/^--?/, "");
    const [name, inline] = body.includes("=") ? [body.slice(0, body.indexOf("=")), body.slice(body.indexOf("=") + 1)] : [body, undefined];

    if (!known.has(name)) {
      throw new Error(`unknown option --${name}\n\n${USAGE}`);
    }
    if (boolean.has(name)) {
      flags.set(name, true);
      continue;
    }

    const value = inline ?? argv[++i];
    if (value === undefined) throw new Error(`--${name} needs a value`);
    flags.set(name, value);
  }

  if (flags.has("help")) command = "help";
  return { command, flags };
}

function stringFlag(options: Options, name: string): string | undefined {
  const value = options.flags.get(name);
  return typeof value === "string" ? value : undefined;
}

function requireString(options: Options, name: string): string {
  const value = stringFlag(options, name);
  if (!value) throw new Error(`--${name} is required; one of: ${clientIds().join(", ")}`);
  return value;
}
