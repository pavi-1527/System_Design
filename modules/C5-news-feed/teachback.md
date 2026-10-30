# C5 Teach-back

## Prompt

A mid-level engineer on your team is designing the first version of a social feed for a side project that is
suddenly growing. Their plan: "when someone posts, loop over their followers and write the post into each
follower's feed table; when someone opens the app, `SELECT` their feed table ordered by time; for the two accounts
with a million followers we'll just run the loop in a background job". They have never seen a feed fall over.

Explain to them, in your own words, **how a feed is built so it stays fast and correct as it grows, and why**: what
the real unit of work is, when pushing posts to followers is right and when it isn't, what the hybrid looks like
and how you decide who gets which treatment, what the per-user timeline actually stores and why it is bounded and
rebuildable, what a read does, and which consistency promises you make (and don't) to authors, readers, and people
who delete or unfollow.

Form: written, 500–900 words, one diagram (Mermaid or a sketch) allowed. Use numbers: pick a scale (theirs or the
module's) and show two or three calculations. No copying from the unit files; if a sentence from the course
appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, so they can go and change
their design, not for an examiner.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Names **deliveries** (posts × followers) and **hydrations** (reads × page size) as the units of work, with
      arithmetic at a stated scale, and concludes that fan-out and the read path, not post storage, are the
      problem.
- [ ] Explains why a high-follower account breaks fan-out on write as a **burst and head-of-line problem** (one
      post = minutes of a worker or seconds of the whole fleet, blocking other authors), not merely "too many
      writes per day", and why fan-out on read for everyone makes **read cost proportional to followees**.
- [ ] Describes the **hybrid**: push for ordinary authors, merge on read for the few; the mode **recorded per
      post** so an author crossing the threshold neither duplicates nor loses posts; and at least one concrete
      budget that sets the threshold (burst per post, merge width per read, or hot-key read rate).
- [ ] The **timeline is a bounded cache of IDs** (≈ hundreds of entries, bytes per entry), not of post bodies, is
      **derived** from the posts store and the follow graph and therefore **rebuildable**, and reads **hydrate**
      IDs from a separate body cache with a database fallback.
- [ ] Timeline writes are **idempotent sorted inserts**, which is what makes at-least-once, out-of-order delivery
      from the queue/log invisible to readers (with the offset or ack handled after the write).
- [ ] States the consistency promises explicitly: **eventual for followers with a lag target**, **read-your-own-
      posts for the author** by reading the truth or merging the author's own recent IDs, and **deletes enforced
      at hydration by a tombstone** rather than by cleaning every timeline.

Should-hit (at least 4 of 7):
- [ ] Post creation commits the post and an outbox row (or writes to a durable log) before responding, and
      fan-out runs asynchronously from it; the post API is idempotent per client key.
- [ ] The log/queue is partitioned by author for ordering and replay; failures retry with backoff then go to a
      DLQ; a lane or rate cap keeps mid-size bursts from delaying ordinary authors.
- [ ] Misses (inactive user, lost cache node) rebuild from followees' recent posts with coalescing and a
      concurrency budget, and why a rebuild storm can take down the posts store otherwise.
- [ ] Memory arithmetic for the timeline tier (readers × entries × bytes) and the observation that this, not post
      storage, is the cost driver; eviction of inactive users' timelines; push to active followers only.
- [ ] The celebrity's recent-posts list is a hot key that must be replicated or cached in-process.
- [ ] Cursor (keyset) pagination on a time-sortable ID, and why offset paging duplicates and skips.
- [ ] Unfollow/block handled eagerly on the one affected timeline; how a ranked feed would change the picture.

Red flags (any one = revise):
- [ ] Fan-out performed inside the request that creates the post, or fan-out to a million followers described as
      "a background job" with no bound, lane or hybrid.
- [ ] Post bodies (text, author name) stored in each follower's timeline as the design (not as an explicitly
      costed exception).
- [ ] The cache described as the only copy of the feed, with no rebuild path, or "we never lose the cache".
- [ ] A claim of strong consistency or exactly-once delivery for followers' feeds, or "Kafka guarantees each
      post is delivered once".
- [ ] Push vs pull decided by the author's *current* follower count at read time, with no per-post record.
- [ ] Deletes handled only by removing the ID from timelines (so a rebuild or a lagging worker resurrects the
      post), or not handled at all.
- [ ] Numbers stated as facts with no scale assumption, or a ≥ 8× unit error (bytes vs bits, per day vs per
      second) that isn't caught.

## Recording

`sd record teachback C5 --result <pass|revise> --notes "<missed items>"`
