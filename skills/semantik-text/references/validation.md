# Validating an anchor set

An anchor set is a claim that certain phrases separate certain messages under a certain embedder. Claims get checked. This is the check, and it is a few dozen lines of arithmetic over vectors you already have.

Every number here is a cosine similarity, on the same scale and in the same direction as `WITHIN`: higher is closer, and `WITHIN` is a floor.

## 1. Build the sample

- Real messages, not invented ones. Pull them from the namespace with `/v1/search` (results carry `content`), or from the publisher's logs. Invented messages are written by the same person who wrote the anchors and share their register, which hides exactly the failure you are looking for.
- At least 20 per label, 30 or more is better. Below 20 the thresholds you derive are guesses.
- Label by hand or by the publisher's own intent field, never by the subscription under test.
- Include hard negatives: messages from adjacent topics, sibling categories, and the confusable intents (do against done, ask against object). A sample with only easy negatives passes every anchor set.
- For a category partition, include messages that belong in "other", labelled `other`, so the residual bin is tested too.

## 2. Embed

Use the namespace's pinned model at the namespace's pinned dimensions. Semantik does not return vectors, so embed through the model provider directly (the same model string the namespace was created with). If the namespace was created with pre-computed vectors, use whatever produced them.

Embed each anchor phrase on its own, exactly as it will appear in the SemQL. Normalise every vector to unit length; then cosine similarity is a dot product and everything below is one line each.

For an ATTRACT list, average the phrase vectors and renormalise: that is the composite SemQL builds. For a CONTRAST, subtract the mean of the REPEL vectors from the mean of the ATTRACT vectors and renormalise.

## 3. Compute

Five quantities, in this order. Stop at the first failure: each one's fix invalidates the ones below it.

1. **Anchor pairwise similarity.** Every anchor against every other. Keep the maximum.
2. **Message centroids.** Per label, the normalised mean of that label's message vectors. Their pairwise similarities, and the accuracy of assigning each message to its nearest centroid.
3. **Nearest-anchor accuracy.** Assign each message to the anchor it is most similar to; count how often that is its own label. Keep the confusion pairs.
4. **Per-anchor tails.** For each anchor: the in-class similarities' 10th percentile (`in_p10`, the in-class messages that sit lowest against their own anchor) and the out-of-class similarities' 90th percentile (`out_p90`, the impostors that sit highest). A clean floor exists when `in_p10` is above `out_p90`, and it goes between them.
5. **The same again for composites.** Rebuild each anchor as a CONTRAST that repels its two or three nearest siblings, and repeat 3 and 4. This is what a real intent or category subscription evaluates, and it is the number to put in the clause.

## 4. Read the result

**Maximum anchor pair similarity.** The closest pair is the weakest link. If it is not comfortably below your intended WITHIN (leave at least 0.1), two anchors are too close for the floor and no message sample will fix that: a message clearing the floor for one clears it for the other. Rephrase one of the pair with a distinguishing noun. For intent phrases, lengthen rather than replace, so the shared prefix on the publisher side stays recognisable.

**Nearest-centroid accuracy.** Centroids are the best possible anchors for this sample, so this is an upper bound on what any phrasing can reach. Below about 0.9 the labels are not separable by this embedder at all. The problem is upstream: the publish text does not carry the distinction (see the "cannot do" section of `embedding-behaviour.md`), or the partition cuts along an axis the embedder does not see. Fix the publish text or recut the categories before touching anchors.

**Nearest-anchor accuracy.** How close the anchors came to the centroids. A large gap between the two means the anchors are phrased wrong, not that the labels are inseparable.

**Composite accuracy.** Similarities to a composite run lower than to a plain anchor, so take the floor from the composite numbers, never from the plain-anchor ones. If composite accuracy is *lower* than nearest-anchor accuracy, the repel lists are hurting: a repelled sibling shares too much with the attract phrase (shared head word, same nouns). Repel fewer siblings, or rephrase.

**Confusion pairs.** Every pair is a phrasing task. The pair with the most confusions gets fixed first.

## 5. Pass bar

- Maximum anchor pair similarity below the intended WITHIN minus 0.1.
- Nearest-centroid accuracy at or above 0.9 (if not, stop and fix the publish text).
- Intent sets: composite accuracy at or above 0.95, and for every label `in_p10` above `out_p90`.
- Category sets: composite accuracy at or above 0.9 per bin, and the residual bin's messages not captured by any named bin at the chosen floors.
- Topic subscriptions: no accuracy number, since there is one label; treat the topic as the label and the rest of the stream as out-of-class, and require precision and recall both above 0.85 at the chosen floor.

## 6. What to change when it fails

| Failure | Change |
|---|---|
| Two anchors too close | lengthen one with a distinguishing noun; for intents, keep the original words and append |
| Centroids inseparable | the publish text lacks the distinction; add content words on the publisher side, or accept that the consumer reads it after delivery |
| Plain anchors fine, composites worse | repel list shares concepts with attract; drop the offending sibling or rephrase it |
| Good accuracy, but no clean floor (`in_p10` below `out_p90`) | the class has a long tail of short or vague messages; enforce the length and naming rules on the publisher, or accept lower recall |
| Category bins bleed | merge, or recut along an axis with visible nouns; check the parent DIRECTION is not the thing doing the separation |
| Everything passed last month, fails now | embedder changed, or publisher vocabulary drifted; re-sample and re-run, expect new floors |

## 7. Cadence

Run on creation, on every change to an anchor phrase or a publisher's rendering, and on every embedder or dimension change. Keep the sample and the numbers next to the subscription definitions, so the next person can see what the floors were measured on.
