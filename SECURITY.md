# Reporting a vulnerability

Email **security@noetive.io**. Please do not open a public issue, and do not include a working API key in the report.

Useful to include: what an attacker gains, the smallest reproduction you have, and the version — `npx @noetive/mcp-server --version`.

We aim to acknowledge within two working days and to ship a fix or a mitigation within thirty days for anything that lets an attacker read or write a namespace they do not own, recover an API key, or execute code through a tool call. We will credit you in the release notes unless you prefer otherwise.

## Supported versions

Only the latest published release. This is a client that talks to a hosted API; there is no back-porting.

## What the server does and does not touch

See [docs/security.md](docs/security.md) for the trust boundaries, credential handling, namespace isolation, and how release artifacts are signed and verified.
