# Security

## What the server touches

The server reads its API key and its routing configuration from the environment, and talks to `semantik.noetive.io` over TLS. That is all.

It does not read your source, walk your filesystem, or watch your editor. The only content that leaves the machine is text an agent explicitly passes to `noetive_publish`, and queries it passes to `noetive_search`, `noetive_subscribe` and `noetive_lint`. Nothing is sent in the background: every outbound request is a tool call the agent made and the transcript shows.

The `noetive-mcp` binary has no filesystem access beyond what Go's runtime needs. Editor configuration is written by the npm wrapper, at install time, only to the paths listed in [clients.md](clients.md), and only when a user runs `init`.

## The API key

`NOETIVE_KEY_SECRET` is a long-lived bearer credential for one account. It is read once at startup by the SDK and never logged, never included in a tool result, and never sent anywhere but the Noetive API.

`init` writes a reference to the variable rather than its value, so the secret stays out of a config file that gets synced to another machine, committed by accident, or shown in a screen share. `--api-key` overrides that for editors that cannot expand variables; the file is written with owner-only permissions, and it is then a secret at rest that belongs in whatever the user uses to keep secrets out of version control.

Keys are `keyu_` (user-owned) or `keyt_` (tenant-owned). Nothing here parses the body of a key — the server is the only authority on validity.

## Namespace isolation

A namespace is a data boundary. Every publish, search and subscribe names one explicitly, and a call that omits it is refused before any bytes leave the process.

This is deliberate and there is no way to turn it off. Substituting a default — most dangerously turning an empty namespace into a shared one like `global` — would turn a forgotten field into a cross-tenant write or a cross-tenant read, and the caller would never know. The same reasoning covers the embedding model and its dimensions: they are model-coupled properties with no safe default, and a wrong guess silently changes what gets embedded and matched.

An operator may configure a fallback triple at install time. That is a human naming a destination, not the software guessing one.

## Untrusted input

Tool arguments are chosen by a model, not by a programmer, and arrive as arbitrary decoded JSON. They are treated as untrusted: fuzz targets in `internal/broker` assert that no argument — wrong type, malformed UTF-8, out-of-range number, missing field — produces a panic. A panicking handler would take down the editor's whole MCP session.

Metadata values must be strings and are refused rather than coerced, so a stored label always says what the agent believed it wrote.

## Supply chain

Releases are built by a GitHub Actions workflow from a tagged commit, with `-trimpath` and a commit-derived timestamp so the build is reproducible.

- `checksums.txt` is signed with cosign keyless. The signing identity is the workflow itself, recorded in the public transparency log; no private key exists to leak.
- Build provenance is attested with `actions/attest-build-provenance`, so a consumer can verify which workflow and commit produced a given artifact.
- npm packages are published with provenance from the same workflow.
- The postinstall fallback verifies a downloaded binary against the published `checksums.txt` before making it executable. An asset with no published checksum is refused rather than trusted — treating a missing checksum as acceptable would make the check bypassable by exactly the party who can serve a malicious download.

## Reporting

Email security@noetive.io. Please do not open a public issue for a vulnerability.
