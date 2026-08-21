import { ClientSpec } from "../clients";
import { ClientAdapter } from "./adapter";
import { CliDelegateAdapter } from "./cliDelegate";
import { MergeAdapter } from "./generic";

/**
 * adapterFor picks the implementation for a client.
 *
 * The manifest's install strategy is the whole decision. An editor that keeps
 * MCP servers in a JSON object keyed by server name needs no code here at all —
 * it declares "file-merge" and is served by the same adapter as every other
 * such editor. An editor configured through its own command declares
 * "cli-delegate" and needs no code either; which command, which flags and what
 * to do without it are all manifest fields.
 */
export function adapterFor(spec: ClientSpec): ClientAdapter {
  switch (spec.install) {
    case "cli-delegate":
      return new CliDelegateAdapter();
    case "file-merge":
      return new MergeAdapter();
  }
}

export * from "./adapter";
export { CliDelegateAdapter } from "./cliDelegate";
export { MergeAdapter } from "./generic";
