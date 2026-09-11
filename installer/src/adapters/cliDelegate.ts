import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";

import { ClientSpec, configPath, expand, isInstalled, PACKAGE_NAME, SERVER_NAME } from "../clients";
import { redactKey } from "../configFile";
import { entryEnv, renderYamlEntry } from "../serverEntry";
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
/** The parts of a client's `cli` block this adapter needs to build one call. */
type CliInvocation = Pick<
  NonNullable<ClientSpec["cli"]>,
  "command" | "removeArgs" | "envArg" | "envStyle" | "interactive"
>;

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
      return { target: `${spec.command} (CLI)`, changed: false, diff: redactKey(`+ ${spec.command} ${args.join(" ")}`) };
    }

    this.assertTerminal(request);

    // Re-running must be a no-op rather than a duplicate, and the CLI refuses a
    // name it already knows, so the prior entry is cleared first.
    this.removeViaCli(request);

    const result = this.run(spec.command, args, process.env, spec.interactive);
    if (result.status !== 0) {
      throw new Error(
        redactKey(`\`${spec.command} ${args.join(" ")}\` failed: ${result.stderr.trim() || `exit ${result.status}`}`),
      );
    }

    const target = `${spec.command} mcp (scope ${request.scope})`;

    // An interactive CLI exiting zero means it ran, not that it saved. Hermes
    // exits zero after "Cancelled" just as it does after writing, and the only
    // things that distinguish the two are its printed output and whether the
    // config changed — one we will not read, the other we cannot parse. So the
    // answer is reported as unknown rather than guessed in either direction.
    if (spec.interactive) {
      return {
        target,
        changed: true,
        unverified: `${request.spec.displayName} was given the terminal and asked you directly; it does not report back what it saved.`,
      };
    }

    return { target, changed: true };
  }

  async remove(request: Omit<InstallRequest, "entryOptions">): Promise<InstallOutcome> {
    if (!this.cliAvailable(request)) {
      this.assertFallbackAllowed(request.spec);
      return this.fallback.remove(request);
    }

    if (request.dryRun) {
      return { target: `${request.spec.cli!.command} (CLI)`, changed: true };
    }

    this.assertTerminal(request);

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
    //
    // Without such a command there is no answer to give. Hermes has none: its
    // `mcp list` exits zero whatever it finds, and it has no per-server query
    // at all. Saying "not configured" there would be a guess dressed as a fact.
    if (!spec?.statusArgs || !this.cliAvailable(request)) {
      return { target, installed, configured: "unknown" };
    }

    const args = this.expandArgs(spec.statusArgs, request, {});
    return { target, installed, configured: this.run(spec.command, args, process.env).status === 0 ? "yes" : "no" };
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
    request: { scope: string; spec: { cli?: CliInvocation } },
    env: Readonly<Record<string, string>>,
  ): string[] {
    const values = { scope: request.scope, serverName: SERVER_NAME, packageName: PACKAGE_NAME };
    const { envArg, envStyle = "repeated" } = request.spec.cli ?? {};

    return template.flatMap((arg) => {
      if (arg !== "${env}") return [expand(arg, values)];
      if (!envArg) return [];

      const pairs = Object.entries(env).map(([name, value]) => `${name}=${value}`);
      if (pairs.length === 0) return [];

      // Grouped means the flag takes every pair at once. Repeating it against a
      // CLI built that way is not additive — the second occurrence replaces the
      // first — so all but the last pair vanish, and the API key is written
      // first, which makes it the one that goes.
      return envStyle === "grouped" ? [envArg, ...pairs] : pairs.flatMap((pair) => [envArg, pair]);
    });
  }

  /**
   * assertTerminal refuses to drive a prompting CLI down a pipe.
   *
   * A prompt reading a closed stdin takes its cancelling answer, and Hermes'
   * `mcp add` then exits zero having saved nothing — so the install would
   * report success for a config it never wrote. Refusing is the only honest
   * answer, and it carries the entry so the user is not left to reconstruct it.
   */
  private assertTerminal(request: InstallRequest | Omit<InstallRequest, "entryOptions">): void {
    const spec = request.spec.cli;
    if (!spec?.interactive || (process.stdin.isTTY && process.stdout.isTTY)) return;

    const entryOptions = "entryOptions" in request ? request.entryOptions : {};
    throw new Error(
      `${request.spec.displayName} is configured through \`${spec.command} mcp\`, which asks which tools to enable. ` +
        `That needs a terminal and this is not one, so nothing was written. Run this again from an interactive shell, ` +
        `or put this in ${configPath(request.spec, request.scope, request.workspace)} yourself:\n\n` +
        renderYamlEntry(request.spec, entryOptions)
          .split("\n")
          .map((line) => `    ${line}`)
          .join("\n"),
    );
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

  private removeViaCli(request: { spec: { cli?: CliInvocation }; scope: string }): boolean {
    const spec = request.spec.cli;
    if (!spec?.removeArgs) return false;

    // Interactive here too: Hermes asks before removing an entry it finds, and
    // a question asked down a pipe is a question the user never sees.
    return this.run(spec.command, this.expandArgs(spec.removeArgs, request, {}), process.env, spec.interactive).status === 0;
  }
}

export interface RunResult {
  readonly status: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** Runner executes an external command. Injected so tests never shell out. */
export type Runner = (command: string, args: string[], env: NodeJS.ProcessEnv, interactive?: boolean) => RunResult;

/** How long a CLI that is not talking to the user gets before it is given up on. */
const COMMAND_TIMEOUT_MS = 60_000;

/**
 * systemRunner executes a real command.
 *
 * `shell: false` is a security boundary, not a default. Arguments here include
 * a scope name, a server name and an API key that ultimately come from user
 * input, and running through a shell would make a semicolon or a backtick in
 * any of them execute as a command. Without the shell, they are passed to the
 * process verbatim and can only ever be arguments.
 *
 * An interactive command inherits this process's streams, so its questions
 * reach the user and their answers reach it. Nothing is captured in that mode
 * and nothing needs to be: what it asked and what they said is on their screen,
 * and reading it back would make this installer depend on the shape of an
 * interface it does not own.
 *
 * Everything else gets a closed stdin and a deadline. Both guard the same
 * failure: a CLI that decides to ask a question on a run where nobody is
 * watching hangs with its output captured, so the user sees no prompt, no
 * progress and no error — just a command that never returns.
 */
export const systemRunner: Runner = (command, args, env, interactive) => {
  if (interactive) {
    const result = spawnSync(command, args, { env, shell: false, stdio: "inherit" });
    return { status: result.status ?? 1, stdout: "", stderr: result.error ? result.error.message : "" };
  }

  const result = spawnSync(command, args, {
    encoding: "utf8",
    env,
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
    timeout: COMMAND_TIMEOUT_MS,
  });

  // A timed-out or unspawnable command reports nothing on stderr, so the reason
  // it failed is only in `error`. Preferring the stream when it has content
  // keeps a real error message from being replaced by "spawnSync ETIMEDOUT".
  const stderr = result.stderr?.trim() ? result.stderr : (result.error?.message ?? result.stderr ?? "");
  return { status: result.status ?? 1, stdout: result.stdout ?? "", stderr };
};

const defaultRunner = systemRunner;
