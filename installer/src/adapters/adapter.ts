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
}

export interface StatusReport {
  readonly target: string;
  readonly installed: boolean;
  readonly configured: boolean;
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
