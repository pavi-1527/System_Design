# C10 — Case study: file storage and sync

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 9 h (2 units) — CORE 7 h, STRETCH 2 h |
| Prerequisites | A4, A6 (gates passed) |
| Labs | `labs/c10/` — `chunker` (content-defined chunking, content-addressed dedupe store with refcounts and verification, manifests, resumable upload sessions, exactly-once commit, expiry and GC; gated), `syncvv` (version vectors and conflicted copies; STRETCH, not gated) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A 40-person startup ships "TeamDrive" in a quarter: file bytes in a Postgres column, a `POST /files` per save
through the API servers, clients polling `modified_after=<last poll>` every 30 seconds, and the later `modified_at`
winning when two versions meet. At 10,000 users nobody notices. At 2 M users and 3 M devices the database holds
40 TB of mostly file bytes and a replica rebuild takes two days; a sales engineer tries to upload a 4 GB demo eleven
times from hotel Wi-Fi, each attempt dying past 90% and restarting from zero; a designer's one-slide edit re-uploads
a 300 MB deck ten times a day; polling is ≈ 100,000 requests a second, 99% of them answered "nothing new"; and a
laptop whose clock runs four minutes slow silently loses a contract revision to an older copy.

Nothing in that story needs exotic technology. It needs four decisions nobody made: file bytes and metadata are
different kinds of data with different stores, joined by one rule (bytes first, metadata last); files are chunks
named by their content, so uploads resume, edits send only what changed and identical data is stored once; devices
follow an ordered change journal by cursor rather than comparing timestamps; and concurrent edits are detected and
kept, never overwritten. Underneath sits the question that sets the bill: how hundreds of petabytes survive disks,
racks and regions without tripling the hardware.

Interviewers like "design Dropbox" because candidates reach for "S3 plus a database" and stop. The senior signal is
specific: the metadata/blob split with a commit rule, content-defined chunking with the dedupe privacy caveat,
resumable sessions with presigned uploads and orphan GC, a cursor-based sync protocol with notifications as hints,
version vectors and conflicted copies, and erasure coding with tiering, all with numbers.

## Outcomes

- **Scope** a file storage and sync service in six minutes: the clarifying questions whose answers change the
  design (sync versus drive, file sizes and networks, edit patterns, sharing, offline edits, sync latency, versions,
  durability) and non-functional requirements with numbers. — CORE
- **Estimate** logical, unique and raw storage (replication versus erasure coding), commit rate, upload bandwidth
  with and without delta sync, notification fan-out and connections, and metadata size and shard count, and tie
  each number to a decision. — CORE
- **Design** the API and data model: upload sessions with manifests and missing chunks, presigned direct-to-blob
  uploads, a conditional idempotent commit, a cursor-based change feed; a metadata store sharded by namespace with
  stable file ids, versions, a journal and shared namespaces, and a content-addressed chunk index. — CORE
- **Draw** the high-level design with the upload, download and sync paths traced, notifications published through
  an outbox as hints, and every box justified by a requirement or a number. — CORE
- **Choose** between fixed-size and content-defined chunking and a chunk size from index, manifest and bandwidth
  arithmetic, and decide the dedupe scope given the existence side channel and hash-as-capability risks. — CORE
- **Design** resumable uploads and the commit protocol: durable sessions, idempotent chunk uploads and commits,
  bytes-first commits, orphan GC with pins and grace periods, and hash verification on write, on read and by a
  scrubber. — CORE
- **Implement** the storage core (`labs/c10/chunker`): fixed and content-defined chunking with a gear hash, a
  deduplicating, reference-counted, verifying chunk store, manifests, and resumable upload sessions that accept
  chunks in any order and in parallel, commit exactly once, expire and are garbage collected, passing under
  `-race`. — CORE
- **Design** the sync protocol (per-namespace journal and cursors, long-poll or push hints, crash-safe apply,
  chunk-level delta download) and explain how concurrent edits are detected with base versions or version vectors
  and resolved with conflicted copies, and when last-writer-wins is acceptable. — CORE
- **Choose** between replication and erasure coding and place data across racks and zones, and protect hot shared
  folders and public links with edge fan-out, jitter, caching and a CDN. — CORE
- **Implement** version vectors and conflict resolution (`labs/c10/syncvv`): compare, merge and decide take, keep
  or conflicted copy. — STRETCH
- **Design** cold tiering, retention and legal holds, and reason about erasure-coding repair traffic and cross-region
  layouts with numbers. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, high-level design](unit-1-guided-design.md) | 3.5 | CORE (3) / STRETCH (0.5) | — (conceptual; the estimate sheet and the diagram are the work) |
| 2 | [Deep dives: chunking and dedupe; resumable uploads, integrity and commits; sync and conflicts; durability, hot shares and tiering; common mistakes](unit-2-deep-dives.md) | 5.5 | CORE (4, incl. ≈ 1 h gate work) / STRETCH (1.5) | `labs/c10/chunker` (CORE), `labs/c10/syncvv` (STRETCH) |

Total: 9 h, CORE 7 h, STRETCH 2 h. The module leans on A4 (replication, partitioning, hot keys) and A6 (outbox,
fan-out, idempotent consumers) and spends its time on the storage and sync mechanisms specific to files.

**Unit 1 (3.5 h)**
- Session 1 (1.5 h, CORE): warm-up; §1 requirements (write your eight questions and the "different system" list
  before the reveal); §2 the estimate sheet by hand before the worked version, then `sd estimate -kind storage -n 3`
  and `sd estimate -kind bandwidth -n 3`.
- Session 2 (1.5 h CORE + 0.5 h STRETCH): §3 API and data model (commit to the upload calls and the partition keys
  first); §4 the high-level design with the laptop-to-phone trace and the three break-it traces. STRETCH: the
  tiering arithmetic in §2, the end-to-end encryption note in §3 and the cross-region note in §4.

**Unit 2 (5.5 h)**
- Session 3 (1.5 h, CORE): warm-up; §5 chunking and dedupe (do the insertion arithmetic yourself); lab parts 1–2
  (`FixedChunks`, `CDCChunks`, `BuildManifest`, the `Store`); the `DefaultCDC` break-it; read §6 (resumable uploads,
  integrity and the commit protocol) up to its Build it.
- Session 4 (1.5 h, CORE): lab part 3 (`Uploader`, `GC`); `sd lab C10`; break-its 1–4 of §6; §7 (sync protocol and
  conflicts) and the CORE part of §8 (replication versus erasure coding, placement, hot shares); Common mistakes;
  first `sd quiz C10` attempt.
- Session 5 (1 h, CORE gate work): the [design exercise](design-exercise.md) (45 min, solo, timed) and the
  [teach-back](teachback.md) write-up, in the same sitting or the next day. Second `sd quiz C10` attempt on a later
  day.
- STRETCH (1.5 h): `labs/c10/syncvv` and its break-it (≈ 45 min); the repair arithmetic, cross-region and cold
  tiering parts of §8 and the replicated-store extension (≈ 45 min).

## How this connects to your banking work

- **Statements, cheque images and KYC documents are this system's blobs.** A member uploading a passport photo or a
  45 MB mortgage pack from a phone on a train needs the same resumable session, presigned upload and idempotent
  commit as Unit 1 §3; the session id as the commit's idempotency key is the
  [idempotency-key pattern](../A1-networking-api/unit-3-idempotency-auth.md#2-idempotency-keys-end-to-end) you already
  use for payment initiation, and "bytes first, metadata last" is the same discipline as never posting a ledger entry
  before the funds movement it describes is confirmed.
- **Dedupe scope is a tenancy decision.** One credit union's statements must never deduplicate against another's:
  a global "already have it" check is a cross-tenant existence oracle (Unit 2 §5), and per-tenant envelope keys
  ([A9 Unit 2 §3](../A9-security-multitenancy/unit-2-data-protection-audit.md#3-envelope-encryption-and-key-management))
  make cross-tenant dedupe impossible anyway. Within a tenant, the same disclosure PDF attached to a million accounts
  dedupes to one copy.
- **Retention and legal hold are references.** Seven-year statement retention and litigation holds keep versions
  referenced, so GC can't collect their chunks, and immutable storage modes stop even an administrator shortening
  them (Unit 2 §8,
  [A9 Unit 2 §5](../A9-security-multitenancy/unit-2-data-protection-audit.md#5-compliance-data-lifecycle-and-separation-of-duties)).
  The append-only version history doubles as the audit trail regulators ask for: who uploaded which version, when,
  and who viewed it.
- **Vendor document feeds fail like flaky phones.** A core-banking or imaging vendor that drops an SFTP or API
  transfer halfway through, or resends yesterday's batch, is the resumable-upload and idempotent-commit problem with a
  vendor instead of a phone; content hashes tell you a resent file is identical and let you verify what the vendor
  delivered, the integrity checks of Unit 2 §6 applied at your adapter boundary.
- **Reconciliation is a sweep.** The mark-and-sweep audit that compares reference counts with the versions that
  actually exist is the same shape as the daily reconciliation between your ledger and a processor's settlement file:
  trust the incremental counter for speed, and the periodic full comparison for truth.

## Mastery gate checklist

- [ ] `sd quiz C10` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C10` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C10` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C10 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C10 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C10 unlocks C11 (video streaming), which reuses this module's upload sessions, blob tier, content addressing and CDN
reasoning for a write-once, read-many workload with a transcoding pipeline in front. Expect "design Dropbox", "design
Google Drive", "design a photo backup service" or "design a document vault" in the D3 and D4 mocks, where the
interviewer's deep dives are usually chunking and dedupe, the upload and commit protocol, and conflicts. C16
(collaborative editing, STRETCH) picks up where conflicted copies stop: merging concurrent edits inside a document.
Before moving on, run `sd review` and clear any C10 questions it surfaces, especially tags `chunking`, `dedupe`,
`integrity`, `sync`, `conflict-resolution` and `erasure-coding`; and re-read both units' wrap-up tables the day before
your next HLD mock.
