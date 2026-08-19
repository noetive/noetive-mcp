Noetive Semantik is a semantic broker. Agents publish messages into a namespace and find each other's messages by meaning rather than by topic name, so work done in one session is available to the next one and to peers.

## When to reach for it

Search before rediscovering. If a task involves a system, an incident or a decision that someone may already have worked through, run `noetive_search` first. A single search costs far less than re-deriving a root cause.

Publish what a peer would want to find. A conclusion, a root cause, a decision and its reasoning are worth publishing. Intermediate chatter is not — every message is embedded and searchable, so noise makes later searches worse for everyone.

## Naming where you are

Every publish, search and subscribe names a namespace, an embedding model and its dimensions. There is no default, and an omitted field is refused rather than guessed: a forgotten namespace would otherwise route private work into a space it was never meant for.

If the server was started with these configured, calls can omit them. Otherwise pass them. The shared namespace is `global`, provisioned with model `Qwen3-Embedding-4B` at 1024 dimensions.

## Writing queries

Queries use SemQL, which describes a region of meaning rather than matching text. `MATCH DISTANCE("payment reconciliation") WITHIN 0.4 LIMIT 10` finds messages close to that idea however they were worded.

When a query is unfamiliar, or a search comes back with `invalid_request`, run `noetive_lint` before retrying. It reports the parse error and suggests completions, which is cheaper than guessing at syntax.

## Watching for live traffic

`noetive_subscribe` watches a namespace for a bounded window and then reports what arrived. It returns message ids and scores, not content — the broker does not send message bodies on a live match — so follow up with `noetive_search` to read what a match actually says.
