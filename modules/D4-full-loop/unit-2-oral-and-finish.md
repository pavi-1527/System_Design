# D4 Unit 2 — The component oral and the finish line

> Time: ~12 h · CORE: §1–§2 (≈ 1.5 h reading and oral drills) + loops 2 and 3 (≈ 4.5 h each, including breaks and
> debrief) + reviewing loop 2's log between them (≈ 1.5 h) · Lab: none · Kit: [component-oral.md](component-oral.md),
> [rubric.md](rubric.md)

The last twenty minutes of each loop are the component oral: four or five building blocks, four questions each, no
whole-system diagrams to hide behind. This unit prepares it, and then steps back to the course's definition of
done: what the three loops prove, and what they don't.

## Before you start

Do loop 1 first ([Unit 1](unit-1-loop-day.md)) and read its log. Warm-up:

1. The four questions asked about every building block in the oral.
2. Name the 13 building blocks in the definition of done.
3. From loop 1's log: which oral block was weakest, and which must-hit did you miss?

<details data-kind="answer">
<summary>Warm-up answers</summary>

1. Why does it exist? How does it work inside? How does it fail? When not to use it?
2. DNS, load balancer, CDN, cache, relational database, LSM/KV store, search index, object store, queue, log,
   coordination/consensus service, rate limiter, circuit breaker.
3. From the log. If a block was at revise, it's asked first in loop 2.

</details>

**Session plan (≈ 12 h):** Session 1 (1.5 h): §1 with two oral drills, §2 with your DoD tracker. Session 2 (≈ 4.5 h,
one sitting): loop 2. Session 3 (≈ 1.5 h, another day): loop 2's log, revisits, the one habit for loop 3. Session 4
(≈ 4.5 h, one sitting): loop 3. A loop below meets is redone on new problems; retakes come on top of these hours.

---

## §1 First principles under cross-examination

### The failure story

Loop 1's oral. Block: log. The candidate has put Kafka in eight case-study designs. "Why does it exist?" — "For
high-throughput messaging." "How does it work inside?" — "Producers write to topics, consumers read them." "How
does it fail?" — "It's replicated, so it's pretty reliable." "When wouldn't you use it?" — "When you don't need
scale?" Four answers, each one true at the level of a product page, none of them a mechanism. Revise.

The same candidate, asked in an HLD round why they chose a log over a queue, gave a good answer, because the design
on the board anchored it. Without a design to point at, the knowledge turned out to be vocabulary. That's exactly
what item 5 of the definition of done is there to catch.

### Predict

<!-- predict id="D4-U2-P01" -->
**Predict.** Answer the four questions for the **log** (append-only, partitioned, consumer offsets), in at most
three sentences each, without notes. Then answer the follow-up: "Why does adding partitions to a topic break per-key
ordering for a while?"
<!-- /predict -->

### Reveal

<details data-kind="reveal" data-for="D4-U2-P01">
<summary>Reveal</summary>

**Why it exists:** a durable, ordered, replayable record that many independent consumers read at their own pace.
A queue hands each message to one consumer and forgets it; a log keeps it, so a new consumer, or a consumer
recovering from a bug, can re-read history.

**How it works inside:** each topic is split into append-only partitions; a message's key picks its partition;
consumers track their own offsets per partition; a consumer group splits partitions among its members so each
partition has one reader in the group; partitions are replicated, and messages are deleted by retention (time or
size), not by consumption.

**How it fails:** ordering exists only within a partition; a skewed key makes one partition hot while others idle;
consumer lag grows when consumers are slower than producers, and if lag exceeds retention, data is lost to that
consumer; losing the replication quorum for a partition stops writes to it.

**When not to use it:** when you need per-message routing, priorities, delayed delivery or individual
acknowledgements (a queue fits better), and at low volumes where an outbox table in the database you already run is
simpler and avoids operating another system.

**Follow-up:** the partition is chosen by hashing the key modulo the partition count. Change the count, and many keys
map to a different partition; messages already in the old partition for that key are still being consumed while new
ones land in the new partition, so a consumer can see them out of order until the old partition drains. The fixes are
to over-provision partitions up front, or drain/pause producers for affected keys during a resize.

Compare this with the failure story: every answer here names a mechanism (offsets, partitions, retention, hashing).
That's the difference between pass and revise.

</details>

### How it actually works

**The four questions are a dependency chain too.** You can't explain how something fails without knowing how it
works inside, and you can't say when not to use it without knowing both why it exists and how it fails. Answer them
in order, and let each answer use the previous one.

| Question | What the tutor listens for | Common weak answer |
|---|---|---|
| Why does it exist? | The problem it solves, stated as a constraint ("one primary can't take 50k writes/s") | A feature list ("it's fast and scalable") |
| How does it work inside? | Two or three internal mechanisms by name, and how data moves | The external API only |
| How does it fail? | Specific failure modes with their trigger, and what the user sees | "It's replicated" |
| When not to use it? | A condition where an alternative wins, and the alternative | "When you don't need scale" |

**Why an oral, after teach-backs.** The Track A teach-backs were written, untimed, one concept at a time. The oral
checks retrieval under pressure, across blocks, at the end of a long day: the conditions in which vocabulary and
understanding separate. A block passes with at most one hint across its four questions, and the oral passes only
when every block asked passes (see [rubric.md](rubric.md)).

**How to prepare without memorising.** For each block, rebuild the answer from the failure story that motivated it
in Track A: a cache exists because a hot set dominates reads; its internals follow from where it sits and how it's
filled; its failure modes follow from what happens when it's empty, stale or hot; and "when not" follows from a
workload with no hot set. If you can rebuild the chain, you don't need to recite it.

### Build it

No Go lab. **Oral drills:** twice, on different days, pick four blocks from [component-oral.md](component-oral.md)
at random (not the ones you feel best about), set a 16-minute timer, and answer the four questions for each out loud
or in writing without notes. Then mark each answer against the must-hit table.

Acceptance: by the second drill, all four blocks pass with no hints. Blocks that fail go into the next drill.

### Break it

Answer one block's four questions in reverse order (when not → fails → inside → why), then in the normal order on a
different block of similar difficulty. Compare how often the reverse-order answers fall back to generic statements.

<details data-kind="answer">
<summary>What you'll likely see</summary>

"When not to use it" asked first tends to come out generic ("when you don't need it"), because there's no mechanism
on the table yet to reason from. In the normal order, the failure modes you just described make the "when not"
answer specific. The order isn't a ritual: each question supplies material for the next one.

</details>

### In the wild

- **Oral examinations** (doctoral qualifying exams, medical board orals) are used precisely because follow-up
  questions expose whether an answer is understood or recited.
- **Design-review questions** at many companies take the same shape: "why do we need this?", "how does it work?",
  "what happens when it fails?", "what else did you consider?". The oral is that review for the building blocks
  every design uses.

### Trade-offs

| Approach | Wins when | Loses when | When the answer flips |
|---|---|---|---|
| Oral with strict pass (every block) | Final check of first principles; definition-of-done evidence | One unlucky block fails a strong loop | That's why there are three loops and a rotation: a revise block comes back first next time. |
| Written teach-back | Learning a concept in depth with time to think | Measuring retrieval under pressure | Teach-backs are for learning (Track A); the oral is for checking (D4). |
| Ask blocks the HLD round already covered | — | Double-counts one area, leaves others untested | Swap that block for another and move it to the next loop. |

### Senior bar

- **Failure modes:** product-page answers; "it's replicated" for failure; "when you don't need scale" for "when
  not"; drawing a whole system instead of the component's internals.
- **Numbers:** a passing answer usually contains one number (a throughput ceiling, a latency, a cost) that shows
  where the component's limits are, consistent with [docs/NUMBERS.md](../../docs/NUMBERS.md).
- **Exceeds-level signal:** connecting a block's failure mode to a design decision you'd make because of it, e.g.
  "because consensus writes are a quorum round trip, I keep it off the data path and use it for leases only".

**Interviewer follow-up probes:**

<details data-kind="answer">
<summary>Probe: "Why not use the coordination service as your database? It's consistent and replicated."</summary>

"Because every write is a quorum round trip through one leader, so throughput is far below a real database's and
latency is several network round trips; it's designed to hold small amounts of critical metadata (leases, config,
membership), with watches for change notification. Putting application data in it would make the system that
everything depends on for leader election the slowest and busiest part of the stack. I'd flip only for tiny,
critical, rarely-written state." Mechanism (quorum, single leader), consequence, and the condition where it's right.

</details>

<details data-kind="answer">
<summary>Probe: "When would you not put a CDN in front of a public API?"</summary>

"When responses are per-user and uncacheable, the CDN adds a hop and a cost line without offloading anything; I'd
still consider it for TLS termination near users and DDoS absorption, but that's a different reason. If even a
small part of the response is shared (a catalogue, public metadata), I'd split it into a cacheable endpoint and put
only that behind the CDN." Shows the candidate knows *why* a CDN helps (shared bytes close to users) and applies it.

</details>

### Check yourself

1. Why answer the four questions in order?
2. What's the pass rule for a block, and for the oral?
3. What should a "how does it fail?" answer contain?

<details data-kind="answer">
<summary>Answers</summary>

1. Each answer supplies material for the next: failure modes come from the internals, and "when not" comes from the
   purpose and the failure modes.
2. A block passes when all four questions are correct with at most one hint across them. The oral passes when every
   block asked passes.
3. Specific failure modes with their trigger and what users see, not "it's replicated".

</details>

---

## §2 The finish line: reading your evidence

### The failure story

Loop 3 passes: meets, meets, exceeds, oral pass. D4's gate shows 3/3. The learner declares the course done. Then
`sd status` shows A6 in *needs refresh* (review accuracy fell to 62% over the last ten cards), `sd estimate` mastery
is at 75% over the last 20, and E2's failure-injection milestone was never finished. Three of the seven
definition-of-done items aren't met, and D4 can't see any of them. Passing the final exam isn't the same as finishing
the course.

### Predict

<!-- predict id="D4-U2-P02" -->
**Predict.** For each of the seven items of the [course definition of done](../../SYLLABUS.md#course-level-definition-of-done),
write which evidence proves it: a D4 loop, another gate, `sd status`, `sd progress`, or something else. Which items
can three passing loops prove on their own, and which can't they prove at all?
<!-- /predict -->

### Reveal

<details data-kind="reveal" data-for="D4-U2-P02">
<summary>Reveal</summary>

| DoD item | Evidence | Can D4 prove it? |
|---|---|---|
| 1. Every CORE gate passed, nothing in needs refresh | `sd status` (gates and refresh flags) | No: D4 unlocking proves many gates, but not E1–E3 or current refresh state |
| 2. HLD cold, ≥ meets on every dimension, under probes | D3 mocks and the HLD rounds of D4 loops (every dimension ≥ meets) | Yes, together with D3 |
| 3. Machine coding: B3 problems in time box + two unseen at ≥ meets | `sd lab B3` and B3's design exercise; D2 mocks and D4 MC rounds | Partly: the unseen problems, yes; the B3 bank comes from B3's gate |
| 4. Estimation: ≥ 80% of last 20 within ±25%, all kinds, and mock estimates within tolerance | `sd status` (MASTERED) for the drill; the D3/D4 logs for the mocks | Partly: the mock part, yes; the drill part only `sd estimate` shows |
| 5. First principles for 13 building blocks | Track A teach-backs and the D4 orals covering all 13 | Yes, over three loops (the rotation covers all 13) |
| 6. Track E complete | E1–E3 gates and their design docs and reflections | No |
| 7. Retention: review accuracy ≥ 85% over the last 50 cards | `sd progress` | No |

Three passing loops can fully prove items 2 and 5, and contribute to 3 and 4. They can't say anything about 1, 6 or
7. The tempting wrong answer is "D4 is the final exam, so passing it means done": it's the final *interview* exam,
and the course's goal includes building systems and retaining what you learned.

</details>

### How it actually works

**Keep a definition-of-done tracker** from loop 1 onwards: one table, seven rows, each with its evidence source and
its current state. The loop log has a DoD section for the items a loop can evidence; the rest come from the tools:

```text
sd status                 # gates, needs-refresh flags, estimation mastery
sd progress               # review accuracy, weak tags, mock history
sd status D4              # 3/3 loop mocks at ≥ meets on distinct problems
```

**Close the gaps in this order.** Needs-refresh modules first (they're cheap: wrap-ups and a full quiz), then
estimation reps if mastery has slipped, then any unfinished Track E milestones (they take the longest, so schedule
them early rather than discovering them at the end), and review accuracy last, because it rises with everything else.

**What "done" means after done.** Retention decays. After the course, a light maintenance routine keeps it: `sd
review` when cards are due, a few `sd estimate` problems a week, and one probe sprint or mock a month on an unseen
problem, ideally in the format of your next real interview.

### Build it

No Go lab. Build your **DoD tracker** now (a table in your notes, or a section at the top of your last loop log):
seven rows, the evidence source for each, its state today (met / not yet / partly), and the next action for each
unmet item with an estimate in hours.

Acceptance: every row has an evidence source that a third party could check (`sd` output, a log file, a gate).

### Break it

Ask the tutor to audit your tracker as a skeptical reviewer: for each row marked "met", they check the evidence
directly (`sd status`, `sd progress`, the logs) without reading your notes.

<details data-kind="answer">
<summary>What you'll likely see</summary>

Usually at least one "met" turns out to be "partly": a needs-refresh flag set since you last looked, estimation
mastery that slipped below 80% with recent misses, or an oral rotation that hasn't yet covered all 13 blocks. That's
the reason the definition of done says "all of these hold *at the same time*": the evidence has to be current.

</details>

### In the wild

- **Exit criteria** in software releases (all tests green, no open severity-1 bugs, performance targets met) work the
  same way: a release isn't done because the last task was finished, but because every criterion holds at once.
- **Spaced repetition** research (going back to Ebbinghaus's forgetting-curve experiments) is why the course keeps
  reviewing after gates pass: without retrieval practice, retention decays predictably.

### Trade-offs

| Approach | Wins when | Loses when | When the answer flips |
|---|---|---|---|
| Treat D4 as the last step | You've kept every other DoD item current all along | Anything has decayed or was skipped | Check the tracker before loop 3, not after. |
| Interleave E-track work with loops | Track E milestones are still open when D4 unlocks | It splits attention during loops | If an E milestone is the only DoD gap left, finish it before loop 3. |
| Keep doing mocks after the course | A real interview is coming | No interview soon: diminishing returns | Switch to a monthly maintenance mock and `sd review`. |

### Senior bar

- **Failure modes:** declaring done on D4 alone; letting needs-refresh flags sit; treating Track E as optional because
  the interview rounds passed.
- **Numbers:** the definition of done is seven yes/no items; at the end, all seven are "yes" on the same day.
- **Org view:** the definition of done is a contract you wrote with yourself at the start. Holding to it is the same
  discipline as holding a launch to its exit criteria under schedule pressure.

**Interviewer follow-up probes** (closing debrief):

<details data-kind="answer">
<summary>Probe: "Which item of the definition of done would you be least confident defending to a skeptic today?"</summary>

A good answer names one item and its weakest evidence: "Retention: my last-50 review accuracy is 86%, just over the
bar, and it was 79% two weeks ago, so I'd want another two weeks of reviews before calling it stable." It shows the
learner reads their own evidence critically.

</details>

<details data-kind="answer">
<summary>Probe: "You have a real loop in three weeks. How do you spend the time?"</summary>

"One loop in that company's format and order (adjusting round lengths if theirs differ), daily `sd review`, a
probe sprint every other day on the case studies closest to their product, a few `sd estimate` problems daily, and
no new material." Focused, format-specific, and retrieval-based rather than new content.

</details>

### Check yourself

1. Which definition-of-done items can D4 loops prove fully?
2. Which commands show the evidence for items 1, 4 (drill part) and 7?
3. In what order should you close remaining gaps, and why?

<details data-kind="answer">
<summary>Answers</summary>

1. Item 2 (HLD cold, with D3) and item 5 (first principles, across three loops' orals). They contribute to 3 and 4.
2. `sd status` for gates, refresh flags and estimation mastery; `sd progress` for review accuracy.
3. Needs-refresh first (cheap), then estimation reps, then Track E milestones (long, so schedule early), then review
   accuracy (it rises with the rest).

</details>

---

## Unit wrap-up

| Situation | Do this | Flip to this when… |
|---|---|---|
| An oral block | Why → inside → fails → when not, each with a mechanism | — (always in that order) |
| Preparing the oral | Rebuild each block from its Track A failure story; random-block drills | A block fails a drill → it's first in the next drill |
| After each loop | Update the DoD tracker from the loop log and `sd status` / `sd progress` | — |
| D4 gate passes | Check all seven DoD items hold at once | Any item unmet → close it before calling the course done |
| After the course | `sd review`, weekly `sd estimate`, a monthly mock | A real interview is coming → one loop in that company's format |

**Next:** loops 2 and 3. When `sd status D4` shows 3/3 and your tracker shows seven "met" rows on the same day,
you've finished the course.
