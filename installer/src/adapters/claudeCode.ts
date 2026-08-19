import { spawnSync } from "node:child_process";

import { expand, PACKAGE_NAME, SERVER_NAME } from "../clients";
import { API_KEY_ENV } from "../serverEntry";
import { ClientAdapter, InstallOutcome, InstallRequest, StatusReport } from "./adapter";
import { MergeAdapter } from "./generic";

/**
 * ClaudeCodeAdapter defers to the `claude` CLI when it is on PATH, and falls
 * back to editing the config file when it is not.
 *
 * The CLI is preferred because Claude Code's user-scope entries are keyed per
 * project inside ~/.claude.json — a layout the file path alone does not
 * describe, and one that has already changed once between releases. Writing it
 * by hand means encoding a private layout that the CLI knows authoritatively.
 *
 * The file fallback exists because a user may have Claude Code without its CLI
 * on PATH, and refusing to configure them at all would be worse than writing
 * the layout we do know.
 */
export class ClaudeCodeAdapter implements ClientAdapter {
  private readonly fallback = new MergeAdapter();

  constructor(private readonly run: Runner = defaultRunner) {}

  async install(request: InstallRequest): Promise<InstallOutcome> {
    if (!this.cliAvailable(request)) {
      return this.fallback.install(request);
    }

    const spec = request.spec.cli!;
    const args = spec.args.map((arg) =>
      expand(arg, { scope: request.scope, serverName: SERVER_NAME, packageName: PACKAGE_NAME }),
    );

    if (request.dryRun) {
      return { target: `${spec.command} (CLI)`, changed: false, diff: `+ ${spec.command} ${args.join(" ")}` };
    }

    // Re-running must be a no-op rather than a duplicate, and the CLI refuses a
    // name it already knows, so the prior entry is cleared first.
    this.removeViaCli(request);

    const env: NodeJS.ProcessEnv = { ...process.env };
    if (request.entryOptions.apiKey) env[API_KEY_ENV] = request.entryOptions.apiKey;

    const result = this.run(spec.command, args, env);
    if (result.status !== 0) {
      throw new Error(`\`${spec.command} ${args.join(" ")}\` failed: ${result.stderr.trim() || `exit ${result.status}`}`);
    }

    return { target: `${spec.command} mcp (scope ${request.scope})`, changed: true };
  }

  async remove(request: Omit<InstallRequest, "entryOptions">): Promise<InstallOutcome> {
    if (!this.cliAvailable(request)) {
      return this.fallback.remove(request);
    }

    if (request.dryRun) {
      return { target: `${request.spec.cli!.command} (CLI)`, changed: true };
    }

    const removed = this.removeViaCli(request);
    return { target: `${request.spec.cli!.command} mcp (scope ${request.scope})`, changed: removed };
  }

  async status(request: Omit<InstallRequest, "entryOptions" | "dryRun">): Promise<StatusReport> {
    // Status reads the file even when the CLI is present: `claude mcp list`
    // output is a human-facing format with no stability promise, and parsing it
    // would break on a wording change.
    return this.fallback.status(request);
  }

  private cliAvailable(request: { spec: { cli?: { command: string } } }): boolean {
    const command = request.spec.cli?.command;
    if (!command) return false;
    return this.run(command, ["--version"], process.env).status === 0;
  }

  private removeViaCli(request: { spec: { cli?: { command: string; removeArgs?: readonly string[] } }; scope: string }): boolean {
    const spec = request.spec.cli;
    if (!spec?.removeArgs) return false;

    const args = spec.removeArgs.map((arg) =>
      expand(arg, { scope: request.scope, serverName: SERVER_NAME, packageName: PACKAGE_NAME }),
    );
    return this.run(spec.command, args, process.env).status === 0;
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
 * a scope name and a server name that ultimately come from user input, and
 * running through a shell would make a semicolon or a backtick in any of them
 * execute as a command. Without the shell, they are passed to the process
 * verbatim and can only ever be arguments.
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
