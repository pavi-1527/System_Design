# C11 — Case study: video streaming

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 9 h (2 units) — CORE 7 h, STRETCH 2 h |
| Prerequisites | A1, A6, C10 (gates passed) |
| Labs | `labs/c11/` — `transcode` (the transcoding pipeline as a DAG: bounded workers, retries, timeouts, idempotent resume, leak-free cancellation; gated) and `abr` (adaptive bitrate selection; STRETCH, not gated) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A learning platform puts its lectures online the obvious way: instructors upload an MP4, a server stores it, and
the page embeds `<video src="/files/lecture-17.mp4">`. At 2,000 students it's fine. At 400,000 students in 30
countries, students on 1.5 Mbps mobile links stall every minute on an 8 Mbps file; an instructor's 9 GB workshop
upload fails at 87% and restarts from zero; a "convert to 480p" step inside the upload request takes 40 minutes and
times out; one viral course saturates a 10 Gbps NIC; and the month's bill shows ≈ 900 TB of egress at cloud list
prices, more than everything else combined.

Scale it to a real video platform and the shape becomes clear. The bytes going *out* outnumber the bytes coming in by
≈ 200×: 50 M daily viewers at ≈ 3 Mbps is ≈ 10 Tbps at the evening peak and ≈ 45 PB a day, so the CDN's hit ratio and
the price per GB decide more than any database choice. Every upload becomes hundreds or thousands of files (every
segment of every quality), produced by a pipeline that must be parallel to publish in minutes and resumable so that a
crash at 98% doesn't redo ten core-hours of work. And playback isn't a download: the player picks a quality every
few seconds from what its network allows, which only works if the files were cut and labelled for it.

Interviewers like this problem because the weak answer ("upload service, transcoding service, blob store, CDN") is
easy and says nothing. The senior signal is following the bytes and the money: an egress estimate in Tbps in the
first ten minutes, a transcoding DAG with idempotent artifacts and honest failure handling, a ladder and a
client-side ABR policy, and a CDN and storage plan priced against the long tail.

## Outcomes

- **Scope** a video platform in six minutes: the clarifying questions whose answers change the design (VOD or live,
  user-generated or curated, upload-to-watch ratio, devices, time to playable, protection, viewer geography) and
  non-functional requirements with numbers. — CORE
- **Estimate** concurrent viewers, peak egress in Tbps, egress bytes and cost per month, segment request rate, ingest,
  storage per minute across a bitrate ladder and per year, and transcode compute, and tie each number to a
  decision. — CORE
- **Design** the API (idempotent resumable upload, playback returning a signed manifest URL, watch progress, batched
  playback events) and a data model that separates metadata from blobs, with deterministic, versioned, immutable
  object keys and an artifacts record as the processing truth. — CORE
- **Draw** the high-level design with the upload, processing and playback paths traced, and justify every box by a
  requirement or a number. — CORE
- **Design** the transcoding pipeline as a dependency graph: segment-level parallelism, dependency-driven scheduling
  with one owner, bounded workers, fairness across jobs and a fast path to playable. — CORE
- **Make** the pipeline survive failure: classify failures, bounded retries with backoff and jitter, per-attempt
  timeouts, poison inputs to dead letters, idempotent resume from finished artifacts, and cancellation that leaks
  nothing. — CORE
- **Implement** the pipeline (`labs/c11/transcode`): the DAG, a bounded worker pool, retries with backoff,
  per-attempt timeouts, panic containment, resume from a store of artifacts, progress and leak-free cancellation,
  passing under `-race`. — CORE
- **Explain** packaging (HLS, DASH, CMAF, segment duration, aligned keyframes), bitrate-ladder design, and how a
  player adapts quality per segment (safety factor, immediate down-switch, buffer-gated up-switch, panic rule). —
  CORE
- **Reason** about CDN economics and storage: egress dominates, byte hit ratio, cache keys and TTLs, shield tiers and
  request collapsing, head versus long tail, multi-CDN and ISP-embedded caches, storage tiers by last access and lazy
  rungs for the tail. — CORE
- **Design** view counts and watch progress at scale: an event log with windowed aggregation, hot-key handling, a fast
  approximate display versus an exact batch recount, and last-writer-wins progress partitioned by user. — CORE
- **Implement** an adaptive bitrate policy (`labs/c11/abr`) and explain per-title encoding and when it pays back. —
  STRETCH
- **Adapt** the design for live streaming: a latency budget, real-time transcoding, low-latency HLS/DASH, playlist
  caching and collapsing, and redundant ingest. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, high-level design](unit-1-guided-design.md) | 3.5 | CORE (3) / STRETCH (0.5) | — (conceptual; the estimate sheet and the diagram are the work) |
| 2 | [Deep dives: the transcoding DAG and its failures, packaging and ABR, CDN economics and storage tiers, view counts, live; common mistakes](unit-2-deep-dives.md) | 5.5 | CORE (4, incl. ≈ 1 h gate work) / STRETCH (1.5) | `labs/c11/transcode` (CORE), `labs/c11/abr` (STRETCH) |

Total: 9 h, CORE 7 h, STRETCH 2 h. The module leans on A1 (CDNs, the request path), A6 (queues, poison messages,
orchestration) and C10 (resumable chunked upload into a blob store, metadata separate from blobs) and spends its time
on what is specific to video: the processing DAG, the ladder and the player, and the economics of delivery.

**Unit 1 (3.5 h)**
- Session 1 (1.5 h, CORE): warm-up; §1 requirements (write your eight questions and the YouTube-vs-Netflix table
  before the reveal); §2 the estimate sheet by hand before the worked version, then `sd estimate -kind bandwidth -n 3`
  and `sd estimate -kind storage -n 3`.
- Session 2 (1.5 h CORE + 0.5 h STRETCH): §3 API and data model (commit to the object-key scheme and partition keys
  first); §4 the high-level design with all three paths traced and the three break-it traces. STRETCH: the
  cost-per-hour-watched arithmetic at the end of §2 and DRM and licence delivery in §3.

**Unit 2 (5.5 h)**
- Session 3 (1.5 h, CORE): warm-up; §5 the transcoding DAG and §6 its failure handling; `labs/c11/transcode` parts
  1–4 (`Steps`, a serial `Run`, the worker pool, retries/timeouts/panics, resume/cancellation/progress); `sd lab C11`.
- Session 4 (1 h, CORE): §7 packaging, the ladder and ABR (the core parts) and §8 CDN economics and storage tiers;
  the §5–§6 break-it exercises.
- Session 5 (1.5 h, CORE incl. ≈ 1 h gate work): §9 view counts and progress; Common mistakes; first `sd quiz C11`
  attempt; the [design exercise](design-exercise.md) (45 min, solo, timed) and the [teach-back](teachback.md)
  write-up, in the same sitting or the next day. Second `sd quiz C11` attempt on a later day.
- STRETCH (1.5 h): per-title encoding and `labs/c11/abr` with its hysteresis simulation in §7 (≈ 0.75 h); §10 live
  streaming (≈ 0.75 h).

## How this connects to your banking work

- **A transcoding job is an end-of-day batch run.** Splitting a settlement or statement file into independent chunks,
  processing them in parallel with bounded workers, recording each chunk's output under a deterministic key and
  resuming a failed run from what's already done is exactly Unit 2 §5–§6. The rule "output durable, then recorded,
  then dependents released" is the same one that keeps a re-run batch from posting a chunk twice; the lab's
  `Result.Skipped` is your "already processed, skipping" log line.
- **A corrupt source file is a malformed vendor file.** A partner's batch with one record that crashes the parser is
  a poison message: classify it as permanent, quarantine it with the error and the attempt history, tell the sender,
  and keep the rest flowing, instead of retrying the whole file every five minutes for a week (Unit 2 §6,
  [A6 Unit 2 §3](../A6-async/unit-2-outbox-consumers.md#3-poison-messages-dlqs-and-replay)).
- **Multi-CDN is vendor failover with a cold-start cost.** Routing playback sessions across two CDNs by health and
  price is the same decision as routing payments across two processors; the warm-cache caveat (a cold secondary
  sends its whole miss stream to your origin) is the video version of a secondary processor that has never seen your
  volume (Unit 2 §8, and the vendor adapter in
  [A7 Unit 3 §5](../A7-reliability/unit-3-isolation-degradation-dr.md#5-capstone-the-resilient-vendor-adapter)).
- **Cost accounting per unit of use.** "Egress per hour watched" and "storage per uploaded minute" are the same
  discipline as vendor fees per transaction per credit union: once cost is a per-unit metric, a design change (a
  lifecycle rule, a cheaper path) shows up as a number finance can read. Per-tenant attribution of egress is how a
  multi-tenant video feature would be billed back.
- **Long retention, rare reads.** Statements and audit evidence kept for years but rarely opened are the long tail of
  §8: tier by last access, keep the immutable original in an archive tier, and never put anything a customer can
  request instantly into a tier that takes hours to restore.

## Mastery gate checklist

- [ ] `sd quiz C11` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C11` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C11` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C11 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C11 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C11 is one of the core case studies that D4 (the full senior loop) requires. Expect "design YouTube", "design
Netflix" or a variant (a short-video app, a course platform, a live-streaming service, a podcast host) in the D3 and
D4 mocks, where the deep dives an interviewer picks are almost always the processing pipeline and the delivery
economics. E3 (the job pipeline) builds the queue, retry, backoff and dead-letter machinery of Unit 2 §6 as a real
multi-process service. Before moving on, run `sd review` and clear any C11 questions it surfaces, especially tags
`estimation`, `bandwidth`, `cdn`, `idempotency` and `retries`; and re-read both units' wrap-up tables the day before
your next HLD mock.
