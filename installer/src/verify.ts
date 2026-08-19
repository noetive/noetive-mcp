import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

/**
 * checksumOf returns the lowercase hex SHA-256 of a file's bytes.
 *
 *     checksumOf("/tmp/noetive-mcp") // "9f86d081..."
 */
export function checksumOf(path: string): string {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

/**
 * parseChecksums reads the `checksums.txt` a release publishes: one
 * `<hex>  <filename>` pair per line, as sha256sum writes them.
 *
 * Malformed lines are skipped rather than failing the parse, because a release
 * file may legitimately carry blank lines or a signature header, and a strict
 * parse would turn a cosmetic difference into a failed install.
 */
export function parseChecksums(text: string): Map<string, string> {
  const sums = new Map<string, string>();

  for (const line of text.split("\n")) {
    const match = /^([0-9a-fA-F]{64})\s+\*?(\S+)$/.exec(line.trim());
    if (!match) continue;
    sums.set(match[2]!, match[1]!.toLowerCase());
  }

  return sums;
}

/**
 * verifyDownload throws unless the file at path matches the checksum published
 * for assetName.
 *
 * An unlisted asset is a failure, not a pass. Treating "no checksum published"
 * as acceptable would make the whole check bypassable by anyone who can also
 * serve the download — which is exactly the attacker this defends against.
 */
export function verifyDownload(path: string, assetName: string, checksumsText: string): void {
  const published = parseChecksums(checksumsText).get(assetName);
  if (!published) {
    throw new Error(`no published checksum for ${assetName}; refusing to install an unverified binary`);
  }

  const actual = checksumOf(path);
  if (actual !== published) {
    throw new Error(
      `checksum mismatch for ${assetName}: expected ${published}, got ${actual}. ` +
        `The download was corrupted or tampered with; it has not been installed.`,
    );
  }
}
