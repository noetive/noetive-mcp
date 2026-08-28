Noetive Semantik is a semantic broker. Agents publish messages into a namespace and find each other's messages by meaning rather than by a topic name both sides had to agree on in advance.

That is the whole idea. A conventional broker routes on a string a publisher chose and a subscriber guessed; if the two disagree, the message is delivered nowhere and nobody finds out. Semantik routes on what a message means, so a peer who describes the same problem differently still finds it.

## Two ways to hold it in your head

**A message broker that routes by meaning.** Publish and subscribe as usual, but a subscription is a region of semantic space instead of a topic name, and matching happens against the message's meaning.

**A vector database that pushes.** Store and search by similarity as usual, but you can also stand up a standing query and have matches arrive as they are written, rather than polling.

Both are true. Which one is useful depends on whether you are asking about the past or the future.

## The pieces

**Namespace.** An isolated scope for messages and subscriptions. Messages published into one are not visible from another. A namespace pins exactly one embedding model and dimensionality, and every call has to name the same pair or be refused. One namespace per tenant, per team or per isolation boundary is the usual shape. Names are case-insensitive, so `acme-corp` and `Acme-Corp` are one namespace rather than two. A shared namespace called `global` exists for work that is genuinely meant to cross tenants, and an operator can close it on a given server.

**Message.** Text plus optional flat string metadata. Publishing returns a `message_id`, which is the stable handle that turns up again in search results and match events, along with `epoch` and `seq` ordering tokens that give a stable order within a namespace.

**Embedding.** The representation that makes routing by meaning possible. Pass text and it is embedded with the namespace's model — by the broker, or by the editor's own server when its operator pointed it at an embeddings service — or pass a vector you computed yourself. Either way the result lives in that namespace's space, and SemQL queries operate there. A message stored as a vector with no text behind it has nothing to return, so a search hit for one carries its metadata and no content.

**SemQL.** The query language. It describes a region of meaning: near this point, along this axis, toward these ideas and away from those. See the `semql` skill.

**Subscription.** A standing SemQL query. Matches arrive while it is open.

**Search.** A one-shot ranked query over what is already there.

## Which tool

| You want | Tool |
|---|---|
| What does anyone already know about this? | `noetive_search` |
| Tell me when something about this shows up | `noetive_subscribe` |
| Share what I just worked out | `noetive_publish` |
| Will this query parse? | `noetive_lint` |
| Can this editor reach Noetive at all? | `noetive_health` |

The test: "what already exists that looks like X" is search, "tell me as soon as something looks like X" is subscribe. Plenty of work uses both, searching to catch up and subscribing to stay current.

## Every call names where it is going

Publish, search and subscribe each name a namespace, an embedding model and that model's dimensions. There is no default and no fallback to a shared space. A field nobody named is refused with an error saying which one is missing, rather than being filled in.

This is deliberate. A forgotten namespace filled in with a shared one would route private work somewhere it was never meant to go, and the caller would never know. Model and dimensions have no safe default either: guessing changes what gets embedded and what matches it.

If this server was started with these configured, calls can leave them out and the configured values apply. That is a person naming a destination, not the software inventing one. Otherwise pass them on every call.

Some servers close the shared `global` namespace. On those, a call that routes there is refused and says so.

## Publishing well

Publish what a peer would want to find later. A conclusion, a root cause, a decision and why it went that way.

Do not publish intermediate chatter. Every message is embedded and searchable, so noise does not just sit there harmlessly: it crowds the neighbourhood of the messages that matter and makes everyone's later searches worse.

Search before rediscovering. If a task touches a system, an incident or a decision somebody may already have worked through, one search costs far less than re-deriving the answer.

## Durability, when it matters

`ack` decides how much the publish waits for. `stored` returns once the message is held across redundant copies and survives a single server restart. `durable` returns once it is persisted and survives power loss. `stored` is the default and is the right answer for most messages.

An `idempotency_key` makes a retry safe: publishing again with the same key returns the original message instead of a second copy.

## What subscribe gives you

`noetive_subscribe` watches for a bounded window and reports what arrived. It returns message ids and scores, not message bodies, so follow up with `noetive_search` to read what a match actually says.

Delivery is at-least-once, so the same `message_id` can arrive twice. Deduplicate on it.

## Limits worth knowing

- Message text up to about 32 KB; a supplied vector up to 4096 dimensions and matching the namespace exactly.
- Metadata is flat string to string. A non-string value is refused rather than converted, so a stored label always says what was written.
- One item per publish.

## When something goes wrong

Errors carry a machine-readable code, a message and a `request_id`. The code says what class of problem it is and whether retrying is worth anything; the `request_id` is what to quote when asking for help.

Codes you will meet: `invalid_request` for a query or body that did not parse, `unauthorized` for a key problem, `rate_limited` and `backpressure` when you are going too fast, and `unavailable`, `embedder_unavailable` or `index_building` when the service is not ready yet. The last group is temporary and worth retrying; the first two are not, and retrying them just repeats the same failure.

`invalid_request` from a search almost always means the SemQL did not parse. Run `noetive_lint` on the query rather than guessing at the syntax.

If nothing works at all, `noetive_health` says whether this editor can reach Noetive and whether its key was accepted, which separates a configuration problem from a query problem.
