import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";

import { ClientSpec, configPath, expand, isInstalled, PACKAGE_NAME, SERVER_NAME } from "../clients";
import { entryEnv } from "../serverEntry";
import { ClientAdapter, InstallOutcome, InstallRequest, StatusReport } from "./adapter";
import { MergeAdapter } from "./generic";

/**
 * CliDelegateAdapter configures an editor by running the editor's own CLI.
 *
 * That is preferred wherever the editor owns a config layout the file path does
 * not describe. Claude Code keys user-scope entries per project inside
 * ~/.claude.json — a layout that has already changed once between releases —
 * and Codex keeps its servers in TOML rather than the JSON this installer
 * merges. In both cases the CLI knows the layout authoritatively.
 *
 * What happens when the command is missing is a manifest decision, not this
 * class's: an editor whose file we can write falls back to editing it, and an
 * editor whose file we cannot write refuses. Guessing the second case would
 * mean writing JSON into a TOML file and reporting success.
 */
export class CliDelegateAdapter implements ClientAdapter {
  private readonly fallback = new MergeAdapter();

  constructor(private readonly run: Runner = defaultRunner) {}

  async install(request: InstallRequest): Promise<InstallOutcome> {
    if (!this.cliAvailable(request)) {
      this.assertFallbackAllowed(request.spec);
      return this.fallback.install(request);
    }

    const spec = request.spec.cli!;
    const args = this.expandArgs(spec.args, request, entryEnv(request.spec, request.entryOptions));

    if (request.dryRun) {
      return { target: `${spec.command} (CLI)`, changed: false, diff: `+ ${spec.command} ${args.join(" ")}` };
    }

    // Re-running must be a no-op rather than a duplicate, and the CLI refuses a
    // name it already knows, so the prior entry is cleared first.
    this.removeViaCli(request);

    const result = this.run(spec.command, args, process.env);
    if (result.status !== 0) {
      throw new Error(`\`${spec.command} ${args.join(" ")}\` failed: ${result.stderr.trim() || `exit ${result.status}`}`);
    }

    return { target: `${spec.command} mcp (scope ${request.scope})`, changed: true };
  }

  async remove(request: Omit<InstallRequest, "entryOptions">): Promise<InstallOutcome> {
    if (!this.cliAvailable(request)) {
      this.assertFallbackAllowed(request.spec);
      return this.fallback.remove(request);
    }

    if (request.dryRun) {
      return { target: `${request.spec.cli!.command} (CLI)`, changed: true };
    }

    const removed = this.removeViaCli(request);
    return { target: `${request.spec.cli!.command} mcp (scope ${request.scope})`, changed: removed };
  }

  async status(request: Omit<InstallRequest, "entryOptions" | "dryRun">): Promise<StatusReport> {
    const spec = request.spec.cli;

    // Where a file fallback exists the file is the better source: it answers
    // even with the CLI absent, and `claude mcp list` is a human-facing format
    // with no stability promise, so parsing it would break on a wording change.
    if (spec?.fallback !== "none") {
      return this.fallback.status(request);
    }

    const target = configPath(request.spec, request.scope, request.workspace);
    const installed = isInstalled(request.spec, request.workspace, existsSync);

    // No file this installer can read, so the CLI is asked instead. Only its
    // exit status is used — that is an existence check, not output parsing, and
    // survives any rewording of what it prints.
    if (!spec?.statusArgs || !this.cliAvailable(request)) {
      return { target, installed, configured: false };
    }

    const args = this.expandArgs(spec.statusArgs, request, {});
    return { target, installed, configured: this.run(spec.command, args, process.env).status === 0 };
  }

  /**
   * expandArgs fills the manifest's placeholders, splicing one flag pair per
   * environment entry in at ${env}.
   *
   * The position matters and belongs to the manifest: every CLI here takes its
   * flags before the `--` that introduces the launch command, so an appended
   * flag would be handed to the server process instead of to the editor.
   */
  private expandArgs(
    template: readonly string[],
    request: { scope: string; spec: { cli?: { envArg?: string } } },
    env: Readonly<Record<string, string>>,
  ): string[] {
    const values = { scope: request.scope, serverName: SERVER_NAME, packageName: PACKAGE_NAME };
    const envArg = request.spec.cli?.envArg;

    return template.flatMap((arg) => {
      if (arg !== "${env}") return [expand(arg, values)];
      if (!envArg) return [];
      return Object.entries(env).flatMap(([name, value]) => [envArg, `${name}=${value}`]);
    });
  }

  /**
   * assertFallbackAllowed refuses to edit a config the manifest says we cannot
   * write, and is the reason a missing `codex` does not put JSON into a TOML
   * file.
   *
   * The refusal names the command rather than reporting a generic failure: the
   * user has an editor we support and a one-line fix, and the alternative is an
   * installer that appears to work and configures nothing.
   */
  private assertFallbackAllowed(spec: ClientSpec): void {
    if (spec.cli?.fallback !== "none") return;

    throw new Error(
      `${spec.displayName} is configured through its \`${spec.cli.command}\` command, which is not on PATH. ` +
        `Its config is not a format this installer can edit safely, so nothing was written. Install the CLI and run this again.`,
    );
  }

  private cliAvailable(request: { spec: { cli?: { command: string } } }): boolean {
    const command = request.spec.cli?.command;
    if (!command) return false;
    return this.run(command, ["--version"], process.env).status === 0;
  }

  private removeViaCli(request: { spec: { cli?: { command: string; removeArgs?: readonly string[]; envArg?: string } }; scope: string }): boolean {
    const spec = request.spec.cli;
    if (!spec?.removeArgs) return false;

    return this.run(spec.command, this.expandArgs(spec.removeArgs, request, {}), process.env).status === 0;
  }
}

export interface RunResult {
  readonly status: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** Runner executes an external command. Injected so tests never shell out. */
export type Runner = (command: string, args: string[], env: NodeJS.ProcessEnv) => RunResult;

/**
 * systemRunner executes a real command.
 *
 * `shell: false` is a security boundary, not a default. Arguments here include
 * a scope name, a server name and an API key that ultimately come from user
 * input, and running through a shell would make a semicolon or a backtick in
 * any of them execute as a command. Without the shell, they are passed to the
 * process verbatim and can only ever be arguments.
 */
export const systemRunner: Runner = (command, args, env) => {
  const result = spawnSync(command, args, { encoding: "utf8", env, shell: false });
  return {
    status: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? (result.error ? result.error.message : ""),
  };
};

const defaultRunner = systemRunner;
