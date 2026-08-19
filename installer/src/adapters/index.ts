import { ClientSpec } from "../clients";
import { ClientAdapter } from "./adapter";
import { ClaudeCodeAdapter } from "./claudeCode";
import { MergeAdapter } from "./generic";

/**
 * adapterFor picks the implementation for a client.
 *
 * The manifest's install strategy is the whole decision. An editor that keeps
 * MCP servers in a JSON object keyed by server name needs no code here at all —
 * it declares "file-merge" and is served by the same adapter as every other
 * such editor.
 */
export function adapterFor(spec: ClientSpec): ClientAdapter {
  switch (spec.install) {
    case "cli-delegate":
      return new ClaudeCodeAdapter();
    case "file-merge":
      return new MergeAdapter();
  }
}

export * from "./adapter";
export { ClaudeCodeAdapter } from "./claudeCode";
export { MergeAdapter } from "./generic";
