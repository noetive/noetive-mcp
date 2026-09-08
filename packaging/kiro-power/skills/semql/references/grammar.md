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

distance         = "DISTANCE" "(" anchor ")" [ "WITHIN" number | "TOP" integer ] ;
direction        = "DIRECTION" "(" anchor_list ")" [ "CONE" number ] ;
contrast         = "CONTRAST" "(" "ATTRACT" anchor_list [ "," "REPEL" anchor_list ] ")" [ "WITHIN" number ] ;

(* Plural forms parse but the broker rejects them — see Namespace selector. *)
namespace_selector = namespace_ref { "," namespace_ref } | "ALL" | "GLOBAL" ;
namespace_ref      = [ "NOT" ] string_literal ;

anchor           = string_literal | vector_literal ;
anchor_list      = "[" anchor { "," anchor } "]" | anchor ;
vector_literal   = "[" number { "," number } "]" ;
duration         = integer ( "s" | "m" | "h" | "d" | "w" ) ;
```

Precedence, tightest first: `NOT`, `AND`, `OR`.

Reserved words are case-insensitive in the text syntax:
`MATCH AND OR NOT DISTANCE DIRECTION CONTRAST ATTRACT REPEL WITHIN TOP CONE WINDOW NAMESPACE ALL GLOBAL LIMIT`.

JSON keys are lowercase, always.

## WITHIN is a similarity floor, not a distance

This is the single most common way to get a query wrong, so it is worth stating on its own.

`WITHIN` is the **minimum cosine similarity** a message must reach to pass the clause. It runs from 0.0 to 1.0, and **higher is tighter**:

| `WITHIN` | Meaning |
|---|---|
| `0.0` | No floor. Every message survives the clause and it acts as a scoring signal only. |
| `0.3` | Loose. Anything in the general vicinity of the anchor. |
| `0.5` | Moderate. Recognisably about the same thing. |
| `0.7` | Tight. Close paraphrases and restatements. |
| `0.9` | Near-duplicate. |
| `1.0` | Exact match only. |

A value outside 0.0 to 1.0 is rejected. If you find yourself writing `WITHIN 1.5` you are thinking in distances, where lower means closer. SemQL is the other way round.

## Clauses

### DISTANCE, a sphere around a point

Proximity to one anchor. Both topic and specificity matter: a passing mention and a deep analysis of the same subject sit at different similarities.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `anchor` | string or float[] | yes | | The centre point |
| `within` | number | no | | Minimum cosine similarity, 0.0 to 1.0 |
| `top_k` | integer | no | | Return the k nearest instead of applying a floor |
| `metric` | string | no | `cosine` | `cosine`, `euclidean` or `dot` |

Set `within` or `top_k`, not both. Setting neither scores without thresholding.

```sql
DISTANCE("payment reconciliation failure") WITHIN 0.55
```

```json
{ "distance": { "anchor": "payment reconciliation failure", "within": 0.55 } }
```

### DIRECTION, a cone around an axis

Thematic alignment, ignoring magnitude. A one-line mention and a long post-mortem point the same way and both match, which is what you want for topical coverage.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `toward` | string, string[] or float[] | yes | | The direction concept or concepts |
| `cone` | number | no | `0.3` | Half-angle in radians, 0 to π |

An array of concepts becomes the normalised mean of their embeddings.

`CONE` runs the opposite way to `WITHIN`: **smaller is tighter**. 0.1 to 0.2 is a narrow, high-precision beam; 0.5 to 0.8 is a broad sweep.

```sql
DIRECTION(["customer frustration", "billing complaint"]) CONE 0.4
```

```json
{ "direction": { "toward": ["customer frustration", "billing complaint"], "cone": 0.4 } }
```

### CONTRAST, attract and repel

Vector arithmetic. It answers "in the direction of A, away from B", which carves out a slice no single anchor can describe.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `attract` | string[] | yes | | At least one concept |
| `repel` | string[] | no | | Concepts to subtract |
| `within` | number | no | | Minimum cosine similarity to the composite, 0.0 to 1.0 |

With both lists the composite is `normalize(mean(embed(attract)) - mean(embed(repel)))`. With `attract` alone it is `normalize(mean(embed(attract)))`.

```sql
CONTRAST(ATTRACT ["enterprise", "high-value account"], REPEL ["self-serve", "free tier"]) WITHIN 0.45
```

```json
{
  "contrast": {
    "attract": ["enterprise", "high-value account"],
    "repel": ["self-serve", "free tier"],
    "within": 0.45
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
| `include` | string[] | `[]` | One name, matched literally — no globs |
| `global` | boolean | `false` | The shared namespace, on its own |

Namespace names are case-insensitive. `acme-corp`, `Acme-Corp` and `ACME-CORP` are one namespace, not three, and the same is true of `global`.

The clause asserts scope rather than choosing it, and the broker checks it: a clause naming a namespace other than the request's is `400 invalid_request`. Only a single namespace is honoured. The grammar above still admits several names, an `exclude` list and `ALL` — the parser accepts them, and the broker then rejects them with `400 invalid_request` rather than ignoring them. Selecting several namespaces from a query is not supported yet.

Read the note on scope in the skill body before reaching for this: through the Noetive MCP tools, the `namespace` argument on the tool call is what decides where a query runs.

## Window and limit

`WINDOW` bounds how far back a query looks. `LIMIT` caps how many results come back.

Text: `30s`, `15m`, `24h`, `7d`, `4w`.
JSON: ISO 8601, so `"PT30S"`, `"PT15M"`, `"PT24H"`, `"P7D"`, `"P28D"`.

```sql
MATCH DISTANCE("deployment rollback") WITHIN 0.5
WINDOW 7d
LIMIT 20
```

```json
{
  "match": { "distance": { "anchor": "deployment rollback", "within": 0.5 } },
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
