# SemQL patterns and anti-patterns

Worked examples. Every query here is written for the Noetive MCP tools, so the `namespace` argument on the tool call decides where it runs and nothing in the query needs to say so.

Semantik sets each query's relevance bar itself and refuses `WITHIN` (`400 invalid_request`), so there is no threshold to tune. `CONE` is a half-angle, so **smaller is tighter**. `CONE` can only tighten: a message must be inside the cone and above the bar, so a cone wider than the bar changes nothing. To match less, make an anchor more specific; to match more, OR in another anchor of similar specificity: anchors of one kind ORed together share one bar, so ORing in a much broader anchor raises the bar for the narrower ones and can drop their marginal matches.

## Patterns that work

### Topic and polarity

Watch a subject, but only the half of it you care about. `DIRECTION` sets the subject, `CONTRAST` picks the side.

```sql
MATCH DIRECTION("service onboarding") CONE 1.0
  AND CONTRAST(
        ATTRACT ["friction", "confusion", "drop-off", "abandoned"],
        REPEL   ["success", "completed", "activated"]
      )
```

`DIRECTION` alone matches everything about onboarding. `CONTRAST` carves out the part where it went wrong. Together they mean "onboarding problems", which no single anchor says well.

### Specific signal, noise rejected

Pin the thing you want, then subtract the thing that always comes with it.

```sql
MATCH DISTANCE("connection pool exhaustion under sustained load")
  AND NOT DISTANCE("connection pool configuration guide")
```

The incident and the guide sit close together, because both are about connection pools. Without the `NOT`, every guide matches. The `NOT` removes that neighbourhood and leaves the incidents.

### One problem across several domains

Cover breadth with `OR` branches and keep precision inside each one.

```sql
MATCH (
        DIRECTION("payment processing") CONE 0.9
    AND CONTRAST(ATTRACT ["timeout", "failure"], REPEL ["success", "completed"])
  )
  OR (
        DIRECTION("inventory reconciliation") CONE 0.9
    AND CONTRAST(ATTRACT ["stockout", "discrepancy"], REPEL ["balanced", "reconciled"])
  )
  OR (
        DIRECTION("shipping fulfilment") CONE 0.9
    AND CONTRAST(ATTRACT ["delay", "lost package"], REPEL ["delivered", "on time"])
  )
```

One query broad enough to cover all three would be too noisy to read. Three narrow branches cover the same ground and each stays precise.

### Broad topic, specific manifestation

Open wide with `DIRECTION`, then narrow with `DISTANCE`.

```sql
MATCH DIRECTION("infrastructure cost reduction") CONE 1.1
  AND DISTANCE("right-sizing container resource requests")
```

`DIRECTION` keeps recall high across the whole topic. `DISTANCE` pulls it down to the one technique. The pairing also stops the query drifting into container content that has nothing to do with cost.

### A concept defined by its edges

Some concepts have no good single anchor because their neighbours are too close. Describe the boundary instead.

```sql
MATCH CONTRAST(
        ATTRACT [
          "API rate limiting",
          "request throttling",
          "backpressure",
          "load shedding"
        ],
        REPEL [
          "API authentication",
          "API versioning",
          "API documentation"
        ]
      )
```

"API rate limiting" sits inside a dense cluster of API topics, so `DISTANCE` on it returns auth and versioning too. The repel list pushes the composite away from the general API cluster and leaves the throttling corner of it.

### Catching up after time away

Bound the window and let the limit do the rest.

```sql
MATCH DIRECTION(["incident", "outage", "regression"]) CONE 1.05
  AND CONTRAST(ATTRACT ["root cause", "resolved", "post-mortem"], REPEL ["speculation", "still investigating"])
WINDOW 7d
LIMIT 20
```

Useful before starting work in an area: it asks what peers concluded recently rather than what they were still guessing at.

## Anti-patterns

### One broad clause

```sql
-- Matches anything remotely about payments.
MATCH DIRECTION("payments") CONE 1.1
```

A generic concept with a wide cone covers an enormous region. Add a second clause, use a more specific anchor, or tighten the cone.

```sql
MATCH DIRECTION("payment gateway integration") CONE 0.9
  AND CONTRAST(ATTRACT ["error", "failure"], REPEL ["reference guide", "walkthrough"])
```

### `WITHIN` written from general SemQL knowledge

```sql
-- Semantik answers 400 invalid_request.
MATCH DISTANCE("microservice decomposition") WITHIN 0.7
```

`WITHIN` exists in the SemQL language but Semantik does not accept it. Drop it and put the specificity in the anchor.

```sql
MATCH DISTANCE("splitting a monolith into independently deployed microservices")
```

### Attract and repel that overlap

```sql
-- "performance" appears on both sides.
MATCH CONTRAST(
        ATTRACT ["performance optimization", "speed improvement"],
        REPEL   ["performance testing", "performance monitoring"]
      )
```

Concepts that sit close together partly cancel when subtracted, and the composite direction that survives is weak and unstable. Keep the two lists semantically far apart.

```sql
MATCH DIRECTION("performance optimization") CONE 0.9
  AND CONTRAST(
        ATTRACT ["reduced latency", "throughput improvement"],
        REPEL   ["testing framework", "monitoring dashboard", "alerting setup"]
      )
```

### `NOT` binding tighter than expected

`NOT` is supported only directly under an `AND` that also has a positive clause. `A AND NOT DISTANCE("x")` excludes messages that are closer to x than general reference text is; it only filters and does not raise the score. Anywhere else (`A OR NOT B`, `NOT A AND NOT B`, `A AND (B OR NOT C)`) the query is refused with `400 invalid_request`, because it would match nearly every message.

```sql
MATCH NOT DISTANCE("routine alert")
  AND DIRECTION("infrastructure") CONE 1.0
```

`NOT` binds tighter than `AND`, so this reads as `(NOT DISTANCE(...)) AND DIRECTION(...)`: everything about infrastructure that is not a routine alert. That is usually what people want, but it is not what the layout suggests. To exclude several things at once, negate a group, still directly under the `AND` with its positive clause.

```sql
MATCH DIRECTION("infrastructure") CONE 1.0
  AND NOT (DISTANCE("routine alert") OR DISTANCE("scheduled maintenance"))
```

### A namespace used as a topic

```sql
MATCH DISTANCE("bug report")
NAMESPACE "topic:frontend-bugs"
```

A namespace is an isolation boundary, not a label. There is no topic scheme to name. Narrow with clauses and let the tool call's `namespace` argument decide the scope.

```sql
MATCH DISTANCE("bug report")
  AND DIRECTION("frontend rendering") CONE 0.9
```

Namespace names are also case-insensitive, so `acme-corp` and `Acme-Corp` are one namespace rather than two. Reaching for a second name to separate two kinds of message is the same mistake in a different form.

### A query nobody linted

A query that does not parse comes back as `invalid_request` from `noetive_search`, which says the request was bad but not which part. `noetive_lint` reports the parse error and suggests completions, and it costs a fraction of a search. Run it whenever a query is unfamiliar or a search has already been refused once.

## Starting points

Copy one of these and adjust rather than starting from an empty query.

| Goal | Shape |
|---|---|
| Watch a known pattern | `DIRECTION` at `CONE 0.85` and a specific `DISTANCE` |
| Broad topical sweep | `DIRECTION` at `CONE 1.1` |
| Separate two close concepts | `CONTRAST` with sibling concepts in REPEL |
| Rank rather than filter | `DISTANCE` with `TOP 20` (search; returns the 20 nearest that also clear the bar, so possibly fewer) |

If a search comes back empty, change one thing at a time: make the anchor wording less specific, OR in another anchor of similar specificity, or widen `CONE` by 0.1. If it comes back flooded, go the other way: a more specific anchor, a CONTRAST or NOT, or a narrower `CONE`. Changing two at once tells you nothing about which one mattered.

## Groups

To split one stream between several subscribers so each message has one owner, give their `/v1/subscribe` calls the same `group` name (1 to 64 letters, digits, `-` or `_`). Members compete on score and a message goes only to the closest; matches carry `lead`. Members must match on the same clause kinds (DISTANCE, DIRECTION, CONTRAST; clauses under NOT ignored), or the subscription is refused with `400 invalid_request`. For DISTANCE the members must also use the same metric (mixing metrics is refused like mixing kinds). Write members in the same shape as well: an AND member scores the mean of its clauses and an OR member the best, so an AND member loses to an OR member it would otherwise tie. Groups belong to the account: all your keys share its group names, and another account's identically named group never competes with yours. See the semantik-text skill, "Roles that split a stream".
