# SemQL v1.0 reference

The complete language. Read this before writing or reviewing a query.

Both formats are equivalent and convert losslessly. `noetive_search`, `noetive_subscribe` and `noetive_lint` accept either in their `query` argument.

## Grammar

```ebnf
query            = "MATCH" expression
                   [ "NAMESPACE" namespace_selector ]
                   [ "WINDOW" duration ]
                   [ "LIMIT" integer ] ;

expression       = term { "OR" term } ;
term             = factor { "AND" factor } ;
factor           = [ "NOT" ] atom ;
atom             = clause | "(" expression ")" ;

clause           = distance | direction | contrast ;

distance         = "DISTANCE" "(" anchor ")" [ "TOP" integer ] ;
direction        = "DIRECTION" "(" anchor_list ")" [ "CONE" number ] ;
contrast         = "CONTRAST" "(" "ATTRACT" anchor_list [ "," "REPEL" anchor_list ] ")" ;

(* Plural forms parse but the broker rejects them; see Namespace selector. *)
namespace_selector = namespace_ref { "," namespace_ref } | "ALL" | "GLOBAL" ;
namespace_ref      = [ "NOT" ] string_literal ;

anchor           = string_literal | vector_literal ;
anchor_list      = "[" anchor { "," anchor } "]" | anchor ;
vector_literal   = "[" number { "," number } "]" ;
duration         = integer ( "s" | "m" | "h" | "d" | "w" ) ;
```

Precedence, tightest first: `NOT`, `AND`, `OR`.

`NOT` is supported only directly under an `AND` that also has a positive clause. `A AND NOT DISTANCE("x")` excludes messages that are closer to x than general reference text is; the `NOT` only filters and does not raise the score. Anywhere else (`A OR NOT B`, `NOT A AND NOT B`, `A AND (B OR NOT C)`) the query is refused with `400 invalid_request`, because it would match nearly every message.

Reserved words are case-insensitive in the text syntax:
`MATCH AND OR NOT DISTANCE DIRECTION CONTRAST ATTRACT REPEL WITHIN TOP CONE WINDOW NAMESPACE ALL GLOBAL LIMIT`.

JSON keys are lowercase, always.

## No threshold, and WITHIN is refused

The SemQL language has a `WITHIN` clause on DISTANCE and CONTRAST (text and JSON `within`), but Semantik does not accept it: any query that sets it is answered `400 invalid_request` on search and subscribe, and `noetive_lint` reports it as an error. Do not write it from general SemQL knowledge.

Semantik sets the relevance bar itself, per clause. A clause holds for a message only when the message is closer to it than general reference text is. `A AND B` matches only when each clause holds on its own; a strong match on one does not make up for the other. Clauses of one kind ORed together (`A OR B OR C`, all DISTANCE, say) share one bar measured on the group as a whole, which rises with the group's breadth. Clauses of different kinds in one OR (DISTANCE, DIRECTION, CONTRAST) each keep their own bar, so a CONTRAST branch still fires next to DISTANCE branches. To match less, make the anchor text more specific. To match more, OR in another anchor of similar specificity.

## Clauses

### DISTANCE, a sphere around a point

Proximity to one anchor. Both topic and specificity matter: a passing mention and a deep analysis of the same subject sit at different similarities.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `anchor` | string or float[] | yes | | The centre point |
| `top_k` | integer | no | | Return the k nearest messages that also clear the bar (search only), so it can return fewer than k |
| `metric` | string | no | `cosine` | `cosine`, `euclidean` or `dot` |

```sql
DISTANCE("payment reconciliation failure")
```

```json
{ "distance": { "anchor": "payment reconciliation failure" } }
```

### DIRECTION, a cone around an axis

Thematic alignment, ignoring magnitude. A one-line mention and a long post-mortem point the same way and both match, which is what you want for topical coverage.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `toward` | string, string[] or float[] | yes | | The direction concept or concepts |
| `cone` | number | no | `1.0` | Half-angle in radians, 0 to π |

An array of concepts becomes the normalised mean of their embeddings.

`CONE`: **smaller is tighter**. 0.8 to 0.9 is a narrow beam; 1.0 to 1.1 is a broad sweep. A cone below about 0.6 matches only close paraphrases of the anchor. `CONE` can only tighten: a message must be inside the cone and above the bar, so a cone wider than the bar changes nothing.

```sql
DIRECTION(["customer frustration", "billing complaint"]) CONE 1.0
```

```json
{ "direction": { "toward": ["customer frustration", "billing complaint"], "cone": 1.0 } }
```

### CONTRAST, attract and repel

Vector arithmetic. It answers "in the direction of A, away from B", which carves out a slice no single anchor can describe.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `attract` | string[] | yes | | At least one concept |
| `repel` | string[] | no | | Concepts to subtract |

With both lists the composite is `normalize(mean(embed(attract)) - mean(embed(repel)))`. With `attract` alone it is `normalize(mean(embed(attract)))`.

```sql
CONTRAST(ATTRACT ["enterprise", "high-value account"], REPEL ["self-serve", "free tier"])
```

```json
{
  "contrast": {
    "attract": ["enterprise", "high-value account"],
    "repel": ["self-serve", "free tier"]
  }
}
```

## Expressions in JSON

```json
{ "and": [ <expression>, <expression> ] }
{ "or":  [ <expression>, <expression> ] }
{ "not": <expression> }
{ "distance": { } }
```

`and` and `or` take at least two children. `not` takes exactly one. A bare clause object is a valid expression on its own.

## Anchors

A string is natural language that the broker embeds with the namespace's own model. An array of numbers is a raw vector you already have.

```json
"payment reconciliation failure"
[0.182, -0.041, 0.389, 0.057]
```

A raw vector must have exactly the namespace's dimensionality.

## Namespace selector

```sql
NAMESPACE "acme-corp"
NAMESPACE GLOBAL
```

```json
{ "include": ["acme-corp"] }
```

| Field | Type | Default | Notes |
|---|---|---|---|
| `include` | string[] | `[]` | One name, matched literally: no globs |
| `global` | boolean | `false` | The shared namespace, on its own |

Namespace names are case-insensitive. `acme-corp`, `Acme-Corp` and `ACME-CORP` are one namespace, not three, and the same is true of `global`.

The clause asserts scope rather than choosing it, and the broker checks it: a clause naming a namespace other than the request's is `400 invalid_request`. Only a single namespace is honoured. The grammar above still admits several names, an `exclude` list and `ALL`: the parser accepts them, and the broker then rejects them with `400 invalid_request` rather than ignoring them. Selecting several namespaces from a query is not supported yet.

Read the note on scope in the skill body before reaching for this: through the Noetive MCP tools, the `namespace` argument on the tool call is what decides where a query runs.

## Window and limit

`WINDOW` bounds how far back a query looks. `LIMIT` caps how many results come back.

Text: `30s`, `15m`, `24h`, `7d`, `4w`.
JSON: ISO 8601, so `"PT30S"`, `"PT15M"`, `"PT24H"`, `"P7D"`, `"P28D"`.

```sql
MATCH DISTANCE("deployment rollback")
WINDOW 7d
LIMIT 20
```

```json
{
  "match": { "distance": { "anchor": "deployment rollback" } },
  "window": "P7D",
  "limit": 20
}
```

On `noetive_search` the `limit` tool argument overrides a `LIMIT` in the query.

## Full query object

```json
{
  "match": <expression>,
  "namespace": <namespace_selector>,
  "window": "<ISO 8601 duration>",
  "limit": <integer>
}
```

Only `match` is required.

## Not in v1.0

Trajectory, region, anomaly and resonance clauses. Named references (`$variable`, `MSG("id")`, `CENTROID("id")`). Composite anchors. Per-clause weights. A query using any of these does not parse.
