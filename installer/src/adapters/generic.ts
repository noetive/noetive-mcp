import { existsSync } from "node:fs";

import { configPath, entryKeyPath, isInstalled, SERVER_NAME } from "../clients";
import * as configFile from "../configFile";
import { buildEntry } from "../serverEntry";
import { ClientAdapter, InstallOutcome, InstallRequest, StatusReport } from "./adapter";

/**
 * MergeAdapter configures any editor that keeps MCP servers in a JSON object
 * keyed by server name. It is driven entirely by the client manifest, which is
 * what makes adding such an editor a data change rather than a code change.
 *
 * It touches exactly one key, the one named by SERVER_NAME, and rewrites the
 * file by range edit, so sibling servers, comments and formatting survive
 * untouched.
 */
export class MergeAdapter implements ClientAdapter {
  async install(request: InstallRequest): Promise<InstallOutcome> {
    const target = configPath(request.spec, request.scope, request.workspace);
    const before = configFile.read(target);

    // Parsing first turns a corrupt file into a refusal rather than a
    // clobbering write.
    configFile.parseObject(before, target);

    const entry = buildEntry(request.spec, request.entryOptions);
    const after = configFile.setEntry(before, entryKeyPath(request.spec, request.scope, request.workspace), entry);

    if (after === before) {
      return { target, changed: false };
    }
    if (request.dryRun) {
      return { target, changed: true, diff: configFile.diff(before, after, target) };
    }

    const backup = configFile.write(target, after);

    // Verify by reading back what was written. A write that succeeded but did
    // not produce a usable entry is worse than a failed write, because nothing
    // tells the user until their editor silently has no Noetive tools.
    const verification = this.verify(target, request);
    if (verification) {
      if (backup) configFile.restore(target, backup);
      throw new Error(`${target} was written but does not contain a usable ${SERVER_NAME} entry (${verification}); the previous file has been restored`);
    }

    return backup ? { target, changed: true, backup } : { target, changed: true };
  }

  async remove(request: Omit<InstallRequest, "entryOptions">): Promise<InstallOutcome> {
    const target = configPath(request.spec, request.scope, request.workspace);
    if (!existsSync(target)) {
      return { target, changed: false };
    }

    const before = configFile.read(target);
    configFile.parseObject(before, target);

    const after = configFile.removeEntry(before, entryKeyPath(request.spec, request.scope, request.workspace));
    if (after === before) {
      return { target, changed: false };
    }
    if (request.dryRun) {
      return { target, changed: true, diff: configFile.diff(before, after, target) };
    }

    const backup = configFile.write(target, after);
    return backup ? { target, changed: true, backup } : { target, changed: true };
  }

  async status(request: Omit<InstallRequest, "entryOptions" | "dryRun">): Promise<StatusReport> {
    const target = configPath(request.spec, request.scope, request.workspace);
    const installed = isInstalled(request.spec, request.workspace, existsSync);

    if (!existsSync(target)) {
      return { target, installed, configured: "no" };
    }

    try {
      const entry = this.entryAt(target, entryKeyPath(request.spec, request.scope, request.workspace));
      if (!entry) return { target, installed, configured: "no" };

      const detail = typeof entry.command === "string" ? `${entry.command} ${(entry.args as string[] | undefined)?.join(" ") ?? ""}`.trim() : undefined;
      return detail ? { target, installed, configured: "yes", detail } : { target, installed, configured: "yes" };
    } catch (err) {
      return { target, installed, configured: "no", detail: (err as Error).message };
    }
  }

  /** verify returns a reason the written entry is unusable, or undefined. */
  private verify(target: string, request: InstallRequest): string | undefined {
    let entry: Record<string, unknown> | undefined;
    try {
      entry = this.entryAt(target, entryKeyPath(request.spec, request.scope, request.workspace));
    } catch (err) {
      return (err as Error).message;
    }

    if (!entry) return `no entry at ${entryKeyPath(request.spec, request.scope, request.workspace).join(".")}`;
    if (typeof entry.command !== "string" || entry.command === "") return "the entry has no command";
    if (!Array.isArray(entry.args)) return "the entry has no args";
    return undefined;
  }

  /** entryAt walks keyPath from the root of target, or returns undefined where it breaks off. */
  private entryAt(target: string, keyPath: readonly string[]): Record<string, unknown> | undefined {
    let node: unknown = configFile.parseObject(configFile.read(target), target);
    for (const key of keyPath) {
      if (!node || typeof node !== "object" || Array.isArray(node)) return undefined;
      node = (node as Record<string, unknown>)[key];
    }

    if (!node || typeof node !== "object" || Array.isArray(node)) return undefined;
    return node as Record<string, unknown>;
  }
}
