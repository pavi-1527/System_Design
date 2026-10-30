# D3 Rubric — senior-level scoring for the 45-minute HLD mock

The scale is the course's (MODULE_TEMPLATE.md §6): **below** would fail a mid-level loop; **approaching** is the
right direction but shallow or needed heavy prompting; **meets** is the senior bar (driven by the candidate,
quantified, trade-offs explicit, failure modes covered); **exceeds** is staff signal (anticipates the interviewer,
reasons about evolution, cost and org impact).

Score what was **on the board or said before 0:45**, plus the answers to probes. Nothing written after the stop
counts. Every level needs one piece of evidence in the log: a timestamp, a quote or a number.

## Dimensions

| # | Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|---|
| 1 | **Requirements & scope** | Lists features; no NFR numbers; scope never bounded | NFRs named but some without numbers; out-of-scope implicit; asks generic questions | 3–5 core functions; every NFR has a number; explicit out-of-scope; asks the questions whose answers change the design and says why | Names the dominant NFR and how the design shifts if it moves 10×; negotiates scope to protect the hard part |
| 2 | **Estimation** | No numbers, or a ≥ 8× unit error left uncaught; numbers never used | Numbers within ≈ 2× but several not tied to a decision; peak, replication or retention missing | ≤ 6 decision-driving numbers within ±25% of the bank's reference (the `sd estimate` tolerance); units carried; each followed by its decision | Sanity-checks one number against another; gives a range for a shaky input; says which number would flip the design |
| 3 | **API & data model** | No contract, or one that contradicts the requirements; schema without access patterns | Endpoints listed but pagination/idempotency/auth missing where needed; store chosen by habit | Contract reflects NFRs (cursor pagination, idempotency keys on retried writes, auth); access patterns → keys → store; partition key named and justified | Anticipates the hot-key or cross-partition query and shapes the key or a secondary index for it |
| 4 | **High-level design** | No end-to-end design by 0:30; boxes contradict requirements | Paths traceable with gaps; some boxes decorative or unjustified | Complete write and read paths by ≈ 0:25; every box justified by a requirement or a number; one request walked through | Diagram already shows where the deep dives will happen and why; nothing is decorative and nothing is missing |
| 5 | **Deep dives & trade-offs** | Describes a component; "because it's scalable"; no alternatives | One alternative mentioned; costs or flip conditions missing; dives chosen by comfort, not risk | 2–3 dives chosen by risk; each compares ≥ 2 options with cost and a measurable flip condition (claim → because → cost → flip) | Quantifies the trade-off (lag in seconds, cost per month, probability of collision); proposes a migration path between options |
| 6 | **Probe handling** | Defensive, contradicts the design, or invents facts with confidence; ignores the requirement change | Answers need two or more pushes to reach a mechanism or number; requirement change triggers a redesign or is bolted on without consequences | Answers from the design on the board: mechanism + number + cost, in ≤ 2 minutes; says "I don't know, here's how I'd find out" when true; absorbs the requirement change as a delta and names what it costs | Pre-empts the next rung ("…and at 10× that becomes the bottleneck, so…"); turns a probe into an improvement of the design |
| 7 | **Failure, operations & cost** | "Add replicas"; no detection; no security | Failure modes named without detection or recovery; cost or security only when asked | For the critical path: failure modes with blast radius, detection (SLI + alert threshold), recovery; rollout/migration; the dominant cost driver; the obvious security boundary (authn/z, tenant isolation, abuse) | Error budgets, degradation modes, and cost/security trade-offs reasoned with numbers; operational ownership considered |
| 8 | **Communication & time** | No structure; > 5 min over; interviewer has to drive | All phases present but a checkpoint missed or two slipped; signposting weak | Drives: announces phases, summarises each in a line, follows steers and returns to the plan; checkpoints on time (≤ 2 min slip); stops at 0:45 | Also adapts the plan out loud when the interviewer steers, and leaves the interviewer nothing to prompt |

**Gating dimensions:** 4 High-level design · 5 Deep dives & trade-offs · 6 Probe handling · 8 Communication & time.
These are what separate a senior HLD round from a mid-level one, and they're the course's weak spots (b) and (d).

## Caps (applied before the overall rule)

| Observation | Cap |
|---|---|
| No numbers by 0:15 | Estimation ≤ approaching |
| A ≥ 8× unit error (bits/bytes, per-day/per-second) not caught before 0:45 | Estimation = below |
| A decision-driving estimate off by > 2× from the reference, uncaught | Estimation ≤ approaching |
| No end-to-end diagram by 0:30 | High-level design ≤ approaching |
| Requirement change ignored, or answered with a full redraw | Probe handling ≤ approaching |
| An invented fact stated with confidence and not withdrawn when probed | Probe handling ≤ approaching |
| No closing phase (bottlenecks/failures) at all | Failure, operations & cost ≤ approaching |
| One checkpoint missed, or two slipped | Communication & time ≤ approaching |
| Overran 0:45 by > 3 minutes | Communication & time = below |

## Overall rule

- **meets** = meets or better on every gating dimension **and** no dimension at below.
- **exceeds** = meets or better on every dimension **and** exceeds on at least three, including at least two gating
  dimensions.
- Otherwise, overall = the **lowest gating-dimension level**, and never above **approaching** if any dimension is
  below.

Worked examples:

| Scores (1–8) | Overall | Why |
|---|---|---|
| M M A M M M A M | meets | all four gating at meets, nothing below |
| M B M M M M M M | approaching | estimation at below caps the overall at approaching |
| E M M M E E M M | meets | only two exceeds; exceeds needs three |
| E E M E E E M M | exceeds | five exceeds, three of them gating |
| M M M A M M M M | approaching | HLD (gating) at approaching |

## Calibration anchors

The same moment at four levels, so self-scores and tutor scores mean the same thing.

**Probe: "The cache cluster loses a node at peak. What happens?"** (dimension 6)

- *below:* "Redis is replicated, so nothing happens."
- *approaching:* "We'd get more misses and the DB takes more load." (Needs a push for how much, and what then.)
- *meets:* "One of 6 nodes is ≈ 17% of keys. At 200k reads/s and a 95% hit rate, misses go from 10k/s to ≈ 42k/s (≈ 33k/s for the lost node's keys plus 5% of the rest),
  above the ≈ 20k/s the replicas handle, so I need request coalescing per key and load shedding on the feed endpoint
  until the node is replaced; alert on the miss rate, not just on node health."
- *exceeds:* the meets answer, plus "this is why I'd size the DB tier for one cache node's worth of misses, or use
  consistent hashing with replicas so a node loss rebalances only its share — the cost is ≈ 2× cache memory."

**Estimation** (dimension 2)

- *approaching:* "About 100k QPS." (No derivation, no decision.)
- *meets:* "50M DAU × 20 reads ≈ 1B/day ≈ 12k/s average, ≈ 30k/s at a 2.5× peak → at the top of what one primary serves
  even with a cached working set (≈ 10k–50k indexed reads/s), so reads go through a cache and replicas."

**Requirement change "it must now run in two regions"** (dimension 6)

- *approaching:* starts redrawing the whole diagram, or says "we'd replicate the database" and moves on.
- *meets:* "Reads stay local. Writes: either a home region per user (no conflicts, cross-region latency for
  travellers) or multi-leader (local writes, conflict resolution on the like-count, which is a counter, so a CRDT
  works). I'd pick home-region because the write rate is low; the cost is ≈ 70 ms extra for roaming users. The ID
  generator needs a region bit."

## Self-score calibration

After the tutor scores, compare dimension by dimension. A gap of **two levels or more** on any dimension is called
out in the debrief and recorded with `sd record note D3 --tags calibration`. Over-rating hand-wavy answers is weak
spot (d) in another form; under-rating is less common but wastes mocks, because the learner fixes the wrong thing.

## What counts towards the gate

`sd status D3` counts an `hld-45` mock at ≥ meets, once per problem (`mock_distinct_problems` in
`course/course.json`). Four are required. A problem only counts as **unseen** if the learner hasn't designed it in
Track C or an earlier mock, or if it's a variant that changes the design in at least two of: dominant NFR, data
model/partition key, top deep dive (see [problem-bank.md](problem-bank.md)). The tutor checks this before the mock,
not after the score is known.
