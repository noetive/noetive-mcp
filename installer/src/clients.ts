import { homedir } from "node:os";
import { isAbsolute, join, normalize, resolve } from "node:path";

import manifest from "./manifest/clients.json";

export type ConfigFormat = "json" | "jsonc" | "toml";
export type InstallStrategy = "file-merge" | "cli-delegate";

export interface ScopeSpec {
  readonly path: string;
  readonly default?: boolean;
}

export interface CliSpec {
  readonly command: string;
  readonly args: readonly string[];
  readonly removeArgs?: readonly string[];
  /**
   * The flag this CLI takes one `NAME=value` pair after. Each entry the server
   * needs is spliced into `args` at the `${env}` placeholder.
   */
  readonly envArg?: string;
  /**
   * Args that exit zero when the server is already configured. Used only where
   * there is no file to read; the exit status is the whole answer, so no CLI
   * output is parsed.
   */
  readonly statusArgs?: readonly string[];
  /**
   * What to do when the command is not on PATH. "file-merge" edits the config
   * directly, which is right for an editor whose file we can write. "none"
   * refuses, which is the only safe answer for an editor whose config is not
   * the JSON this installer knows how to merge.
   */
  readonly fallback?: "file-merge" | "none";
}

export interface ClientSpec {
  readonly displayName: string;
  readonly configFormat: ConfigFormat;
  readonly topLevelKey: string;
  readonly install: InstallStrategy;
  readonly expandsVariables: boolean;
  readonly detect?: readonly string[];
  readonly entryExtras?: Readonly<Record<string, unknown>>;
  readonly cli?: CliSpec;
  readonly scopes: Readonly<Record<string, ScopeSpec>>;
  readonly restartHint: string;
  readonly quirks?: readonly string[];
}

/** The key this installer writes under. It is the only key it ever touches. */
export const SERVER_NAME: string = manifest.serverName;

/** The npm package editors are told to run. */
export const PACKAGE_NAME: string = manifest.packageName;

const CLIENTS = manifest.clients as unknown as Readonly<Record<string, ClientSpec>>;

/** clientIds lists every supported editor, in manifest order. */
export function clientIds(): string[] {
  return Object.keys(CLIENTS);
}

/**
 * clientSpec returns the manifest entry for id, or throws naming the ids that
 * do exist. A typo in --client is the most likely way a user reaches this, and
 * the list is more useful than the rejection.
 */
export function clientSpec(id: string): ClientSpec {
  const spec = CLIENTS[id];
  if (!spec) {
    throw new Error(`unknown client ${JSON.stringify(id)}; supported: ${clientIds().join(", ")}`);
  }
  return spec;
}

/** defaultScope is the scope used when --scope is omitted. */
export function defaultScope(spec: ClientSpec): string {
  const named = Object.entries(spec.scopes).find(([, s]) => s.default);
  if (named) return named[0];

  const first = Object.keys(spec.scopes)[0];
  if (!first) throw new Error(`client ${spec.displayName} declares no scopes`);
  return first;
}

/**
 * configPath resolves the file a scope writes to.
 *
 * A leading ~ becomes the home directory and ${workspace} becomes the directory
 * the command was run from, which is what makes project-scoped installs land
 * beside the code rather than in the user's home.
 */
export function configPath(spec: ClientSpec, scope: string, workspace: string): string {
  const declared = spec.scopes[scope];
  if (!declared) {
    throw new Error(`client ${spec.displayName} has no scope ${JSON.stringify(scope)}; supported: ${Object.keys(spec.scopes).join(", ")}`);
  }

  return expandPath(declared.path, workspace);
}

/**
 * expandPath turns a manifest path template into a real path for this machine.
 *
 * The manifest spells paths with forward slashes because that is what every
 * vendor's documentation uses. Substituting into that leaves a Windows path
 * with mixed separators — `C:\Users\me\project/.vscode/mcp.json` — which opens
 * files perfectly well and then fails every comparison against a path built
 * with `join`, so `list` and `doctor` report an editor as unconfigured while
 * looking straight at its config. Normalising once, here, is what stops that
 * difference from leaking into everything downstream.
 */
function expandPath(template: string, workspace: string): string {
  let path = template;
  if (path.startsWith("~/")) path = join(homedir(), path.slice(2));
  path = path.replace("${workspace}", workspace);

  return normalize(isAbsolute(path) ? path : resolve(workspace, path));
}

/**
 * isProjectScoped reports whether a scope writes inside the current directory
 * rather than into the user's home.
 */
export function isProjectScoped(spec: ClientSpec, scope: string): boolean {
  return spec.scopes[scope]?.path.includes("${workspace}") ?? false;
}

/**
 * assertUsableWorkspace refuses a project-scoped install run from the home
 * directory.
 *
 * Copilot's only scope is the workspace, and the published instructions are
 * "run this in your terminal" — which for most people means their home
 * directory. That writes ~/.vscode/mcp.json, a path VS Code never reads as a
 * workspace config, and reports success. The user then has a config file, an
 * editor with no Noetive tools, and nothing connecting the two.
 *
 * Refusing is the only honest outcome: there is no project here to configure.
 */
export function assertUsableWorkspace(spec: ClientSpec, scope: string, workspace: string): void {
  if (!isProjectScoped(spec, scope)) return;
  if (resolve(workspace) !== resolve(homedir())) return;

  const alternatives = Object.keys(spec.scopes).filter((s) => !isProjectScoped(spec, s));
  const suggestion =
    alternatives.length > 0
      ? `Either change to your project directory first, or use --scope ${alternatives[0]}.`
      : `Change to your project directory and run the command again.`;

  throw new Error(
    `the ${scope} scope for ${spec.displayName} writes into the current directory, and you are in your home directory. ` +
      `${spec.displayName} only reads this file inside a project, so configuring it here would do nothing. ${suggestion}`,
  );
}

/**
 * isInstalled reports whether the editor appears to be present, so `list` can
 * distinguish "not configured" from "not installed" — a user with no Kiro does
 * not need to be told their Kiro config is missing.
 *
 * Absence of a detect path is treated as unknown, not as absent: a false
 * negative would refuse to configure an editor that is actually there.
 */
export function isInstalled(spec: ClientSpec, workspace: string, exists: (p: string) => boolean): boolean {
  if (!spec.detect || spec.detect.length === 0) return true;

  return spec.detect.some((candidate) => exists(expandPath(candidate, workspace)));
}

/** expand substitutes ${name} placeholders from values, leaving unknowns alone. */
export function expand(template: string, values: Readonly<Record<string, string>>): string {
  return template.replace(/\$\{(\w+)\}/g, (whole, key: string) => values[key] ?? whole);
}
