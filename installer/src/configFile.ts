import { copyFileSync, existsSync, mkdirSync, readFileSync, renameSync, unlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

import { applyEdits, modify, parse as parseJsonc, ParseError, printParseErrorCode } from "jsonc-parser";

const FORMATTING = { insertSpaces: true, tabSize: 2, eol: "\n" } as const;

/**
 * read returns a config file's text, or an empty object literal when the file
 * does not exist yet. A missing file and an empty file are the same situation
 * from the caller's point of view: there is nothing to preserve.
 */
export function read(path: string): string {
  if (!existsSync(path)) return "{}";

  const text = readFileSync(path, "utf8");
  return text.trim() === "" ? "{}" : text;
}

/**
 * parseObject decodes config text, tolerating comments and trailing commas
 * because editors write both.
 *
 * A file that is not parseable at all is an error rather than an empty object:
 * treating a corrupt config as empty would let the next write silently discard
 * every server the user had configured.
 */
export function parseObject(text: string, path: string): Record<string, unknown> {
  const errors: ParseError[] = [];
  const value = parseJsonc(text, errors, { allowTrailingComma: true, disallowComments: false });

  if (errors.length > 0) {
    const first = errors[0]!;
    throw new Error(
      `${path} is not valid JSON (${printParseErrorCode(first.error)} at offset ${first.offset}); fix or move it and re-run`,
    );
  }
  if (value === undefined || value === null) return {};
  if (typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${path} must contain a JSON object at the top level`);
  }
  return value as Record<string, unknown>;
}

/**
 * setEntry returns config text with `path` set to value, preserving every byte
 * it does not have to touch.
 *
 * Comments, key order and indentation elsewhere in the file survive: the edit
 * is computed as a range replacement rather than by re-serialising the parsed
 * document. Re-serialising is what destroys a user's annotated config.
 */
export function setEntry(text: string, path: (string | number)[], value: unknown): string {
  const edits = modify(text, path, value, { formattingOptions: { ...FORMATTING } });
  const next = applyEdits(text, edits);
  return next.endsWith("\n") ? next : `${next}\n`;
}

/** removeEntry deletes a path, leaving the rest of the document untouched. */
export function removeEntry(text: string, path: (string | number)[]): string {
  const edits = modify(text, path, undefined, { formattingOptions: { ...FORMATTING } });
  const next = applyEdits(text, edits);
  return next.endsWith("\n") ? next : `${next}\n`;
}

/** backupPath is where write stashes the previous contents of a config file. */
export function backupPath(path: string): string {
  return `${path}.noetive.bak`;
}

/**
 * write replaces a file atomically after backing up what was there.
 *
 * Temp-file-and-rename is what stops a crash mid-write from leaving an editor
 * with a truncated config and no MCP servers at all. The backup is what lets a
 * failed verification be undone. One rolling backup rather than a timestamped
 * series: the useful operation is undoing the last change, and a series only
 * accumulates litter in the user's config directory.
 *
 * Returns the backup path, or undefined when there was no prior file.
 */
export function write(path: string, text: string): string | undefined {
  mkdirSync(dirname(path), { recursive: true });

  let backup: string | undefined;
  if (existsSync(path)) {
    backup = backupPath(path);
    copyFileSync(path, backup);
  }

  const temp = join(dirname(path), `.noetive-${process.pid}.tmp`);
  writeFileSync(temp, text, { encoding: "utf8", mode: 0o600 });
  renameSync(temp, path);

  return backup;
}

/** restore puts a backup back, used when a write cannot be verified. */
export function restore(path: string, backup: string): void {
  copyFileSync(backup, path);
  unlinkSync(backup);
}

/**
 * diff renders the changed region of a file, so --dry-run shows a user exactly
 * what would happen before anything is written.
 *
 * Matching leading and trailing lines are elided and the remainder is shown as
 * removed-then-added. A config edit is a localized change, so this locates it
 * as precisely as a full diff algorithm would, without being one.
 */
export function diff(before: string, after: string, path: string): string {
  const a = before.split("\n");
  const b = after.split("\n");

  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head += 1;

  let tail = 0;
  while (
    tail < a.length - head &&
    tail < b.length - head &&
    a[a.length - 1 - tail] === b[b.length - 1 - tail]
  ) {
    tail += 1;
  }

  const removed = a.slice(head, a.length - tail);
  const added = b.slice(head, b.length - tail);

  if (removed.length === 0 && added.length === 0) {
    return `${path}: no change`;
  }

  return [
    `--- ${path}`,
    `+++ ${path}`,
    `@@ line ${head + 1} @@`,
    ...removed.map((line) => `-${line}`),
    ...added.map((line) => `+${line}`),
  ].join("\n");
}
