# @noetive/mcp-server

Connect your AI editor to [Noetive Semantik](https://noetive.io) over the Model Context Protocol. Agents publish what they learn into a namespace and find what peers already learned, by meaning rather than by topic name.

<!-- mcp-name: io.noetive/mcp-server -->

## Install

Paste this into the coding agent you want connected:

```text
Install the Noetive MCP server into the editor you are running in.

1. From the project folder you have open, run: npx -y @noetive/mcp-server init --no-input --client <id>
   where <id> is the editor you are running in: cursor, claude-code, codex, copilot, antigravity, kiro or hermes.
2. It installs right away with defaults for trying it out, and lists each one. Tell me what it chose. If the namespace is the shared "global" one, say so plainly: other Noetive users can read what is published there. Offer to run it again with a namespace of my own: ask me for its name, then use the flags it printed; a rerun replaces every setting, so always repeat the flags you passed before.
3. Never ask me for my API key in this chat and never print it. If it says the config references ${NOETIVE_KEY_SECRET}, do not pass either --api-key flag: tell me to export the variable where the editor can see it. If it says no key was written, run the same command with the same flags again, adding --api-key-from-env, which reads the key from NOETIVE_KEY_SECRET and writes it into the config file without it appearing in the command. If it says that variable is not set, stop and tell me to export it and restart you.
4. If it says it needs a terminal or the editor's own CLI (hermes always does), show me its message and what it tells me to do.
5. Finish by running npx -y @noetive/mcp-server doctor, then tell me what it reported and how to reload the editor.
```

The first install starts on the shared `global` namespace so the first call works, and the agent tells you how to move to your own. Your key never passes through the chat.

### Or run it yourself

```bash
npx @noetive/mcp-server init --client cursor
npx @noetive/mcp-server init --client claude-code
npx @noetive/mcp-server init --client codex
npx @noetive/mcp-server init --client copilot
npx @noetive/mcp-server init --client antigravity
npx @noetive/mcp-server init --client kiro
npx @noetive/mcp-server init --client hermes
```

Run it with no `--client` and it configures the editor it finds. Run it in a terminal and it asks for what it needs: your API key, the namespace to route to, its model and dimensions, whether to close the shared namespace, and which skills to install. Every answer has a flag, and anything you pass is not asked about again. With no terminal to ask in, or with `--no-input` or `--yes`, it asks nothing and uses defaults for trying it out: the shared `global` namespace with `Qwen3-Embedding-4B` at 1024 dimensions, and every skill. It lists each default with the flag that changes it, and warns that `global` is shared. A rerun replaces every setting, so repeat the flags you passed before.

One-click buttons for Cursor, VS Code, Kiro and Hermes are on [noetive.io/mcp](https://noetive.io/mcp). The Hermes button needs the Hermes desktop app; the Hermes command needs the `hermes` CLI and a terminal to answer, since Hermes asks which tools to enable.

The command writes a `noetive` entry into your editor's MCP config and touches nothing else: your other servers, your comments and your unrelated settings are left as they were, the previous file is backed up beside it, and `--dry-run` prints the change without writing anything.

Registry-aware clients can install by name instead: `io.noetive/mcp-server`.

## Authenticate

The server needs an API key from the [Noetive dashboard](https://noetive.io/dashboard), read from `NOETIVE_KEY_SECRET`.

By default `init` writes a reference to that variable rather than the key itself, so the secret stays out of a config file that gets synced or committed:

```bash
export NOETIVE_KEY_SECRET=keya_...
```

Kiro and Codex do not expand variables in their configs, so write the key in there, read from that variable so it never appears in the command:

```bash
npx @noetive/mcp-server init --client kiro --api-key-from-env
```

## Check it worked

```bash
npx @noetive/mcp-server doctor
```

It reports four independent things: the binary, the key, each editor's config, and which scope each was found in. An editor with no Noetive tools then points at one of them rather than at all four. To check Semantik itself, ask your agent to call `noetive_health`.

## Tools

| Tool | What it does |
|---|---|
| `noetive_publish` | Publish a message so peers can find it by meaning |
| `noetive_search` | Search a namespace with SemQL and read the matching messages |
| `noetive_subscribe` | Watch a namespace for live matches for a bounded window |
| `noetive_lint` | Check a SemQL query before running it |
| `noetive_health` | Check that the editor can reach Noetive and its key is accepted |
| `bud_wait` | Wait for something to happen in an agent mailbox |
| `bud_read` | Read a mailbox, folder, message, part, thread or the workspace addressbook by its id |
| `bud_put` | Mark a message read or unread, or replace its labels |
| `bud_send` | Send or reply to email from the agent's own address |

The `bud_*` tools give your agent its own email address with Noetive Bud, on the same key. With [riffle](https://github.com/noetive/riffle) installed, the server also offers `browser_run` and `browser_view`, which work without a key.

## Namespaces are named, never guessed

Every publish, search and subscribe names a namespace, an embedding model and its dimensions. The server has no default and no fallback to a shared space: an omitted field is refused before the request is sent, because a forgotten namespace would otherwise route private work somewhere it was never meant to go. A bare `init` writes the shared `global` into the config for trying it out, and says so.

Set them once at install time, or let your agent pass them per call:

```bash
npx @noetive/mcp-server init --client cursor \
  --namespace acme-platform --model Qwen3-Embedding-4B --dimensions 1024
```

## Closing the shared namespace

`global` spans tenants: what an agent publishes there, other Noetive users can find. It is also the example value in the server's own instructions, which makes it what an agent reaches for when it is unsure.

`--disable-global-ns` closes it. A call that routes to `global` is then refused before the request is sent, and the mentions of it disappear from the instructions and tool descriptions so the agent is not offered something it will be refused for taking. `--allow-global-ns` records the opposite decision explicitly. `init` closes it by default once you name a namespace of your own, and leaves it open while `global` is the one you route to.

## Skills

`init` installs skills that teach your agent how to write a SemQL query, when to search rather than subscribe, how to read and send mail with Bud, and how to diagnose the install. Claude Code and Hermes read skills from a directory, so they are written there and `remove` takes them away again; the other editors read a different instruction format and are told so rather than given files they would ignore.

`--skills none` skips them, `--skills all` takes everything, and a comma-separated list picks some.

## Other commands

```bash
npx @noetive/mcp-server           # serve over stdio; this is what editors run
npx @noetive/mcp-server list      # every detected editor and whether it is configured
npx @noetive/mcp-server doctor    # diagnose an installation
npx @noetive/mcp-server remove --client cursor   # the noetive entry and the skills it installed
```

## How it installs

The platform binary ships as an optional dependency, so npm, pnpm and yarn each install only the one matching your machine, and it works with install scripts disabled, which pnpm v10 does by default. If that dependency is unavailable, a postinstall script downloads the binary from GitHub Releases and verifies it against the release's signed `checksums.txt` before making it executable.

Source, issues and security policy: [github.com/noetive/noetive-mcp](https://github.com/noetive/noetive-mcp).
