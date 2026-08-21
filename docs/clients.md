# Editor configuration

Verified against vendor documentation in August 2026. These details change often — re-check them against each vendor's docs before relying on a row.

Everything here is data, held in [`installer/src/manifest/clients.json`](../installer/src/manifest/clients.json) and validated by the schema beside it. An editor that keeps MCP servers in a JSON object keyed by server name is added by writing a manifest entry, with no new code.

## Supported editors

| Editor | Config | Key | Strategy |
|---|---|---|---|
| Cursor | `~/.cursor/mcp.json`, `${workspace}/.cursor/mcp.json` | `mcpServers` | file merge |
| Claude Code | via `claude mcp add`; falls back to `~/.claude.json`, `${workspace}/.mcp.json` | `mcpServers` | CLI |
| Codex | via `codex mcp add`; no fallback | `[mcp_servers.noetive]` (TOML) | CLI |
| GitHub Copilot | `${workspace}/.vscode/mcp.json` | **`servers`** | file merge (JSONC) |
| Antigravity | `~/.gemini/config/mcp_config.json`, `${workspace}/.agents/mcp_config.json` | `mcpServers` | file merge |
| Kiro | `~/.kiro/settings/mcp.json`, `${workspace}/.kiro/settings/mcp.json` | `mcpServers` | file merge |

Only these six are advertised on noetive.io. Anything else is unsupported until it has a manifest entry and a test fixture.

## What differs between them

**Copilot uses `servers`, not `mcpServers`.** Writing the wrong key produces a valid-looking file that Copilot ignores completely, with no error anywhere. Its config is also JSONC, so it is edited with a comment-preserving parser.

**Claude Code is configured through its own CLI.** User-scope entries are keyed per project inside `~/.claude.json`, a layout the file path alone does not describe and one that has already changed once between releases. Writing it by hand means encoding a private layout; the CLI knows it authoritatively. When `claude` is not on `PATH`, the installer falls back to editing the file, because refusing to configure the editor at all would be worse.

**Codex is configured through its CLI with no fallback.** Its config is TOML, not the JSON the merger writes, so falling back would replace a working `config.toml` with a document Codex cannot read — and the merger's own read-back check would pass, because the JSON it wrote is exactly the JSON it looks for. Without `codex` on `PATH` the install refuses and says so. For the same reason `list` and `doctor` ask `codex mcp get` and use only its exit status; there is no file here to parse, and CLI output has no stability promise.

**Codex does not expand `${VAR}` either.** Its `--env` writes literal values, so writing the placeholder would store the string `${NOETIVE_KEY_SECRET}` and the server would receive it verbatim. As with Kiro, the key comes from `--api-key` or from the environment Codex was launched in.

**A CLI-configured editor still needs its environment.** The manifest's `args` carry an `${env}` placeholder that splices in one `--env NAME=value` pair per variable the server needs. Position matters and belongs to the manifest: every one of these CLIs takes its flags before the `--` that introduces the launch command, so an appended flag is handed to `npx` instead of to the editor. Without the placeholder the CLI is invoked with no environment at all, which configures a server that cannot authenticate and reports success.

**Kiro does not expand `${VAR}` in its config.** Cursor, Claude Code, Copilot and Antigravity resolve environment references at launch, which is how the API key normally stays out of the file. For Kiro the key must come from `--api-key` or from the environment Kiro itself was launched in. Kiro also accepts `disabled` and `autoApprove` keys, which the manifest supplies as entry extras.

**Antigravity shares one config across its IDE, its CLI and its SDK.** It uses `serverUrl` rather than `url` for remote servers — irrelevant to this stdio server, but the reason an Antigravity config cannot be copied from a Cursor one. It accepts a `disabled` flag, supplied as an entry extra.

**The Add to Kiro button and `init` write different entries.** The deeplink carries its own configuration; running `init --client kiro` afterwards replaces it with the canonical one.

## What a write guarantees

- Only the `noetive` key under the editor's server object is touched. Other servers and unrelated top-level keys keep their values, and comments survive.
- Re-running `init` converges: the second run reports no change and leaves the file byte-identical.
- The previous file is copied to `<file>.noetive.bak` before writing, and the write itself is a temp-file-and-rename so an interrupted run cannot truncate a config.
- The entry is read back after writing. If it is not usable, the backup is restored and the command fails.
- A config that does not parse is refused rather than replaced — treating a corrupt file as empty would discard every server the user had.
- `remove` deletes the `noetive` key and nothing else.

Formatting inside the edited object may be normalized by the JSON writer. Content and comments are preserved; byte-identical formatting of the surrounding object is not promised.

## Adding an editor

1. Add an entry to `clients.json` matching `clients.schema.json`.
2. Add a fixture and a case to `installer/test/merge.test.ts`.
3. Run `make emit`. `packaging/install.json` gains the editor's published install command, and a deeplink too if `tools/manifest.yaml` declares one for it. CI fails when that file is stale.
4. If the editor's configuration is not fully described by that entry — as with Claude Code and Codex — declare `install: cli-delegate` and its `cli` block. Both are served by the same `CliDelegateAdapter`; neither needed new code. A genuinely new shape needs an adapter implementing `ClientAdapter`, selected from `adapterFor`.
