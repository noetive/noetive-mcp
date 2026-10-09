---
name: semql
description: Write, review and debug SemQL queries for Noetive Semantik. Use whenever the user mentions SemQL, a semantic query, a semantic subscription, matching by meaning or embedding space, or wants to express interest in messages about a topic. Also use for "like X but not Y" queries, for DISTANCE, DIRECTION or CONTRAST clauses, and when a search returns invalid_request. Covers the text syntax and the JSON wire format.
---

SemQL describes a region of meaning rather than matching text. A query names where in embedding space you are interested, and every message that lands there matches however it happened to be worded.

It is the query language for `noetive_search`, `noetive_subscribe` and `noetive_lint`.

## Read the reference first

`references/grammar.md` is the language: the full grammar, every clause and its fields, and the exact ranges each numeric parameter accepts. Read it before writing or reviewing a query. Guessing at syntax costs more than reading it, and the mistakes it prevents are the ones that still parse.

`references/patterns.md` has worked queries, the mistakes that look like working queries, and starting values to copy.

## Which clause says what you mean

Three clauses, three different questions about the space. Picking the wrong one gives you a query that runs, returns results, and answers something else.

| What you mean | Clause | Shape |
|---|---|---|
| Similar to this | `DISTANCE` | A sphere around a point |
| About this subject | `DIRECTION` | A cone along an axis |
| Like this but not that | `CONTRAST` | Attract minus repel |
| Similar to this, within that subject | `DISTANCE` and `DIRECTION` | Sphere meeting cone |

`DISTANCE` cares about proximity, so topic and depth both count: a passing mention and a full analysis of the same thing sit at different similarities.

`DIRECTION` cares only about alignment and ignores magnitude, so a one-line note and a long write-up on the same subject both match. Reach for it when you want coverage of a topic regardless of how deeply anything went into it.

`CONTRAST` builds a composite from concepts you want and concepts you do not. Use it when the thing you are after has no good single anchor because its neighbours are too close.

## Thresholds and the cone

Semantik decides the bar, and you decide the angle.

**There is no threshold to set.** Semantik sets the relevance bar itself, per clause: a clause holds for a message only when the message is closer to it than general reference text is. `A AND B` matches only when each clause holds on its own; a strong match on one does not make up for the other. Clauses of one kind ORed together (`A OR B OR C`, all DISTANCE, say) share one bar measured on the group as a whole, which rises with the group's breadth. Clauses of different kinds in one OR (DISTANCE, DIRECTION, CONTRAST) each keep their own bar, so a CONTRAST branch still fires next to DISTANCE branches. **Semantik refuses `WITHIN`** on any DISTANCE or CONTRAST clause with `400 invalid_request`, and `noetive_lint` reports it as an error, even though `WITHIN` is part of the SemQL language elsewhere. To match less, make the anchor text more specific. To match more, OR in another anchor of similar specificity.

**`NOT` is supported only directly under an `AND` that also has a positive clause.** `A AND NOT DISTANCE("x")` excludes messages closer to x than general reference text is; the `NOT` only filters and does not raise the score. Anywhere else (`A OR NOT B`, `NOT A AND NOT B`, `A AND (B OR NOT C)`) Semantik refuses the query with `400 invalid_request`, because it would match nearly every message.

**`CONE` is a half-angle in radians. Smaller is tighter.** `CONE` can only tighten: a message must be inside the cone and above the bar, so a cone wider than the bar changes nothing. `CONE 0.8` is a narrow beam, `CONE 1.1` is a broad sweep, and the default when you omit it is 1.0. A cone below about 0.6 rad matches only close paraphrases of the anchor.

## Compose, do not widen

A single clause is almost always too broad. The power of SemQL is that each clause removes a different kind of noise, so two narrow clauses beat one loose one.

```sql
MATCH DIRECTION(["payment processing", "transaction"]) CONE 1.0
  AND CONTRAST(
        ATTRACT ["failure", "error", "timeout"],
        REPEL   ["success", "completed", "processed"]
      )
  AND DISTANCE("intermittent drops under load")
```

Thematically about payments, specifically the failure side of it, and close to one particular pattern. If a query returns too little, OR in another anchor of similar specificity rather than hunting for a looser setting.

## Scope comes from the tool call

Through these tools, the `namespace` argument on `noetive_search` or `noetive_subscribe` decides where the query runs. Set it there.

SemQL has a `NAMESPACE` clause, and `references/grammar.md` documents it, but it is not how you scope a call made through this server. Use clauses to narrow meaning and the tool argument to choose the namespace. If you do write the clause, it must name the same namespace as the tool argument and no more than one: the broker checks it and answers `400 invalid_request` on a disagreement, on several names, on a `NOT` exclusion, and on `ALL`.

A namespace is an isolation boundary, never a topic label. `NAMESPACE "topic:networking"` is not a thing; narrowing by subject is what `DIRECTION` and `CONTRAST` are for.

## Groups

To split one stream between several subscribers, give their subscribe calls the same `group` field (1 to 64 letters, digits, `-` or `_`). Each message then goes only to the member that scores highest against it, and match frames carry `lead`. Members must match on the same clause kinds (DISTANCE, DIRECTION, CONTRAST; clauses under `NOT` are ignored): a subscription whose kinds differ from the group's current members is refused with `400 invalid_request`. For DISTANCE the members must also use the same metric (mixing metrics is refused like mixing kinds). Write members in the same shape as well: an AND member scores the mean of its clauses and an OR member the best, so an AND member loses to an OR member it would otherwise tie. Groups belong to the account, so any of your keys can join your groups, and another account's identically named group never competes with yours. See the semantik-text skill.

## Text for people, JSON for machines

The two formats are equivalent and convert losslessly. Write text in explanations and discussion, JSON in code and config. When you produce a query for someone, give both unless they asked for one.

## Prefer text anchors

An anchor can be natural language or a raw vector. Use text: it is embedded with the model the namespace is pinned to, it stays readable, and it survives a model change. Reach for a raw vector only when you already have one, such as when you want the neighbours of a message you just read.

Some servers embed on the machine they run on and replace each text anchor with a vector before sending the query. Writing text anchors is still right there (that is what gets replaced), but each one then costs its dimensionality rather than a few characters, so such a server refuses a query carrying too many. The refusal names the ceiling for that namespace, and the higher the dimensionality the lower it is. Fewer, better anchors is the fix, not a raw vector.

## Writing a query

1. Name the core concept. What region of meaning is this about?
2. Name what should not match. That becomes a `REPEL` list or an `AND NOT`.
3. Choose a clause for each, using the table above.
4. Compose with `AND`, `OR` and `NOT`. Remember `NOT` binds tighter than `AND`, which binds tighter than `OR`, and that `NOT` is only valid directly under an `AND` with a positive clause.
5. Set the numbers, checking which direction each one runs.
6. Bound it. `WINDOW` for recency, `LIMIT` for volume.
7. Check it with `noetive_lint` before running it.

## Before you hand a query over

Give the text form, the JSON form, a sentence per clause saying why it is there, and what to change first (anchor wording, an OR'd anchor, `CONE`) if the results come back too wide or too narrow.

## When a search is refused

`invalid_request` from `noetive_search` means the query did not parse, and it does not say which part. Run `noetive_lint` on it: it reports the parse error and suggests completions. Lint first, then retry.

Empty results are a different problem. The query parsed and matched nothing, so change one thing at a time: reword an anchor to be less specific, OR in another anchor of similar specificity, or widen `CONE` by 0.1. Changing two at once tells you nothing about which one mattered.

## Reviewing someone else's query

The mistakes worth looking for, in the order they turn up:

- `WITHIN` written at all, which Semantik answers with `400 invalid_request`.
- A single clause, which is nearly always too broad.
- `CONTRAST` whose attract and repel lists overlap, so the composite cancels itself out.
- `NOT` at a tighter precedence than the author intended, or `NOT` anywhere but directly under an `AND` with a positive clause (refused with `400 invalid_request`).
- A namespace used to narrow meaning instead of a clause.
- A raw vector where a text anchor would read better and survive longer.

`references/patterns.md` has each of these written out with its correction.
