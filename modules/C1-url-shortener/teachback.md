# C1 Teach-back

## Prompt

A mid-level engineer on your team has been asked to build a link-shortening service for the platform's
transactional messages. They say: *"It's a weekend project. One table with the code and the long URL, a POST to
create, a GET that redirects. Why are we spending a design review on it?"*

Explain, in your own words, **why a URL shortener is a real system and where the three hard parts are**. Write
400–800 words (or talk it through with the tutor) for someone who has built CRUD services but has never run a
read-heavy public endpoint. You may include one diagram. Use at least two concrete numbers from your own
estimates, and at least one example from the banking platform (statement links, verification links, phishing
takedowns).

Your explanation should cover: what the numbers say about the shape of the problem; how two servers avoid
giving out the same code and why codes must not be guessable; what makes the redirect path fast and keeps it up
when things fail, including the viral link and the cold cache; why click counting and abuse handling stay off the
hot path; and how a takedown actually reaches every copy.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] States the **shape from the numbers**: creates are tiny (tens to hundreds per second), redirects are two
      orders of magnitude more, storage is a few TB over the retention, and concludes that the risks are ids, the
      redirect path and the pipeline, not write throughput.
- [ ] Explains **uniqueness through one atomic primitive**: insert-if-absent (unique index or conditional put) for
      random codes, or compare-and-swap on a counter for leased ranges, and says that a check-then-act version
      produces duplicates.
- [ ] Gives the **collision or headroom arithmetic** for the code length (e.g. 62^7 ≈ 3.5 × 10^12; ≈ 0.2% collision
      at 6B random codes) and explains **why codes must be unguessable** (enumeration of personal links, leaked
      volume) with a mechanism (random codes, or a keyed permutation of a counter).
- [ ] Explains the **redirect path's layering** (in-process L1, shared cache, store), why the viral key needs
      the L1 plus request coalescing, and the **cold-cache factor** `1/(1 − h)` with what it means for the store.
- [ ] States the **302 vs 301 decision** and its consequences for analytics and takedown.
- [ ] Describes the **takedown fan-out** (store first, then every cache layer, then the edge) and why TTL alone
      does not meet a minutes-level SLA, with how you would prove completion.
- [ ] Keeps **click counting and reputation scanning off the hot path**: async events into a log and an
      aggregator; the scan after create with disable-on-hit; names the cost of doing either synchronously.

Should-hit (at least 4 of 7):
- [ ] Idempotent creates (sender retries must not mint a second link) with the key scoped per caller or message.
- [ ] Negative caching and code-shape validation against enumeration, with the "unknown codes are permanent
      misses" argument.
- [ ] Lazy expiry on read plus a background sweeper, and 404 vs 410 with tombstones.
- [ ] What a store outage does: cached redirects survive, misses fail fast behind a timeout and a breaker,
      creates degrade only when leased ranges run out.
- [ ] The range-size trade-off (store ops per id vs ids wasted per crash vs minutes of creates during a counter
      outage).
- [ ] Analytics storage outgrowing the links table, and HyperLogLog (≈ 12 KB, ≈ 1% error) for uniques.
- [ ] A link to the banking context: personal destinations, single-use verification links, per-tenant domains or
      takedown evidence for compliance.

Red flags (any one = revise):
- [ ] Proposes check-then-insert for uniqueness, or a global counter incremented on every create as the only
      option, without naming the atomic primitive or the coordination cost.
- [ ] Exposes a sequential id as the code without discussing enumeration.
- [ ] Puts the click counter in the link row or the reputation scan on the create path's latency budget.
- [ ] Treats TTL expiry as the takedown mechanism, or recommends 301 "because it's permanent" without the
      analytics and takedown consequences.
- [ ] Claims the cache only improves latency and never discusses the cold or hot-key cases.
- [ ] States an invented throughput or pricing figure as fact.

## Recording

`sd record teachback C1 --result <pass|revise> --notes "<missed items>"`
