# How embeddings see text

This is the reasoning behind the rules in SKILL.md. Read it when a rule needs justifying, when the user asks why, or when a case is not covered by a rule and you need to reason from the model.

Everything here describes typical behaviour of contrastively trained sentence embedders (the text-embedding-3 family, e5, bge, gte, Qwen embedding models and their relatives). The direction of each effect is stable across models; the magnitude is not, and thresholds are always model-specific. Measure before relying on any number.

## What a sentence embedding is

An embedder maps text to a point on a sphere such that texts which were paired in training (paraphrases, question and passage, title and body, query and click) end up close. Training pairs are dominated by *what the text is about*, so the geometry is a topic geometry first. Everything else (mood, tense, polarity, register, specificity) is present, but weakly, because the training signal rarely separated two texts on those grounds alone.

Consequence: the vector is close to a weighted average of the concepts the text names, weighted by how content-bearing each token is. Function words, punctuation and formatting contribute little. Rare identifiers contribute noise. Repeated concepts contribute more.

## Feature by feature

| Feature of the text | Effect on the vector | Implication |
|---|---|---|
| Concrete nouns and noun phrases | dominate the direction | write the subject and the claim in nouns |
| Verbs of action | moderate | useful when the action is the point ("duplicates", "rejects", "overwrites") |
| Tense, mood, person | very weak | do not route on them; use a canonical phrase for intent |
| Negation ("not", "no", "never", "un-") | very weak, often invisible | carry polarity in a content word ("unsafe", "fails", "duplicate") |
| Pronouns, demonstratives, option letters | near zero, and they displace content | name the referent instead |
| Numbers, error codes | weak; HTTP codes land in a generic HTTP region | fine as topic hints, useless for distinguishing 500 from 503 |
| Hashes, UUIDs, commit ids | noise; tokenized into fragments with no meaning | metadata, never text |
| URLs and file paths | pull toward a shared "URL/code" region regardless of topic | metadata; the path's meaningful words can be restated in prose if needed |
| Code identifiers (`Reservation.Create`, `TestCreateRetryMidSubmit`) | subword tokens carry the embedded English (reservation, create, retry, submit) | acceptable in text when they are the subject; keep them few |
| Code blocks, stack traces, JSON | strong pull toward the code region, topic washed out | metadata or a separate message that states the claim in prose |
| Markdown, labels (`At:`, `Check:`), brackets | mostly ignored, but constant labels across all messages become a shared offset | strip before embedding |
| Greetings, signatures, boilerplate | shared offset | strip |
| Domain terms used consistently | sharpen the region | one term per thing |
| Synonyms for the same thing | spread the region | define once, reuse |
| Mixed languages in one text | average of two regions | one language per message |
| Proper nouns the model has seen (AWS, Postgres, Kubernetes) | strong, well placed | use them |
| Proper nouns it has not seen (internal project names) | weak or misplaced; tokenized like a hash | add a descriptive phrase next to the name until the name is established in the stream |

## Length

Very short texts (one to five content words) produce unreliable vectors. There is too little signal, so the vector sits somewhere generic and any constant prefix dominates it. Very long texts produce averaged vectors: a paragraph that makes one specific claim among five general sentences embeds as the general topic, and the specific claim is lost. The reliable range for routing is roughly one to three sentences of content, ten to forty content words. When a message must be longer, the first sentence should already carry the claim, and if two claims are present they should be two messages.

## Constant prefixes: marker or offset

Adding the same phrase to every message of one class gives every vector in that class a shared component. Vectors of that class move toward the prefix and away from other classes. That is a class marker and it is how intent routing works.

Adding the same phrase to every message of every class gives every vector the same shared component. All vectors move toward one point, the angles between them shrink, and every threshold becomes harder to set. That is a shared offset and it is pure loss.

The rule follows: keep exactly one constant per class (the intent phrase), and remove every constant that is not class-specific.

The same logic explains why a subscription's REPEL list should name siblings rather than everything. Repelling the mean of many classes subtracts something close to the shared offset, which helps a little, but repelling two or three near siblings subtracts the specific directions that cause confusion, which helps a lot.

## Register and symmetry

Semantik embeds the anchor string and the message text with the same model and compares them with a symmetric similarity. There is no query side and no document side unless the pinned model was trained with instruction prefixes, and then both sides carry the same prefix.

Texts cluster by register as well as by topic. "Messages reporting completed work" is a sentence *about* a class of messages; it lives near other class descriptions, catalogue entries and documentation. "Reservation.Create returns the existing hold when the submit token repeats" is a sentence *in* that class; it lives near other implementation reports. The two share a topic and differ in register, and the register gap is often as large as a topic gap.

Two ways to close it:

1. Exemplar anchors. Write the anchor the way a matching message would be written. For topics and categories this is the right default.
2. Canonical phrases. Have the publisher include a fixed phrase and put the identical phrase in the anchor. Now the message contains the anchor's text, and the gap is closed by construction. For intent this is the only method that works, because intent has no content words to write an exemplar with.

## Similarity and thresholds

SemQL thresholds on cosine similarity, and `WITHIN` is a floor: higher is tighter. For text it runs from about 0 to 1. Modern embedders compress that range from both ends: unrelated texts do not fall to 0, and close paraphrases do not reach 1. Rough shape, to be measured per model:

- near-paraphrase and retells: high, upper eighties and nineties
- same topic, different claim: middle
- same domain, different topic: lower middle
- unrelated: low, but rarely below about 0.1

The gaps between these bands are narrow, which is why thresholds must come from measurement rather than intuition, and why a threshold tuned on one model is wrong on the next. The semql skill's starting points (WITHIN 0.6 for a known pattern, 0.5 for a CONTRAST separating two close concepts, 0.85 for near-duplicates, CONE 0.2 to 0.5 for topics) are starting points only.

A CONTRAST composite is not a piece of text and nothing sits especially close to it, so similarities to it run lower than similarities to a plain anchor of the same quality. A CONTRAST floor and a DISTANCE floor of the same number are not the same strictness; compare each against its own measured distribution.

DIRECTION ignores magnitude and compares angle to a mean direction, so it tolerates length and specificity differences better than DISTANCE. DISTANCE is sensitive to both. That is why a specific exemplar sentence belongs in DISTANCE and a set of concept phrases belongs in DIRECTION.

CONTRAST composites are normalized differences of means. The direction is stable only when the attract mean and the repel mean are well separated; when they share concepts the difference is small, normalization amplifies noise, and the composite points somewhere arbitrary. That is the mechanism behind the "no shared head word" rule.

## Deixis and closure

A reply that says "re your last message, agreed" carries no topic. It embeds near other short agreements, not near the message it answers. Subscribers who matched the original never see the reply, and any process that waits for closure by subscription waits forever. Restating the subject in the reply is not redundancy, it is the addressing mechanism. The same holds for `take` after `block`, `fix` after `tell`, `verdict` after `done`: each closing message should land next to the message it closes, and the only way to land there is to say the same nouns.

## Retells and duplicates

Two texts that say the same thing embed very close to each other, usually above 0.85 to 0.9 similarity on most models. This is the one place where a raw-vector anchor is the right tool: a subscriber that wants to suppress retells of a message it just published uses DISTANCE on that message's own vector with a high WITHIN, and gets exactly the retells.

## What the embedder cannot do

Some distinctions are invisible at any threshold: pass versus fail stated only by one word, do versus done stated only by tense, a claim versus its negation, the difference between two option letters. When routing depends on such a distinction, one of two things is true: the publisher can add content words that make the distinction visible (a fail states what failed; a done states what was finished), or the distinction is not routable and must be read by the consumer after delivery. Deciding which is the case is part of writing the protocol, not part of writing the anchor.
