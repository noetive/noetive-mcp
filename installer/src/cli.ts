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
import { resolveBinary } from "./resolveBinary";
import { API_KEY_ENV, describeKeyHandling, EntryOptions } from "./serverEntry";

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

const USAGE = `noetive-mcp — connect your AI editor to Noetive Semantik

Usage:
  npx ${PACKAGE_NAME}                          serve over stdio (what editors run)
  npx ${PACKAGE_NAME} init                     configure the editor found here
  npx ${PACKAGE_NAME} init --client <id>       configure a named editor
  npx ${PACKAGE_NAME} remove --client <id>     remove the ${SERVER_NAME} entry
  npx ${PACKAGE_NAME} list                     show every editor and its status
  npx ${PACKAGE_NAME} doctor                   diagnose an installation

Clients:
${clientIds().map((id) => `  ${id.padEnd(14)}${clientSpec(id).displayName}`).join("\n")}

Options:
  --client <id>        editor to configure; detected when omitted
  --scope <name>       where to write; defaults per editor (see list)
  --api-key <key>      write the key into the config instead of referencing ${API_KEY_ENV}
  --namespace <name>   default namespace for tool calls that do not name one
  --model <name>       default embedding model
  --dimensions <n>     default embedding dimensionality
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
    err(`noetive-mcp: ${(e as Error).message}`);
    return 1;
  }
}

async function install(options: Options, out: Writer): Promise<number> {
  const clientId = stringFlag(options, "client") ?? detectClient(process.cwd());
  const spec = clientSpec(clientId);
  const scope = stringFlag(options, "scope") ?? defaultScope(spec);
  const workspace = process.cwd();

  assertUsableWorkspace(spec, scope, workspace);

  // The key is embedded only when the user asks for it by name. Reading it out
  // of the ambient environment and writing it to disk would turn an exported
  // shell variable into a file that gets synced, committed or screen-shared.
  const apiKey = stringFlag(options, "api-key");
  const namespace = stringFlag(options, "namespace");
  const model = stringFlag(options, "model");
  const dimensions = stringFlag(options, "dimensions");

  const entryOptions: EntryOptions = {
    ...(apiKey ? { apiKey } : {}),
    targeting: {
      ...(namespace ? { namespace } : {}),
      ...(model ? { model } : {}),
      ...(dimensions ? { dimensions } : {}),
    },
  };

  const outcome = await adapterFor(spec).install({
    spec,
    clientId,
    scope,
    workspace,
    entryOptions,
    dryRun: options.flags.has("dry-run"),
  });

  if (options.flags.has("json")) {
    out(JSON.stringify({ client: clientId, scope, ...outcome }, null, 2));
    return 0;
  }

  if (outcome.diff) {
    out(outcome.diff);
    out(`\nDry run: nothing was written.`);
    return 0;
  }
  if (!outcome.changed) {
    out(`${spec.displayName} already has ${SERVER_NAME} configured at ${outcome.target}. Nothing to do.`);
    return 0;
  }

  out(`Configured ${spec.displayName} at ${outcome.target}.`);
  out(describeKeyHandling(spec, clientId, entryOptions));
  out(spec.restartHint);
  out(`Check it worked: npx ${PACKAGE_NAME} doctor`);
  return 0;
}

/**
 * detectClient picks the editor to configure when --client was omitted.
 *
 * Requiring the flag means a first-time user has to learn our client ids before
 * they can install anything, and the ids are ours, not theirs. One detected
 * editor is an unambiguous answer. Several is not, and choosing for them would
 * write into an editor they did not mean — so it lists them and stops.
 */
function detectClient(workspace: string): string {
  const found = clientIds().filter((id) => isInstalled(clientSpec(id), workspace, existsSync));

  if (found.length === 1) return found[0]!;
  if (found.length === 0) {
    throw new Error(`no supported editor was detected here; name one with --client: ${clientIds().join(", ")}`);
  }
  throw new Error(`several editors are installed; name one with --client: ${found.join(", ")}`);
}

async function uninstall(options: Options, out: Writer): Promise<number> {
  const clientId = requireString(options, "client");
  const spec = clientSpec(clientId);
  const scope = stringFlag(options, "scope") ?? defaultScope(spec);

  const outcome = await adapterFor(spec).remove({
    spec,
    clientId,
    scope,
    workspace: process.cwd(),
    dryRun: options.flags.has("dry-run"),
  });

  if (options.flags.has("json")) {
    out(JSON.stringify({ client: clientId, scope, ...outcome }, null, 2));
    return 0;
  }

  out(outcome.changed ? `Removed ${SERVER_NAME} from ${outcome.target}.` : `${SERVER_NAME} was not configured at ${outcome.target}.`);
  return 0;
}

async function list(options: Options, out: Writer): Promise<number> {
  const workspace = process.cwd();
  const rows = [];

  for (const id of clientIds()) {
    const spec = clientSpec(id);
    const scope = defaultScope(spec);
    const installed = isInstalled(spec, workspace, existsSync);
    const found = await configuredScopes(id, workspace);

    rows.push({
      client: id,
      displayName: spec.displayName,
      installed,
      configured: found.length > 0,
      scopes: found,
      defaultTarget: configPath(spec, scope, workspace),
    });
  }

  if (options.flags.has("json")) {
    out(JSON.stringify(rows, null, 2));
    return 0;
  }

  for (const row of rows) {
    const state = row.configured ? "configured" : row.installed ? "not configured" : "not detected";
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
    const found = await configuredScopes(id, workspace);
    editors.push({
      name: spec.displayName,
      status: found.length > 0 ? "pass" : "info",
      detail:
        found.length > 0
          ? found.map((f) => `${f.scope}: ${f.target}`).join(", ")
          : `not configured — npx ${PACKAGE_NAME} init --client ${id}`,
    });
  }

  // No configured editor at all is a real fault: nothing can reach Noetive.
  // One configured editor and three ignored ones is a working setup.
  const configured = editors.filter((e) => e.status === "pass");
  if (editors.length > 0 && configured.length === 0) {
    checks.push({
      name: "editors",
      status: "fail",
      detail: `no editor is configured — npx ${PACKAGE_NAME} init --client ${clientIds()[0]}`,
    });
  }

  // The key is only a fault where it is actually needed. An editor configured
  // with --api-key carries its own, and does not depend on this shell.
  const shellKey = (process.env[API_KEY_ENV] ?? "").trim();
  checks.push(keyCheck(shellKey, configured.length > 0));

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
}

/**
 * configuredScopes finds every scope where an editor already has a noetive
 * entry.
 *
 * An editor can legitimately be configured in more than one place — a global
 * entry and a per-project one — and which of them applies depends on where the
 * editor was opened, not on which this command considers the default.
 */
export async function configuredScopes(
  clientId: string,
  workspace: string,
): Promise<{ scope: string; target: string }[]> {
  const spec = clientSpec(clientId);
  const adapter = adapterFor(spec);
  const found: { scope: string; target: string }[] = [];

  for (const scope of Object.keys(spec.scopes)) {
    const report = await adapter.status({ spec, clientId, scope, workspace });
    if (report.configured) found.push({ scope, target: report.target });
  }
  return found;
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
    "client", "scope", "api-key", "namespace", "model", "dimensions",
    "dry-run", "json", "version", "help",
  ]);
  const boolean = new Set(["dry-run", "json", "version", "help"]);

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
