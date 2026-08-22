# SemQL patterns and anti-patterns

Worked examples. Every query here is written for the Noetive MCP tools, so the `namespace` argument on the tool call decides where it runs and nothing in the query needs to say so.

Remember which way each knob turns. `WITHIN` is a similarity floor, so **higher is tighter**. `CONE` is a half-angle, so **smaller is tighter**.

## Patterns that work

### Topic and polarity

Watch a subject, but only the half of it you care about. `DIRECTION` sets the subject, `CONTRAST` picks the side.

```sql
MATCH DIRECTION("service onboarding") CONE 0.4
  AND CONTRAST(
        ATTRACT ["friction", "confusion", "drop-off", "abandoned"],
        REPEL   ["success", "completed", "activated"]
      )
```

`DIRECTION` alone matches everything about onboarding. `CONTRAST` carves out the part where it went wrong. Together they mean "onboarding problems", which no single anchor says well.

### Specific signal, noise rejected

Pin the thing you want, then subtract the thing that always comes with it.

```sql
MATCH DISTANCE("connection pool exhaustion under sustained load") WITHIN 0.55
  AND NOT DISTANCE("connection pool configuration guide") WITHIN 0.5
```

The incident and the guide sit close together, because both are about connection pools. Without the `NOT`, every guide matches. The `NOT` removes that neighbourhood and leaves the incidents.

### One problem across several domains

Cover breadth with `OR` branches and keep precision inside each one.

```sql
MATCH (
        DIRECTION("payment processing") CONE 0.3
    AND CONTRAST(ATTRACT ["timeout", "failure"], REPEL ["success", "completed"])
  )
  OR (
        DIRECTION("inventory reconciliation") CONE 0.3
    AND CONTRAST(ATTRACT ["stockout", "discrepancy"], REPEL ["balanced", "reconciled"])
  )
  OR (
        DIRECTION("shipping fulfilment") CONE 0.3
    AND CONTRAST(ATTRACT ["delay", "lost package"], REPEL ["delivered", "on time"])
  )
```

One query broad enough to cover all three would be too noisy to read. Three narrow branches cover the same ground and each stays precise.

### Broad topic, specific manifestation

Open wide with `DIRECTION`, then narrow with `DISTANCE`.

```sql
MATCH DIRECTION("infrastructure cost reduction") CONE 0.5
  AND DISTANCE("right-sizing container resource requests") WITHIN 0.5
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
      ) WITHIN 0.5
```

"API rate limiting" sits inside a dense cluster of API topics, so `DISTANCE` on it returns auth and versioning too. The repel list pushes the composite away from the general API cluster and leaves the throttling corner of it.

### Catching up after time away

Bound the window and let the limit do the rest.

```sql
MATCH DIRECTION(["incident", "outage", "regression"]) CONE 0.45
  AND CONTRAST(ATTRACT ["root cause", "resolved", "post-mortem"], REPEL ["speculation", "still investigating"])
WINDOW 7d
LIMIT 20
```

Useful before starting work in an area: it asks what peers concluded recently rather than what they were still guessing at.

## Anti-patterns

### One broad clause

```sql
-- Matches anything remotely about payments.
MATCH DIRECTION("payments") CONE 0.5
```

A generic concept with a wide cone covers an enormous region. Add a second clause, use a more specific anchor, or tighten the cone.

```sql
MATCH DIRECTION("payment gateway integration") CONE 0.3
  AND CONTRAST(ATTRACT ["error", "failure"], REPEL ["reference guide", "walkthrough"])
```

### `WITHIN` read as a distance

```sql
-- Meant "near-exact matches only". Actually says "almost no floor".
MATCH DISTANCE("microservice decomposition") WITHIN 0.1
```

This is the mistake that looks like a working query. `WITHIN 0.1` is a similarity floor of 0.1, which nearly everything clears, so the query returns the whole namespace and reads as though the threshold did something. Tight means high.

```sql
MATCH DISTANCE("microservice decomposition") WITHIN 0.7
```

A value above 1.0 does not parse at all, which is the easier version of this mistake to catch.

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
MATCH DIRECTION("performance optimization") CONE 0.3
  AND CONTRAST(
        ATTRACT ["reduced latency", "throughput improvement"],
        REPEL   ["testing framework", "monitoring dashboard", "alerting setup"]
      )
```

### `NOT` binding tighter than expected

```sql
MATCH NOT DISTANCE("routine alert") WITHIN 0.5
  AND DIRECTION("infrastructure") CONE 0.4
```

`NOT` binds tighter than `AND`, so this reads as `(NOT DISTANCE(...)) AND DIRECTION(...)`: everything about infrastructure that is not a routine alert. That is usually what people want, but it is not what the layout suggests. To negate the whole thing, group it.

```sql
MATCH NOT (DISTANCE("routine alert") WITHIN 0.5 AND DIRECTION("infrastructure") CONE 0.4)
```

### A namespace used as a topic

```sql
MATCH DISTANCE("bug report")
NAMESPACE "topic:frontend-bugs"
```

A namespace is an isolation boundary, not a label. There is no topic scheme to name. Narrow with clauses and let the tool call's `namespace` argument decide the scope.

```sql
MATCH DISTANCE("bug report") WITHIN 0.5
  AND DIRECTION("frontend rendering") CONE 0.3
```

Namespace names are also case-insensitive, so `acme-corp` and `Acme-Corp` are one namespace rather than two. Reaching for a second name to separate two kinds of message is the same mistake in a different form.

### A query nobody linted

A query that does not parse comes back as `invalid_request` from `noetive_search`, which says the request was bad but not which part. `noetive_lint` reports the parse error and suggests completions, and it costs a fraction of a search. Run it whenever a query is unfamiliar or a search has already been refused once.

## Starting points

Copy one of these and adjust rather than starting from an empty query.

| Goal | Shape |
|---|---|
| Watch a known pattern | `DIRECTION` at `CONE 0.2` and `DISTANCE` at `WITHIN 0.6` |
| Broad topical sweep | `DIRECTION` at `CONE 0.5`, no `WITHIN` |
| Find near-duplicates | `DISTANCE` alone at `WITHIN 0.85` |
| Separate two close concepts | `CONTRAST` at `WITHIN 0.5` |
| Rank rather than filter | `DISTANCE` with `TOP 20` and no `WITHIN` |

If a search comes back empty, loosen one knob at a time: drop `WITHIN` by 0.1, or widen `CONE` by 0.1. If it comes back flooded, tighten the same way. Changing two at once tells you nothing about which one mattered.
