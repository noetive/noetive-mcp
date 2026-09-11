# Editor configuration

Verified against vendor documentation in August 2026, and the Hermes row in September 2026. These details change often, so re-check them against each vendor's docs before relying on a row.

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
| Hermes | `~/.hermes/config.yaml`, via `hermes mcp add`; no fallback | `mcp_servers` (YAML) | CLI, interactive |

Only these seven are advertised on noetive.io. Anything else is unsupported until it has a manifest entry and a test fixture.

## What differs between them

**Copilot uses `servers`, not `mcpServers`.** Writing the wrong key produces a valid-looking file that Copilot ignores completely, with no error anywhere. Its config is also JSONC, so it is edited with a comment-preserving parser.

**Claude Code is configured through its own CLI.** User-scope entries are keyed per project inside `~/.claude.json`, a layout the file path alone does not describe and one that has already changed once between releases. Writing it by hand means encoding a private layout; the CLI knows it authoritatively. When `claude` is not on `PATH`, the installer falls back to editing the file, because refusing to configure the editor at all would be worse.

**Codex is configured through its CLI with no fallback.** Its config is TOML, not the JSON the merger writes, so falling back would replace a working `config.toml` with a document Codex cannot read, and the merger's own read-back check would pass, because the JSON it wrote is exactly the JSON it looks for. Without `codex` on `PATH` the install refuses and says so. For the same reason `list` and `doctor` ask `codex mcp get` and use only its exit status; there is no file here to parse, and CLI output has no stability promise.

**Codex does not expand `${VAR}` either.** Its `--env` writes literal values, so writing the placeholder would store the string `${NOETIVE_KEY_SECRET}` and the server would receive it verbatim. As with Kiro, the key comes from `--api-key` or from the environment Codex was launched in.

**Hermes raises the stakes of the same mistake.** Its `config.yaml` is not an MCP file — the same document carries the model, the profiles and the approval settings for the whole agent. It is CLI-only with no fallback for that reason.

**Hermes asks the user questions, so it is given the terminal.** `hermes mcp add` connects to the server, lists its tools and asks which to enable. Down a pipe that question reads EOF and Hermes takes the cancelling answer, then exits zero having saved nothing — so a piped install would report a configured editor that was never configured. It is run on the user's own terminal instead, and refused outright where there is none. The refusal carries the entry to paste, because a refusal that does not is just a slower dead end.

**Nothing Hermes prints is read.** It exits zero whether it saved or the user backed out, and the only things that distinguish the two are its output and its config file — one this installer will not parse, the other it cannot. Binding the result to the wording or layout of somebody else's terminal interface would mean a cosmetic change there turns a working install into a reported failure. So `init` reports what it handed over rather than what Hermes did with it, and because no `hermes mcp` command answers whether a single server is configured, `list` and `doctor` report Hermes as *cannot tell* rather than guessing. Calling it "not configured" would fail `doctor` on a working install and prescribe the command the user has already run.

**Its flags are not shaped like the others'.** `--args` takes the remainder of the command line, so it goes last and each argument is its own word; joined into one string it becomes a package name npx will never find. `--env` takes every pair after a single flag, and repeating the flag replaces the pairs rather than adding to them — which would drop the API key, since that one is written first. The manifest says which of the two shapes a CLI wants.

**Hermes does not pass its own environment down to a server.** A stdio server receives the `env` its entry declares plus a small baseline, and nothing else. An entry written without an environment is not a server that inherits the key from the shell — it is a server that connects, lists its tools, and refuses every call. Hermes expands `${VAR}` and `${env:VAR}` when it connects, resolved from `~/.hermes/.env` first and the process environment second, so the placeholder is what stays on disk and the key can live in either place.

**A CLI-configured editor still needs its environment.** The manifest's `args` carry an `${env}` placeholder that splices in the pairs the server needs. Position matters and belongs to the manifest: Claude Code and Codex take their flags before the `--` that introduces the launch command, so an appended flag is handed to `npx` instead of to the editor, and Hermes takes the launch command as flags of its own with nothing after them. Without the placeholder the CLI is invoked with no environment at all, which configures a server that cannot authenticate and reports success.

**Kiro does not expand `${VAR}` in its config.** Cursor, Claude Code, Copilot and Antigravity resolve environment references at launch, which is how the API key normally stays out of the file. For Kiro the key must come from `--api-key` or from the environment Kiro itself was launched in. Kiro also accepts `disabled` and `autoApprove` keys, which the manifest supplies as entry extras.

**Antigravity shares one config across its IDE, its CLI and its SDK.** It uses `serverUrl` rather than `url` for remote servers. That is irrelevant to this stdio server, but it is the reason an Antigravity config cannot be copied from a Cursor one. It accepts a `disabled` flag, supplied as an entry extra.

**A one-click button and `init` write different entries.** The Add to Kiro and Add to Hermes deeplinks carry their own configuration; running `init --client kiro` or `init --client hermes` afterwards replaces it with the canonical one. `hermes://` is handled by the Hermes desktop app alone, so the button is not a substitute for the command.

## What a write guarantees

- Only the `noetive` key under the editor's server object is touched. Other servers and unrelated top-level keys keep their values, and comments survive.
- Re-running `init` converges: the second run reports no change and leaves the file byte-identical.
- The previous file is copied to `<file>.noetive.bak` before writing, and the write itself is a temp-file-and-rename so an interrupted run cannot truncate a config.
- The entry is read back after writing. If it is not usable, the backup is restored and the command fails.
- A config that does not parse is refused rather than replaced. Treating a corrupt file as empty would discard every server the user had.
- `remove` deletes the `noetive` key and nothing else.

Formatting inside the edited object may be normalized by the JSON writer. Content and comments are preserved; byte-identical formatting of the surrounding object is not promised.

## Adding an editor

1. Add an entry to `clients.json` matching `clients.schema.json`.
2. Add a fixture and a case to `installer/test/merge.test.ts`.
3. Run `make emit`. `packaging/install.json` gains the editor's published install command, and a deeplink too if `tools/manifest.yaml` declares one for it. CI fails when that file is stale.
4. If the editor's configuration is not fully described by that entry, as with Claude Code, Codex and Hermes, declare `install: cli-delegate` and its `cli` block. All three are served by the same `CliDelegateAdapter`; none needed new code. A genuinely new shape needs an adapter implementing `ClientAdapter`, selected from `adapterFor`.
5. An editor whose config is neither JSON nor JSONC must also set `cli.fallback: none`. The schema refuses the entry otherwise, and `entry.test.ts` asserts the same thing, because the merger's read-back check cannot catch a config it has replaced wholesale.
6. A CLI that asks questions needs `cli.interactive`, and one whose environment flag takes all its pairs at once needs `cli.envStyle: grouped`. Both default to the shape most of these CLIs have; both are silent when wrong, in opposite directions — a cancelled install reported as success, and an API key dropped from an install reported as complete.
