# C12 Teach-back

## Prompt

A mid-level engineer on your team has been asked to add typeahead to the product's search box. Their plan: "on
each keystroke, run `SELECT query FROM searches WHERE query LIKE :prefix || '%' ORDER BY count DESC LIMIT 5`; if
that gets slow, load all the queries into a trie on each server and walk it; rebuild the trie from the table every
night". They have never run anything at keystroke rate.

Explain to them, in your own words, **how a search-autocomplete service is built so that it stays fast, fresh and
safe as it grows, and why**: what the real unit of load is and how big it gets, why ranking has to happen before
the request rather than during it, what one node of the index holds and roughly what it costs in bytes, what the
API and the client do to keep most keystrokes off the servers, how counts become suggestions without going stale
or noisy, how a new index reaches a running fleet without locks or outages, and what stops the service from
suggesting something it never should.

Form: written, 500–900 words, one diagram (Mermaid or a sketch) allowed. Use numbers: pick a scale (theirs or the
module's) and show two or three calculations. No copying from the unit files; if a sentence from the course
appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, so they can go and change
their plan, not for an examiner.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Names the **keystroke** as the unit of load (≈ 15–20 lookups per search), with arithmetic at a stated scale
      that reaches lookups per second at peak, and concludes that **no database belongs on the read path**: the
      index lives in memory on stateless replicas.
- [ ] Explains why the top-K must be **precomputed per prefix at build time**: computing it at query time costs in
      proportion to the subtree, so the shortest, most-requested prefixes are the most expensive; with a stored
      top-K a lookup is O(len(prefix)) regardless of popularity.
- [ ] Sizes the index from **distinct prefixes × bytes per node** (not from query strings), states the per-node
      assumption, stores **ids rather than strings** under each prefix, and uses the result (with ≈ 2× headroom for
      the swap) to decide **replicate whole vs partition**.
- [ ] Describes a **cacheable API and an active client**: `GET` that depends only on (locale, normalised prefix,
      limit) with a public TTL; the same normalisation everywhere; client debounce and a session prefix cache; and
      sequence numbers (or the echoed prefix) so a late response never overwrites a newer one.
- [ ] Explains **freshness by decay**: aggregate *submitted searches*, weight them with a half-life rather than
      resetting or using a hard window, and name the knob and what changing it does to the product.
- [ ] Explains **publishing as immutable snapshots swapped atomically**: build, validate, version, load beside the
      old one, swap one pointer with no lock on the read path, stagger or canary across replicas, keep the previous
      version for rollback.
- [ ] States the **safety rules**: minimum support counted in **distinct users**, a blocklist, and a fast override
      path for takedowns that does not wait for the next build.

Should-hit (at least 4 of 7):
- [ ] The CDN answers the hottest (shortest) prefixes, but the origin is sized for the pre-CDN load because a purge
      or key change removes it.
- [ ] Fail open: on error or timeout the client shows nothing and search is unaffected; the suggest fleet shares no
      dependency with search.
- [ ] A health check that means "snapshot loaded", and freshness SLIs (snapshot age, empty-result rate) because a
      stale or empty index still returns 200.
- [ ] Why a Go trie with a map and pointers per node is ≈ 3–5× larger than a compact layout, and what it does to
      garbage-collection pauses; the pointer-free arena (and mmap) as the fix.
- [ ] A second, short-half-life trending tier fed by a streaming counter (sketch plus heavy hitters, verified), merged
      at lookup, with every safety rule applied to it too.
- [ ] Partitioning when the index stops fitting: by locale first, then by prefix range with split hot ranges (or a
      hash ring), hot shards replicated more.
- [ ] Why `atomic.Pointer` (or an equivalent release/acquire publish) and not a plain pointer write, in one or two
      sentences on visibility of the fully built index.

Red flags (any one = revise):
- [ ] A database, remote cache or search-index query on every keystroke presented as the design, with no
      precomputed index.
- [ ] Top-K computed at query time for short prefixes, or "put an LRU in front of it" as the fix.
- [ ] Updating the live serving index in place under a lock (or "rebuild and replace on every server at 03:00") as
      the publishing story, with no versioned snapshot, validation or rollback.
- [ ] Counting keystrokes, or raw request counts, as popularity; or support measured as raw counts only, so one
      person can make a query suggestible.
- [ ] A per-user field, `Vary: Cookie` or `POST` on the global suggest path with no acknowledgement that it
      disables shared caching.
- [ ] Counts that never decay, or that reset on a calendar boundary, presented as acceptable.
- [ ] Numbers stated as facts with no scale assumption, or a ≥ 8× error (searches vs keystrokes, bytes vs bits,
      strings vs nodes) that isn't caught.

## Recording

`sd record teachback C12 --result <pass|revise> --notes "<missed items>"`
