/**
 * find-entry reports whether an editor's config contains the noetive server
 * entry, and exits non-zero when that disagrees with what was expected.
 *
 * Used by .github/workflows/install-clients.yml to read back what an install
 * into a real editor actually produced. A script rather than a heredoc inside
 * the workflow so it can be run by hand against a config that is behaving
 * oddly, which is when this question is most worth asking.
 *
 *     node scripts/find-entry.mjs ~/.claude.json mcpServers present
 *
 * The search is by shape, not by path. Claude Code keys local-scope entries per
 * project inside ~/.claude.json, a layout that has already moved once between
 * releases; asserting a fixed path would turn their next rearrangement into a
 * failure here on an install that still works. What matters is that the server
 * object holds the entry somewhere the editor will find it.
 */
import { readFileSync } from "node:fs";

const [path, topLevelKey, expected] = process.argv.slice(2);

if (!path || !topLevelKey || !["present", "absent"].includes(expected ?? "")) {
  console.error("usage: find-entry.mjs <config> <topLevelKey> <present|absent>");
  process.exit(2);
}

/** The key the installer writes under, and the only one it ever touches. */
const SERVER_NAME = "noetive";

const text = readFileSync(path, "utf8");

// TOML is matched as text. Parsing it here would mean this script carrying an
// opinion about a format nothing in the product reads, to answer a question a
// table header already answers.
const found = path.endsWith(".toml")
  ? text.includes(`[${topLevelKey}.${SERVER_NAME}]`)
  : contains(JSON.parse(text), topLevelKey);

if (found !== (expected === "present")) {
  console.error(`${path}: expected the ${SERVER_NAME} entry to be ${expected}, and it is not`);
  console.error(text.slice(0, 2000));
  process.exit(1);
}

console.log(`${path}: ${SERVER_NAME} is ${expected}`);

/** contains reports whether any object under value holds the server entry. */
function contains(value, topLevelKey) {
  if (value === null || typeof value !== "object") return false;
  if (!Array.isArray(value)) {
    const servers = value[topLevelKey];
    if (servers !== null && typeof servers === "object" && SERVER_NAME in servers) return true;
  }
  return Object.values(value).some((child) => contains(child, topLevelKey));
}
