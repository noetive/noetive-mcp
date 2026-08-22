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

## Which way the numbers run

Getting these backwards is the most common way to write a query that looks right and does nothing.

**`WITHIN` is a minimum cosine similarity, from 0.0 to 1.0. Higher is tighter.** `WITHIN 0.8` is near-duplicates. `WITHIN 0.3` is the general vicinity. `WITHIN 0` is no floor at all. It is not a distance, and a value above 1.0 does not parse.

**`CONE` is a half-angle in radians. Smaller is tighter.** `CONE 0.15` is a narrow beam, `CONE 0.6` is a broad sweep, and the default when you omit it is 0.3.

## Compose, do not widen

A single clause is almost always too broad. The power of SemQL is that each clause removes a different kind of noise, so two narrow clauses beat one loose one.

```sql
MATCH DIRECTION(["payment processing", "transaction"]) CONE 0.4
  AND CONTRAST(
        ATTRACT ["failure", "error", "timeout"],
        REPEL   ["success", "completed", "processed"]
      )
  AND DISTANCE("intermittent drops under load") WITHIN 0.5
```

Thematically about payments, specifically the failure side of it, and close to one particular pattern. If you find yourself loosening a threshold to get results, try adding a clause instead.

## Scope comes from the tool call

Through these tools, the `namespace` argument on `noetive_search` or `noetive_subscribe` decides where the query runs. Set it there.

SemQL has a `NAMESPACE` clause, and `references/grammar.md` documents it, but it is not how you scope a call made through this server. Use clauses to narrow meaning and the tool argument to choose the namespace.

A namespace is an isolation boundary, never a topic label. `NAMESPACE "topic:networking"` is not a thing; narrowing by subject is what `DIRECTION` and `CONTRAST` are for.

## Text for people, JSON for machines

The two formats are equivalent and convert losslessly. Write text in explanations and discussion, JSON in code and config. When you produce a query for someone, give both unless they asked for one.

## Prefer text anchors

An anchor can be natural language or a raw vector. Use text: the broker embeds it with the model the namespace is pinned to, it stays readable, and it survives a model change. Reach for a raw vector only when you already have one, such as when you want the neighbours of a message you just read.

## Writing a query

1. Name the core concept. What region of meaning is this about?
2. Name what should not match. That becomes a `REPEL` list or a `NOT`.
3. Choose a clause for each, using the table above.
4. Compose with `AND`, `OR` and `NOT`. Remember `NOT` binds tighter than `AND`, which binds tighter than `OR`.
5. Set the numbers, checking which direction each one runs.
6. Bound it. `WINDOW` for recency, `LIMIT` for volume.
7. Check it with `noetive_lint` before running it.

## Before you hand a query over

Give the text form, the JSON form, a sentence per clause saying why it is there, and which numbers to adjust first if the results come back too wide or too narrow.

## When a search is refused

`invalid_request` from `noetive_search` means the query did not parse, and it does not say which part. Run `noetive_lint` on it: it reports the parse error and suggests completions. Lint first, then retry.

Empty results are a different problem. The query parsed and matched nothing, so loosen one knob at a time: drop `WITHIN` by 0.1, or widen `CONE` by 0.1. Changing two at once tells you nothing about which one mattered.

## Reviewing someone else's query

The mistakes worth looking for, in the order they turn up:

- `WITHIN` used as a distance, so a query meant to be tight is wide open.
- A single clause, which is nearly always too broad.
- `CONTRAST` whose attract and repel lists overlap, so the composite cancels itself out.
- `NOT` at a tighter precedence than the author intended.
- A namespace used to narrow meaning instead of a clause.
- A raw vector where a text anchor would read better and survive longer.

`references/patterns.md` has each of these written out with its correction.
