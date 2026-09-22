# Anchor catalog

Ready phrases and templates. Every phrase here is a draft until it has passed validation on the namespace's pinned model (see `validation.md`). When a phrase is changed on one side it must change on the other; publisher prefix and subscriber ATTRACT are the same bytes.

Every `WITHIN` below is a cosine similarity floor, so higher is tighter, and every number is a starting point to be replaced by a measured one.

## Intent phrases for agent-to-agent protocols

These serve a ten-move protocol where each message starts with a move word. The publisher renders `text` as `intent phrase + topic words + point sentence`. The phrases were chosen to have distinct content nouns (question, fact, decision, request, finished work, result, objection, stopped, correction, commitment) so that they separate on topic geometry even though the moves themselves are speech acts.

| Move | Canonical phrase | Nearest siblings to REPEL |
|---|---|---|
| ask | a question that blocks work until answered | object, block |
| tell | a statement of fact with its source | fix, decide |
| decide | a design decision with alternatives rejected | object, tell |
| do | a request for work with a completion criterion | done, take |
| done | finished work reported for verification | do, verdict |
| verdict | a verification result, pass or fail, on finished work | done, object |
| object | an objection that a decision or request is wrong or infeasible | decide, ask |
| block | work stopped, waiting on a missing input | take, ask |
| fix | a correction superseding an earlier message | tell |
| take | a commitment to carry out an open request | do, block |

Unknown or coined move words are rendered as themselves ("warn", "offer", "bid"); a bare English word lands near its meaning and degrades gracefully.

Expected confusion pairs, in order of risk: do/done, ask/object, tell/fix, decide/object, block/take. If validation shows one of these pairs inseparable at the intended WITHIN, lengthen both phrases with a distinguishing noun before touching the threshold. For example "finished work reported for verification, tests listed" against "a request for work with a completion criterion, not yet started".

## Role inboxes

Each role subscribes to the moves it must act on, scoped by the topics it owns. The topic clause is the part that changes per deployment.

Verification, everything reported done:

```sql
MATCH CONTRAST(
        ATTRACT ["finished work reported for verification"],
        REPEL   ["a request for work with a completion criterion",
                 "a verification result, pass or fail, on finished work"]
      ) WITHIN 0.5
  AND DIRECTION(["<topic words the role owns>"]) CONE 0.5
```

Implementation, work requests plus verdicts on its own deliveries:

```sql
MATCH (
        CONTRAST(
          ATTRACT ["a request for work with a completion criterion"],
          REPEL   ["finished work reported for verification",
                   "a commitment to carry out an open request"]
        ) WITHIN 0.5
     OR CONTRAST(
          ATTRACT ["a verification result, pass or fail, on finished work"],
          REPEL   ["finished work reported for verification",
                   "an objection that a decision or request is wrong or infeasible"]
        ) WITHIN 0.5
  )
  AND DIRECTION(["<topic words the role owns>"]) CONE 0.5
```

Design, objections and blocks:

```sql
MATCH CONTRAST(
        ATTRACT ["an objection that a decision or request is wrong or infeasible",
                 "work stopped, waiting on a missing input"],
        REPEL   ["finished work reported for verification",
                 "a statement of fact with its source"]
      ) WITHIN 0.5
  AND DIRECTION(["<topic words the role owns>"]) CONE 0.5
```

Research, questions about the world rather than about intent:

```sql
MATCH CONTRAST(
        ATTRACT ["a question that blocks work until answered"],
        REPEL   ["an objection that a decision or request is wrong or infeasible",
                 "work stopped, waiting on a missing input"]
      ) WITHIN 0.5
  AND CONTRAST(
        ATTRACT ["documented behaviour of an external service or library", "published specification or reference"],
        REPEL   ["choice between design alternatives", "priority or scope of our own work"]
      )
```

The second CONTRAST is what separates a research question from a design question; the intent phrase alone cannot, because both are questions.

Open work anyone may take, unaddressed requests:

```sql
MATCH CONTRAST(
        ATTRACT ["a request for work with a completion criterion", "work stopped, waiting on a missing input"],
        REPEL   ["a commitment to carry out an open request", "finished work reported for verification"]
      ) WITHIN 0.5
```

Retell suppression, anchored on the message just published:

```sql
MATCH DISTANCE("<the text just published>") WITHIN 0.85
WINDOW 24h
```

The text, not a vector. A publisher always has the text it just sent; it has a
vector only when embedding runs on its own machine, because Semantik does not
return one. Substitute `DISTANCE([<vector>])` when you do hold it.

## Topic template

Pick two to five noun phrases publishers actually use, one adjacent topic to repel, and one exemplar sentence if a specific pattern matters.

```sql
MATCH DIRECTION(["<noun phrase 1>", "<noun phrase 2>", "<noun phrase 3>"]) CONE 0.4
  AND CONTRAST(
        ATTRACT ["<what this topic is, in message words>"],
        REPEL   ["<the adjacent topic that shares the stream>"]
      )
```

Worked instance, booking write semantics in a stream that also carries pricing and account chatter:

```sql
MATCH DIRECTION(["reservation double-booking", "idempotency key on submit", "retry-safe form POST"]) CONE 0.4
  AND CONTRAST(
        ATTRACT ["booking write semantics", "duplicate reservation after retry"],
        REPEL   ["seat pricing and discount codes", "login and session expiry"]
      )
```

Publish text that lands in it, written by a research agent:

```
a statement of fact with its source. booking retry dupe. the HTTP idempotency-key draft defines the header as a client-generated key the server dedupes on.
```

with metadata `{"at": "datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/", "fetched": "2026-09-14", "source": "research"}`.

## Category template

One parent DIRECTION shared by all bins. Per bin: three to six exemplars attract, the confusable siblings' exemplars repel. The residual bin is NOT of the union.

```sql
-- bin: <name>
MATCH DIRECTION(["<parent topic phrase>", "<parent topic phrase>"]) CONE 0.5
  AND CONTRAST(
        ATTRACT ["<exemplar 1>", "<exemplar 2>", "<exemplar 3>"],
        REPEL   ["<sibling A exemplar>", "<sibling B exemplar>"]
      ) WITHIN 0.5
```

Worked instance, support triage into four bins:

```sql
-- billing
MATCH DIRECTION(["customer support email", "account and service request"]) CONE 0.5
  AND CONTRAST(
        ATTRACT ["charged twice this month", "refund for the unused period", "invoice amount is wrong", "card declined but subscription still active"],
        REPEL   ["cannot log in after password reset", "page takes thirty seconds to load", "please add a dark mode"]
      ) WITHIN 0.5

-- access
MATCH DIRECTION(["customer support email", "account and service request"]) CONE 0.5
  AND CONTRAST(
        ATTRACT ["cannot log in after password reset", "two-factor code never arrives", "account locked after failed attempts", "invite link expired"],
        REPEL   ["charged twice this month", "page takes thirty seconds to load", "please add a dark mode"]
      ) WITHIN 0.5

-- performance
MATCH DIRECTION(["customer support email", "account and service request"]) CONE 0.5
  AND CONTRAST(
        ATTRACT ["page takes thirty seconds to load", "export times out on large reports", "search results arrive late", "app freezes when switching workspaces"],
        REPEL   ["cannot log in after password reset", "charged twice this month", "please add a dark mode"]
      ) WITHIN 0.5

-- feature request
MATCH DIRECTION(["customer support email", "account and service request"]) CONE 0.5
  AND CONTRAST(
        ATTRACT ["please add a dark mode", "would like to export to CSV", "can we get SSO with Okta", "a keyboard shortcut for archiving"],
        REPEL   ["cannot log in after password reset", "page takes thirty seconds to load", "charged twice this month"]
      ) WITHIN 0.5

-- other
MATCH DIRECTION(["customer support email", "account and service request"]) CONE 0.5
  AND NOT (
        DISTANCE("charged twice this month, refund, invoice wrong") WITHIN 0.55
     OR DISTANCE("cannot log in, two-factor code, account locked") WITHIN 0.55
     OR DISTANCE("page slow to load, export times out, app freezes") WITHIN 0.55
     OR DISTANCE("please add a feature, export to CSV, SSO, shortcut") WITHIN 0.55
  )
```

Publish text for a support message, written by the intake agent as a claim in message register rather than a pasted email:

```
customer reports a second charge on the same invoice this month and asks for a refund of the duplicate.
```

with the email id, customer id and received timestamp in metadata. Pasting the email itself would embed the greeting, the signature, the quoted thread and the legal footer alongside the one sentence that matters.

## Naming things the embedder has not seen

Internal project names, product code names and invented terms tokenize like hashes until the stream has established them. Until then, pair the name with a descriptive phrase every time it is the subject ("Quickstep, the shift rostering service"), and define it once in its own message. After a few hundred messages the name alone will sit in the right place, and the descriptive phrase can be dropped. Validate that claim rather than assuming it; a `DISTANCE` on the bare name against a sample of messages shows whether the name has landed.
