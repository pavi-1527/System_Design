# D1 Rubric — 45-minute LLD/OOD mock (`lld-45`)

Score every dimension on the course scale (**below** · **approaching** · **meets** · **exceeds**, see
[MODULE_TEMPLATE.md §6](../../MODULE_TEMPLATE.md#6-design-exercise-design-exercisemd)). Every score needs one line of
evidence: a quote, a timestamp, a line of code, or a probe answer. "Felt solid" isn't evidence.

**Gating dimensions** (marked ★) carry the overall result: 2 Domain model, 3 Interfaces & extensibility, 6 Trade-offs &
probe handling, 7 Communication & time.

## Dimensions

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **1 Requirements & scope** | Starts drawing classes within the first minute; no questions, or questions that don't change anything; no out-of-scope list | Asks some useful questions but leaves scope implicit; axes of change unnamed, so interfaces later look arbitrary | 3–5 requirements and an explicit out-of-scope list by ≈ 0:05; asks the questions whose answers change the model (concurrency, what varies, what must be audited) and says why; names the axes of change | Negotiates scope deliberately ("I'll treat refunds as out of scope unless you want them; they'd add a second state machine"); spots the requirement the interviewer is likely to add and leaves room for it without building it |
| **2 Domain model** ★ | One god struct, or a class per noun with no responsibilities; identity vs value confused; no idea who owns mutable state | Entities mostly right but responsibilities blur (two types change the same state); invariants unstated; ownership only when asked | Every entity has a one-sentence responsibility; values vs entities with identity separated; **one owner per piece of mutable state, said out loud**; invariants stated in words ("cars on the lot + cars out on rental = fleet size"); variation by data modelled as enums/fields, not subtypes | Model chosen against an alternative with a reason ("an explicit `Rental` type rather than state on `Car`, because a car has many rentals over its life and reservations and walk-ins have different lifecycles"); invariants mapped to the methods that could break them |
| **3 Interfaces & extensibility** ★ | Inheritance-style hierarchies or an interface per noun; the requirement change needs a rewrite of core types; or the change isn't attempted | Some interfaces producer-side or too wide; the change lands but edits several existing types | API written as Go signatures by ≈ 0:15; 1–3 small consumer-side interfaces, each tied to a named axis; errors are values callers can match; **the requirement change lands as an addition** (new type/function behind an existing seam, ≲ 30 changed lines), or, if it hits an unopened axis, the candidate names the smallest edit and the new seam | Explains which seams were *not* added and why (cost of each); the API shape makes misuse hard (constructors enforce invariants, no exported mutable fields); predicts the next likely change and shows it would also be an addition |
| **4 Concurrency & correctness** | No answer to "what if two callers do this at once?"; or a lock-everything/lock-nothing answer with no reasoning; core logic wrong | Names a mutex but not what it guards or the critical section; a race or lost update survives in the hard method; partial-failure path ignored | States the concurrency model (one mutex per aggregate, single-writer goroutine, per-key locks) and why; the hard method is correct, including one edge case; **partial failure** in the hard method has a defined outcome (e.g. card pre-authorisation times out after the keys are handed over → rental stays open, flagged for reconciliation) | Names the happens-before edge or the invariant a stress test would check; discusses the next step (sharding, owner goroutine) with the condition that would justify it; no user callback or I/O under a lock, and says so |
| **5 Code & testability** | Nothing coded by 0:30; or code that couldn't compile (wrong types, invented syntax); `time.Now()`/randomness hard-wired | A skeleton exists but the hard method is missing or mostly comments; no test; dependencies hard to fake | Idiomatic Go skeleton plus the hard method and one meaningful test by ≈ 0:30; clock, IDs and external dependencies injected; errors wrapped/matched with `errors.Is`/`As`; names say what things do | The test is table-driven or targets the invariant directly; a fake for the external dependency in a few lines; code reads like production (doc comments state the contract) |
| **6 Trade-offs & probe handling** ★ | Answers with brands or slogans ("it's more flexible", "it's a best practice"); defensive or vague; can't name a cost | Gives a reason but no cost, or a cost but no flip condition; needs heavy prompting; probe answers drift for minutes | Probe answers in ≲ 2 minutes with **claim → why here → cost → when it flips**; concrete mechanisms (which lock, which state, which error); admits limits honestly ("I haven't handled damaged returns; I'd add an `Inspection` state") | Anticipates probes before they're asked; turns a probe into a sharper design ("good point: that means `Return` must be idempotent per rental id"); quantifies when it matters (objects in memory, ops/s per lock) |
| **7 Communication & time** ★ | No signposting; a phase overruns by > 8 min; nothing to show at 0:45; ignores the interviewer's steers | Finishes but one checkpoint slips by 4–8 min, or the candidate has to be nudged at a checkpoint; thinks silently for long stretches | Announces phases; hits the 0:15 / 0:30 / 0:37 checkpoints within ≈ 2–3 min; follows a steer immediately and returns to the plan; ends with "what I'd do next" and what's not covered; stops at 0:45 | All checkpoints on time *and* says what was deliberately compressed and why; uses the parking-lot list; the interviewer never has to manage the clock |

## Overall rule

- **meets** = *meets* or better on every gating dimension (2, 3, 6, 7) **and** no dimension at *below*.
- **exceeds** = *meets* or better everywhere **and** *exceeds* on at least three dimensions, including at least two
  gating ones.
- **Otherwise** overall = the lowest gating-dimension level, and never above *approaching* if any dimension is at
  *below*.

Examples: gating 2/3/6/7 = meets/meets/approaching/meets, everything else meets → **approaching** (lowest gating).
All gating meets but *Concurrency & correctness* at below → **approaching** (a below anywhere caps it). Exceeds on 1,
3, 6, meets elsewhere → **exceeds** (three exceeds, two of them gating).

Only *meets* or *exceeds* counts toward the gate, and only once per distinct problem.

## Calibration anchors

Use these to keep scores consistent across mocks and to check the learner's self-score.

- **The requirement change is the single best signal for dimension 3.** Count the existing types edited to absorb it.
  0–1 with a new type behind a seam ≈ meets; 2–3 ≈ approaching; a rewrite or "I'd need to restructure" with no
  smallest edit ≈ below.
- **"Who owns this state?"** must get a one-sentence answer for dimension 2 to reach meets. "The service, under
  `mu`" is enough; "it's thread-safe" isn't.
- **A probe answer without a cost is approaching**, however fluent. A probe answer that attacks the alternative
  instead of naming when it wins is approaching too (see the trade-off sentence in
  [docs/INTERVIEW_FRAMEWORK.md](../../docs/INTERVIEW_FRAMEWORK.md#stating-a-trade-off-use-this-shape-every-time)).
- **Over-engineering is a dimension 3 problem, not a bonus.** Five interfaces in a 45-minute design with one stated
  axis of change is approaching even if each is well-shaped.
- **Self-score calibration.** Off by more than one level on any dimension: name it in the debrief and in the
  `sd record note`. Over-rating fluent but uncosted answers is the usual direction (weak spot (d)).
- **Timing evidence comes from the log**, written during the mock, not reconstructed afterwards.
