import { ClientSpec } from "../clients";
import { EntryOptions } from "../serverEntry";

export interface InstallRequest {
  readonly spec: ClientSpec;
  readonly clientId: string;
  readonly scope: string;
  readonly workspace: string;
  readonly entryOptions: EntryOptions;
  readonly dryRun: boolean;
}

export interface InstallOutcome {
  /** Where the change was made, for the user to inspect or undo. */
  readonly target: string;
  /** Whether anything changed. A no-op re-run reports false. */
  readonly changed: boolean;
  /** A unified diff, populated on a dry run. */
  readonly diff?: string;
  /** The backup taken before writing, if any. */
  readonly backup?: string;
  /**
   * Why this outcome could not be confirmed, where it could not be.
   *
   * Set for a CLI that ran to completion without telling us what it did. It is
   * deliberately not derived from what the CLI printed: reading its output
   * would tie this installer to the wording and layout of somebody else's
   * terminal interface, and a cosmetic change there would silently turn a
   * working install into a reported failure. Better to say we do not know.
   */
  readonly unverified?: string;
}

/**
 * Configured is whether an editor has the noetive entry.
 *
 * Three states rather than two, because "we cannot tell" is a real answer and
 * reporting it as "no" is worse than saying nothing: an editor whose config is
 * a format this installer does not read, behind a CLI with no command that
 * reports one, is indistinguishable from an unconfigured one. Called "no", it
 * makes `doctor` fail on a working install and prescribe the command the user
 * has already run.
 */
export type Configured = "yes" | "no" | "unknown";

export interface StatusReport {
  readonly target: string;
  readonly installed: boolean;
  readonly configured: Configured;
  readonly detail?: string;
}

/**
 * ClientAdapter is everything an editor needs in order to be supported.
 *
 * Most editors keep MCP servers in a JSON object keyed by server name, and are
 * served by the manifest-driven implementation with no code of their own. An
 * adapter is written only when an editor's configuration is not fully described
 * by that file — Claude Code, whose CLI owns a layout the file does not
 * express, is the case this exists for.
 */
export interface ClientAdapter {
  install(request: InstallRequest): Promise<InstallOutcome>;
  remove(request: Omit<InstallRequest, "entryOptions">): Promise<InstallOutcome>;
  status(request: Omit<InstallRequest, "entryOptions" | "dryRun">): Promise<StatusReport>;
}
