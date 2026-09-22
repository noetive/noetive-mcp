---
name: semantik-text
description: 'Choose the words Semantik embeds: the text a publisher sends and the anchor phrases inside DISTANCE, DIRECTION and CONTRAST. Use when deciding what goes in text versus metadata, how agents should phrase messages so subscribers match them, how to word anchors for a topic, a category partition or an intent such as ask, report or verdict, or why a subscription matches everything, nothing or the wrong polarity. The semql skill owns the grammar and the JSON format; this one owns the wording and how to measure it.'
---

Semantik delivers a message to every subscription whose SemQL predicate holds on the message's embedding. The predicate is geometry, and the geometry comes from words. This skill is about choosing those words on both sides: the `text` a publisher sends, and the anchor strings a subscriber puts inside DISTANCE, DIRECTION and CONTRAST.

Read `references/embedding-behaviour.md` when you need the reasoning behind a rule or the user asks why. Read `references/anchor-catalog.md` for ready intent anchors, role inboxes and category templates. Read `references/validation.md` before declaring any anchor set finished.

Thresholds throughout are cosine **similarity**: `WITHIN` is a floor, so higher is tighter, and `CONE` is a half-angle, so smaller is tighter. The semql skill owns both knobs; this skill owns the words inside the clauses.

Every query below narrows meaning and nothing else. Where it runs is the `namespace` argument on the tool call, not a `NAMESPACE` clause: the clause exists, but writing one that disagrees with the argument is answered `400 invalid_request`. See the semql skill.

## Two facts that decide everything

**Only `text` is embedded.** Metadata travels with the message and comes back to consumers, but SemQL v1 has no clause over metadata. Anything that must influence routing goes in the text. Anything that would only add noise to the vector (ids, URLs, hashes, timestamps, roles, sequence numbers, code) goes in metadata, where it is still available to the consumer.

**Similarity is symmetric and both sides use one model.** A subscription anchor is just another piece of text embedded the same way as messages. It matches messages written the way it is written. An anchor that *describes* a class ("messages reporting completed work") sits in a meta register; a message that *is* a completed-work report sits in the object register, and the two are farther apart than intuition suggests. So either write anchors as exemplars of the messages, or have publishers carry a canonical phrase that the anchor repeats verbatim.

## Three routing axes, three techniques

Embeddings expose these three unevenly, so each needs its own method.

| Axis | Answers | Signal | Technique |
|---|---|---|---|
| Topic | what is this about | strong, carried by content nouns | DIRECTION over concept phrases, DISTANCE over one exemplar sentence |
| Category | which bin of a fixed partition | medium, sibling bins overlap | CONTRAST: exemplars of the bin attract, exemplars of its siblings repel |
| Intent | what kind of act is this: question, report, request, verdict | weak, content words carry no mood | canonical phrase agreed between publisher and subscriber, CONTRAST between the phrases |

Intent is the axis people get wrong. "Add an idempotency key to Reservation.Create" and "Added an idempotency key to Reservation.Create" land within noise of each other; tense and mood barely move a vector. The fix is not a cleverer anchor, it is agreement: the publisher prepends one fixed phrase per intent, the subscriber attracts that phrase and repels the phrases of the neighbouring intents.

## Writing publish text

Write `text` for the embedder. The reader gets metadata and can fetch the rest.

- **One concern per message.** Two topics in one text average into a vector near neither. Split them.
- **Lead with the subject and the claim, in concrete nouns.** "Reservation.Create double-books the slot when a submit is retried after the response starts" routes. "We found a problem with the thing we discussed" does not.
- **Name, never point.** Pronouns, option letters, "the above", "your last message", "as specified" embed to nothing. A reply written that way never lands next to what it answers, so subscribers of the original never see the closure. Restate the subject in words.
- **Carry polarity in content words.** Embeddings nearly ignore "not". "Retrying a submit is unsafe, it can double-book the slot" separates from the safe case; "not safe after the response starts" does not.
- **Keep one constant prefix per intent and drop every other constant.** A phrase on every message of one class is a class marker. A phrase on every message of every class (greetings, signatures, "Message:", label words such as `At:` or `Check:`) is a shared offset that pulls everything together and shrinks the separation you are trying to route on.
- **Move locators, ids, hashes, dates, URLs, code and stack traces to metadata.** They drag the vector toward a generic code-and-URL region that unrelated messages share. A claim about a resource is text; the resource's address is metadata.
- **Include topic words as words.** A thread named `booking-retry-dupe` contributes `booking retry dupe` when rendered with spaces. A thread named `T-4412` contributes nothing.
- **Ten to forty content words.** Under about six words the vector collapses toward the prefix or toward "generic short text". Past a paragraph the vector is an average and the specific claim is diluted.
- **One term per thing.** Define a term once in its own message ("hold: a reservation with no payment yet, released automatically after fifteen minutes"), then use it bare. The definition seeds the region; synonyms scatter it.
- **Publish claims about resources, never resources.** "Reservation.Create inserts a row per submit with no uniqueness on the request id, so a retry double-books" as text, with `at: app/booking/reservation.rb:58@a1b2c3d` in metadata. The resource is represented by the claims made about it, each checkable at its address.

Before and after, where the publisher's wire message is a compact protocol line and the published `text` is rendered from it:

```
wire   done: idempotency key added.
text   finished work reported for verification. booking retry dupe. Reservation.Create now takes an idempotency key from the submit token and returns the existing hold on a repeat.

wire   decide: (c). Unique index on the request id.
text   a design decision with alternatives rejected. booking retry dupe. reservations get a unique index on the request id, so a retried submit conflicts instead of inserting a second row.

wire   tell: not safe once the response starts.
text   a statement of fact with its source. booking retry dupe. retrying a submit after the response has started can double-book the slot.
```

The rendering rule is `intent phrase + topic words + point sentence`. It is a table lookup on the intent word, so a small structured wire format costs nothing at the embedder, and the wire message itself (or its address) rides in metadata.

## Writing subscription anchors

Rules for any anchor string, whatever clause it sits in:

- **Same register as the messages.** Write what a matching message would say, or the exact canonical phrase the publisher uses. Never a description of the class.
- **Concrete nouns, no meta words.** "message", "content", "information", "update", "about", "issue", "thing" carry almost no direction.
- **No negation inside an anchor.** "not a tutorial" embeds next to "tutorial". Negation lives in REPEL or NOT DISTANCE, never in the words.
- **Several short phrases over one long sentence** for DIRECTION and for attract/repel lists, because the composite is a normalised mean and short phrases contribute clean concepts.
- **Attract and repel never share a head word.** "performance optimization" against "performance testing" cancels. "reduced latency" against "monitoring dashboard" separates.
- **Measured before trusted.** See Validation below.

### Intent

Take the canonical phrases from `references/anchor-catalog.md` or agree new ones with the publisher, byte-identical on both sides. Attract the phrase, repel its two or three nearest siblings (repelling all of them weakens the composite), and combine with a topic clause so the inbox is scoped:

```sql
MATCH CONTRAST(
        ATTRACT ["finished work reported for verification"],
        REPEL   ["a request for work with a completion criterion",
                 "a verification result, pass or fail, on finished work"]
      ) WITHIN 0.5
  AND DIRECTION(["reservations", "booking retry", "Ruby"]) CONE 0.5
```

Start `WITHIN` near 0.5 (a composite sits further from everything than a plain anchor does, so a CONTRAST floor is lower than a DISTANCE floor for the same strictness), and then set it from the measured in-class and out-of-class similarities rather than by feel.

### Information topic

For thematic coverage, DIRECTION over two to five concept phrases with a CONTRAST against the nearest adjacent topic that shares the stream:

```sql
MATCH DIRECTION(["reservation double-booking", "idempotency key on submit", "retry-safe form POST"]) CONE 0.4
  AND CONTRAST(
        ATTRACT ["booking write semantics", "duplicate reservation after retry"],
        REPEL   ["seat pricing and discount codes", "login and session expiry"]
      )
```

For one specific pattern, DISTANCE over a single exemplar written as a message would be, not as a label:

```sql
MATCH DISTANCE("a retried PUT after response headers produced a duplicate object") WITHIN 0.6
```

Use the nouns publishers actually use. If publishers write "conditional PUT" and the anchor says "optimistic concurrency on object writes", the cone misses them even though a human sees the link. When you do not know the publishers' vocabulary, sample their messages first.

### Category

A category set is a partition: sibling bins under one parent topic, and a message should fall in one of them. Build it in four steps.

1. Parent topic as DIRECTION, so nothing outside the domain enters any bin.
2. Per bin, ATTRACT three to six exemplar phrases typical of messages in that bin, in message register.
3. Per bin, REPEL the exemplars of the two or three siblings it is most often confused with. Use the siblings' own attract phrases, so the partition is consistent.
4. Measure pairwise separation between the bins' attract sets. Two bins whose sets sit above roughly 0.8 similarity to each other are one bin as far as the embedder is concerned: merge them, or recut the partition along an axis the embedder can see.

```sql
MATCH DIRECTION(["customer support email", "account and service request"]) CONE 0.5
  AND CONTRAST(
        ATTRACT ["charged twice this month", "refund for the unused period", "invoice amount is wrong"],
        REPEL   ["cannot log in after password reset", "page takes thirty seconds to load", "please add a dark mode"]
      ) WITHIN 0.5
```

Write the residual bin ("other") as NOT of the union of the named bins, never as a positive anchor. "Miscellaneous" has no direction.

## When a subscription misbehaves

| Symptom | Likely cause | Fix |
|---|---|---|
| Matches nearly everything | single DIRECTION with a generic anchor, or a `WITHIN` floor below about 0.3 | add CONTRAST or a specific DISTANCE, raise `WITHIN`, narrow `CONE` |
| Matches the opposite polarity | negation in the anchor or in the messages | polarity words on both sides, REPEL the opposite |
| Misses messages a human would match | meta-register anchor against object-register messages, or messages that point instead of name | rewrite anchors as exemplars, rewrite publish text to name things |
| Two categories bleed into each other | sibling overlap, shared head words | recut, repel each other's exemplars, or merge |
| Intent routing looks random | no canonical prefix, relying on tense and mood | agree the prefix, attract it, repel its siblings |
| A reply never reaches the original's subscribers | reply written with deixis | restate the original's subject in the reply |
| Everything drifts after a model change | anchors tuned to the old embedder | re-run validation, re-phrase, re-threshold |

## Validation

Do not ship an anchor set on judgment. Embed the anchors and a labelled sample of real messages with the namespace's pinned model, then compute anchor separation, nearest-anchor and contrast accuracy, the confusion pairs, and a `WITHIN` per anchor with the precision and recall it buys. On unit-normalised vectors each of those is one line of arithmetic; `references/validation.md` gives the sample, the formulas, the pass bar and what to change when it fails. Re-run on every embedder change; anchors belong to the model that embeds them.

## Output shape

When asked to write publish text: give the rewritten `text`, a metadata map, and one line per item on what moved and why.

When asked to write subscriptions: give SemQL in text form (add the JSON form when the user will paste it into code; the semql skill covers that format), one line per clause on what it does, and the validation step as the last item.

When asked why a subscription misbehaves: name the symptom row, show the offending anchor or message text, show the rewrite.
