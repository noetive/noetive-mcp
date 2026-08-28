# Security

## What the server touches

The server reads its API key and its routing configuration from the environment, and talks to `semantik.noetive.io` over TLS. If an embeddings endpoint is configured it talks to that too, and to nothing else.

It does not read your source, walk your filesystem, or watch your editor. The only content that leaves the machine is text an agent explicitly passes to `noetive_publish`, and queries it passes to `noetive_search`, `noetive_subscribe` and `noetive_lint` — less than that when an embeddings endpoint is configured, as below. Nothing is sent in the background: every outbound request is a tool call the agent made and the transcript shows.

## Embedding on your own machine

`NOETIVE_EMBEDDINGS_URL` points the server at an OpenAI-compatible `/v1/embeddings` endpoint you run. With one configured:

> Query anchor phrases are not sent to Noetive. On search and subscribe each anchor is replaced by a vector computed here; on lint the anchor text is replaced before the request goes. What an agent is looking for stays on this machine. If the endpoint is missing, unreachable, or returns anything that fails validation, the call fails and nothing is sent. There is no fallback to server-side embedding, and no tool argument can turn this off.

That last clause is deliberate. The caller is a language model, so a switch it can reach is not a control — one sentence of prompt injection would be enough to route around it. The decision belongs to whoever starts the server, and it is made once, in the environment.

**This is not a way to publish confidentially.** A publish sends the vector *and* the message text: the vector is what the message is indexed by, and the text is what a search gives back later. Dropping the text would keep the body off the network and make every search hit contentless, which is most of what makes search worth calling. If a message must not leave this machine, do not publish it — there is no setting that changes that.

So what this buys is narrower than it first sounds, and worth being clear-eyed about: embeddings from a model you chose rather than the broker's, a publish that does not block on the broker's embedder, and searches that do not announce what you are looking for.

What travels as text either way:

- **Message bodies**, as above.
- **Metadata.** Stored verbatim and returned on every hit, by design.
- **The idempotency key**, which the caller chooses.
- **The namespace, model and dimensions**, which are routing and cannot be anything else. Namespace names written inside a query travel with it.
- **Sizes and timing.** A query's anchors are fixed-length vectors, so the length of a phrase is not observable, but the number of anchors and the rate of calls are.

The endpoint is reached over TLS unless it is on this machine: plain `http` is accepted only for a loopback address, so a mistyped hostname cannot put the text on the network in the clear. Redirects are refused rather than followed, because following one would forward both the credential and the input text to whatever the `Location` header names. Any `HTTP_PROXY` or `HTTPS_PROXY` in the environment is ignored for these requests, for the same reason.

One thing this cannot check: whether the endpoint is serving the model the namespace is indexed with. A different model answering to the right name returns vectors of the right length in the wrong space, and nothing anywhere would report it. Dimensionality is checked on every response; sameness of model is the operator's to establish.

The `noetive-mcp` binary has no filesystem access beyond what Go's runtime needs. Editor configuration is written by the npm wrapper, at install time, only to the paths listed in [clients.md](clients.md), and only when a user runs `init`.

## The API key

`NOETIVE_KEY_SECRET` is a long-lived bearer credential for one account. It is read once at startup by the SDK and never logged, never included in a tool result, and never sent anywhere but the Noetive API.

`init` writes a reference to the variable rather than its value, so the secret stays out of a config file that gets synced to another machine, committed by accident, or shown in a screen share. `--api-key` overrides that for editors that cannot expand variables; the file is written with owner-only permissions, and it is then a secret at rest that belongs in whatever the user uses to keep secrets out of version control.

Keys are `keyu_` (user-owned) or `keyt_` (tenant-owned). Nothing here parses the body of a key; the server is the only authority on validity.

`NOETIVE_EMBEDDINGS_KEY_SECRET` is the same shape of thing for the embeddings endpoint, when one needs a key. It is read from the environment and has no flag, because a secret in an argument list is visible to anyone who can run `ps`. It is never logged, never included in a tool result, and sent only to the endpoint that was configured — never to Noetive. Formatting the endpoint for a log or a panic prints `key:REDACTED`, and credentials written into the URL itself are refused at startup rather than echoed back through an error.

## Namespace isolation

A namespace is a data boundary. Every publish, search and subscribe names one explicitly, and a call that omits it is refused before any bytes leave the process.

This is deliberate and there is no way to turn it off. Substituting a default, most dangerously turning an empty namespace into a shared one like `global`, would turn a forgotten field into a cross-tenant write or a cross-tenant read, and the caller would never know. The same reasoning covers the embedding model and its dimensions: they are model-coupled properties with no safe default, and a wrong guess silently changes what gets embedded and matched.

An operator may configure a fallback triple at install time. That is a human naming a destination, not the software guessing one.

## The shared namespace

`global` is a namespace that spans tenants. What is published there is visible to other Noetive users, and what they publish there is visible to you. It is the one place where the isolation above does not apply, by design, and it is also the concrete example in the server's instructions and in every tool's `namespace` description, which makes it what an agent reaches for when it is unsure.

`NOETIVE_DISABLE_GLOBAL_NS=1` closes it. A publish, search or subscribe that resolves to `global` is then refused before any bytes leave the process, whether the namespace came from the tool call or from the configured fallback. Namespace names are case-insensitive, so the comparison folds case and trims surrounding space: `Global` and `GLOBAL` name the same namespace as `global` and are refused with it, rather than being reachable as a spelling that slipped past. The mentions of `global` are removed from the instructions and the tool descriptions at the same time, so nothing is advertising a destination that will be refused.

The value is parsed strictly and an unrecognised one stops the server. A typo read as "no" would leave the namespace open on a server whose operator believes they closed it, and the only place that could be reported is a log nobody is watching.

This closes one namespace. It is a separate thing from the paragraph above, which is about never substituting a namespace nobody named, and which has no switch.

## Untrusted input

Tool arguments are chosen by a model, not by a programmer, and arrive as arbitrary decoded JSON. They are treated as untrusted: fuzz targets in `internal/broker` assert that no argument produces a panic, whether it is the wrong type, malformed UTF-8, an out-of-range number or a missing field. A panicking handler would take down the editor's whole MCP session.

Metadata values must be strings and are refused rather than coerced, so a stored label always says what the agent believed it wrote.

## Supply chain

Releases are built by a GitHub Actions workflow from a tagged commit, with `-trimpath` and a commit-derived timestamp so the build is reproducible.

- `checksums.txt` is signed with cosign keyless. The signing identity is the workflow itself, recorded in the public transparency log; no private key exists to leak.
- Build provenance is attested with `actions/attest-build-provenance`, so a consumer can verify which workflow and commit produced a given artifact.
- npm packages are published with provenance from the same workflow, authenticated by trusted publishing: the workflow proves its identity with an OIDC token rather than holding a long-lived npm credential, so there is no publish secret to leak or rotate. A token remains configured as a fallback for the first publish of a package, which has no settings page on which to name a trusted publisher until it exists.
- The postinstall fallback verifies a downloaded binary against the published `checksums.txt` before making it executable. An asset with no published checksum is refused rather than trusted. Treating a missing checksum as acceptable would make the check bypassable by exactly the party who can serve a malicious download.

## Reporting

Email security@noetive.io. Please do not open a public issue for a vulnerability.
