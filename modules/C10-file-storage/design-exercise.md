# C10 Design exercise — Photo and video backup for a phone platform

> Time box: 45 minutes, solo, no notes, timed like a real interview (numbers by 0:10, end-to-end diagram by 0:25,
> stop drilling at 0:40). Then self-score against the rubric (≈ 10 min), then send the write-up to the tutor for the
> probe session. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md) — from memory.

This is a *variant* of the module's case study on purpose. The mechanisms (metadata versus blobs, bytes first and
metadata last, resumable upload sessions, content hashes, integrity and GC, a change feed with cursors, erasure
coding and tiering) all apply, but the constraints are different: the data is written once and almost never edited,
uploads come from phones on metered networks and batteries, the "sync" is mostly one-way, and the read pattern is
heavily skewed towards recent photos and towards thumbnails rather than originals. If you reproduce Unit 1's design
box for box (content-defined chunking, global dedupe, conflicted copies) you will score "approaching": the rubric
rewards *transferring* the decisions, with numbers, to this shape, including deciding which of Unit 2's mechanisms
this product doesn't need.

## Prompt

> "We make a phone OS. Design the cloud photo and video backup: every photo and video a user takes is backed up
> automatically, they can browse their whole library from any of their devices and the web, they can free up space
> on the phone without losing anything, and they can share albums with friends."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down with the answer
you'd assume if told "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Scale: ≈ 50 M users with backup turned on; ≈ 20 M of them upload something on a given day; average existing
  library ≈ 15 GB per user.
- Per uploading user per day: ≈ 10 photos at ≈ 3 MB and ≈ 0.2 videos at ≈ 100 MB.
- By default uploads happen on Wi-Fi while charging: ≈ 60% of a region's daily uploads fall in a 4-hour evening
  window. Users can opt in to cellular uploads.
- Each photo gets three thumbnail sizes, ≈ 60 KB in total. ≈ 30 M users open the library each day and scroll
  through ≈ 200 thumbnails; ≈ 90% of views are of items less than 30 days old.
- Edits are non-destructive: the original is kept and an edit list plus a rendered copy is stored. Favourites,
  captions and album membership can be changed from any device, including offline.
- "Free up space" removes local originals and keeps thumbnails on the phone; deleting an item in the app moves it
  to a trash for 30 days on every device, then it's gone.
- Shared albums: up to 200 members; members can add photos and comment; ≈ 5% of photos are added to a shared album.
- Durability: losing a user's photo is the worst possible incident. Availability: browsing should work even when
  uploads are degraded. EU users' data must stay in the EU.
- Out of scope unless you choose otherwise: face recognition and search, printing, the camera app itself.

</details>

## What to produce

1. **Timestamp log**: the clock time at the start of each phase (evidence for time management).
2. **Requirements card**: functional requirements (4–6), non-functional requirements *with numbers* (durability,
   availability, upload freshness, browse latency), the semantics of "backup", "free up space" and "delete", explicit
   out-of-scope, and your clarifying questions with the assumptions you proceeded with.
3. **Estimate sheet**: at most 6 numbers with arithmetic and units, each with the decision it drives: items and bytes
   uploaded per day and in the peak window, storage growth per year with replication versus erasure coding,
   thumbnail storage and thumbnail reads per second, metadata size, and the cost lever that matters most.
4. **API sketch**: the resumable upload (how a 2 GB video survives a network change), the "already backed up?"
   check, commit, the library listing with a cursor, metadata edits (favourite, caption, album membership), and
   thumbnail and original downloads.
5. **Data model**: access patterns first; library items, originals and renditions, per-user hashes, albums and
   membership, the change journal, upload sessions; partition keys; where the bytes live.
6. **HLD diagram**: the upload path, the browse path (thumbnails), the "open original" path, the shared-album path,
   and the background pipeline (thumbnails, tiering, GC, scrubbing), with rates on at least three arrows and a
   one-line justification per box.
7. **Deep dives (2–3)**, each with alternatives, a cost and a flip condition:
   - the upload path on a phone: parts, sessions, the dedupe check, commit, retries, battery and network policy, and
     what you don't build (and why) compared with a document-sync product;
   - storage durability and cost: replication versus erasure coding, tiering by age and access, what stays hot,
     region pinning;
   - concurrent metadata edits and the change feed: favourites, captions and album membership edited on two devices
     offline, what conflicts and what merges, and how devices and album members learn about changes.
8. **Failure walkthrough**: the blob tier in one region rejects writes for an hour during the evening peak; a client
   release has a bug that commits items whose originals were never uploaded; the thumbnail CDN is down for 20 minutes.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & semantics | Draws immediately; "upload photos to S3" | Lists features but not the semantics of backup, free-up-space and delete | Functional and non-functional requirements with numbers; backup is one-way (deleting a local file to free space never deletes the backup), app deletes go to a 30-day trash everywhere; durability over availability; out-of-scope stated | Spots that "free up space" makes the cloud copy the *only* copy, so durability and integrity checks before local deletion become a product requirement |
| Estimation | Missing, or ≥ 10× off on a number the design depends on | Daily totals only; no peak window, no thumbnail reads | ≤ 6 numbers, units carried: ≈ 200 M items and ≈ 1 PB/day (≈ 93 Gbps average, ≈ 300 Gbps in the evening window), ≈ 365 PB/year logical (≈ 510 PB coded vs ≈ 1.1 EB replicated), thumbnails ≈ 12 TB/day and ≈ 70k reads/s, metadata ≈ 75 TB/year; each tied to a decision | Costs the yearly growth and shows tiering as the main lever, with the access-skew assumption stated |
| API & data model | One `POST` per file; no idempotency; metadata and bytes together | Sessions or hashes mentioned but no commit semantics; no cursor | Resumable sessions with fixed-size parts, a per-user "have it?" check by whole-file hash, an idempotent commit only after bytes are verified, a cursor-based change feed; items keyed by a stable id, originals and renditions as separate blobs, albums as their own shared space; partition by user (and album) | Designs the client-side local index (hash → item) so a new phone restoring a library uploads nothing, and the "pending local delete" state that waits for a verified commit |
| High-level design | Bytes through API servers; no thumbnails path | Separate stores drawn but browse served from originals; no background pipeline | Presigned direct-to-blob uploads; metadata service sharded by user; thumbnail pipeline after commit; thumbnails from a CDN; originals on demand; outbox-driven change notifications (OS push for phones); every box justified; rates on arrows | Per-region cells with EU pinning drawn; degraded modes (uploads paused, browse still works) drawn |
| Upload path & integrity | Retries every failure from zero; no hash checks | Resumable but trusts client hashes, or no orphan handling | Fixed parts (≈ 4–16 MiB) uploaded in parallel and resumed after network changes; the blob tier re-hashes; commit after all parts exist; session TTL and orphan GC with a grace period; Wi-Fi/charging policy; explicitly *no* content-defined chunking or global dedupe, with the reason (immutable, unique media; privacy) | Verifies the stored original (and its hash) before telling the phone it may delete its local copy; measures upload backlog per device as a product SLI |
| Concurrent edits & sync | Last write wins on everything by device clock, or "conflicted copies" of photos | Some merge rule, but no change feed or cursor | Change journal per user (and per shared album) with cursors; OS push or long-poll hints; per-field resolution for metadata: favourites and captions by server commit order (LWW acceptable and stated), album membership as add/remove sets that merge; originals never conflict because they're immutable | Discusses version vectors or per-field versions for captions edited offline on two devices, and why a conflicted copy makes no sense for a photo |
| Durability, tiering & cost | 3× everything, no tiering | Erasure coding *or* tiering, without numbers or placement | Replicated open volumes, erasure-coded sealed volumes across racks (and a zone-aware layout), scrubbing; originals older than ≈ 30 days tier to cheaper media while thumbnails and recent items stay hot; region pinning; the annual storage cost and the tiering saving quantified | Retrieval latency of cold originals designed into the UX (a progress indicator, prefetch of albums being opened); minimum-duration fees considered for items deleted within 30 days |
| Failure modes & ops | None, or "add retries" | Generic metrics | The three walkthroughs answered with what users see (uploads queue on the phone and resume; nothing is deleted locally until committed; browsing continues from cached thumbnails or origin); metrics: upload backlog, commit success, orphan bytes, scrub coverage, thumbnail CDN hit rate, cold-read latency | Client-release safety: staged rollout and a server-side invariant check (no commit without verified parts) that would have caught the bug |
| Communication & time | Checkpoints missed by > 8 min; no trade-off table | Finishes but a phase overruns; trade-offs lack flip conditions | Numbers by ≈ 0:10, diagram by ≈ 0:25, stops at 0:40; drives; a trade-off table with ≥ 3 rows and flip conditions | Anticipates probes; says what was cut and why |

**Overall "meets"** requires *meets* or better on **Upload path & integrity** and **Durability, tiering & cost**
(the two places this design either keeps photos safe and affordable or doesn't), and no dimension at *below*.
**Overall "exceeds"** requires *meets* everywhere and *exceeds* on at least three dimensions. Otherwise overall is
*approaching* (or *below* with two or more dimensions at *below*).

## Tutor critique protocol

1. **Check the artefacts before probing (≈ 3 min).** Timestamp log present; slips at 0:10 / 0:25 / 0:40 computed.
   The semantics of backup, free-up-space and delete are stated. The estimate sheet's arithmetic: items ≈ 20 M ×
   10.2 ≈ 204 M/day ≈ 2,400/s average, ≈ 0.6 × 204 M ÷ 14,400 s ≈ 8,500/s in the evening window; bytes ≈ 200 M ×
   3 MB + 4 M × 100 MB ≈ 1 PB/day ≈ 11.6 GB/s ≈ 93 Gbps average, ≈ 0.6 PB ÷ 14,400 s ≈ 42 GB/s ≈ 330 Gbps in the
   window (lower if regions' windows don't overlap); growth ≈ 365 PB/year logical, ≈ 510 PB raw at 1.4× versus
   ≈ 1.1 EB at 3×, on top of ≈ 750 PB existing (50 M × 15 GB); thumbnails ≈ 204 M × 60 KB ≈ 12 TB/day; thumbnail
   reads ≈ 30 M × 200 ≈ 6 B/day ≈ 70k/s average; metadata ≈ 204 M × ≈ 1 KB ≈ 200 GB/day ≈ 75 TB/year; at
   ≈ $0.023 per GB-month, a year's growth adds ≈ $8 M a month at list price, so tiering ≈ 80% of it to a tier at
   roughly half the price saves ≈ $3 M a month (`TODO-VERIFY` current prices). An error ≥ 10× on bytes per day,
   storage growth or the bits/bytes conversion caps *Estimation* at "approaching".
2. **Self-score first.** Ask for a level per dimension with a one-line justification before saying anything.
3. **Probe (≈ 10 min).** Ask at least six, including the ones marked (required); one at a time; push one rung
   further on vague answers.
   1. **(required)** "Walk me through a 2 GB video uploading on a train, losing signal three times, and the phone
      then freeing up space."
      *Meets:* a session with fixed-size parts and their hashes; parts uploaded in parallel and resumed by asking
      which parts are missing; the blob tier re-hashes each part; commit with the whole-file hash after all parts
      exist (idempotent on the session); the phone deletes its local original only after the commit succeeds (and,
      ideally, after a server-side verification of the stored object); an expired session's parts are reclaimed by
      GC after a grace period.
   2. **(required)** "Your Unit 1 design used content-defined chunking and global dedupe. Do you use them here?"
      *Meets:* no CDC (media is immutable; edits are separate renditions), fixed parts for resumability; per-user
      whole-file hashes so a new phone or a reinstall uploads nothing already backed up; no global dedupe (personal
      photos are unique, and existence leaks are unacceptable for private photos).
   3. **(required)** "Storage grows ≈ 1 PB a day. What does that cost, and what do you do about it?"
      *Meets:* ≈ 365 PB/year; erasure-code sealed volumes (≈ 1.4×) instead of replicating (3×); tier originals
      older than ≈ 30 days to cheaper media given the ≈ 90% recent-view skew, keeping thumbnails and metadata hot;
      a cost number per year; minimum-duration fees for items likely to be deleted.
   4. **(required)** "The user favourites a photo on the phone while offline and un-favourites it on the tablet.
      Then adds it to an album on one and removes it from another album on the other. What's the final state?"
      *Meets:* per-field rules: the favourite flag by server commit order (LWW acceptable and said so), album
      membership as add/remove operations that merge (both changes apply); no conflicted copies of photos;
      devices learn via the change feed by cursor.
   5. "A client release commits items whose originals were never uploaded. How would you have caught it, and how do
      you clean up?"
      *Meets:* the server refuses commits whose parts aren't verified in the blob tier (bytes first); if it got
      past that (a bug in the check), a reconciliation job finds items without stored originals, and the phones
      that still have the local file re-upload; staged rollouts and a commit-anomaly alert.
   6. "The blob tier in one region rejects writes for an hour at 20:00. What do users see?"
      *Meets:* uploads queue on phones and resume; nothing is deleted locally; browsing and sharing of
      already-backed-up items work; the backlog drains with jittered retries and a server-side rate limit after
      recovery; EU data is not failed over outside the EU.
   7. "How does a shared album with 200 members stay in sync?"
      *Meets:* the album is its own space with its own journal; members' devices hold cursors for it; adds and
      comments fan out as hints; items are referenced, not copied, with authorisation checked per item.
   8. "What would you do differently for a document-sync product?" (credit toward *exceeds* only: names CDC, delta
      sync, conflicted copies and per-tenant dedupe, and why each matters there and not here).
4. **Score each dimension with evidence** (quote the write-up or the probe answer). Say what would have moved each
   dimension up one level.
5. **Calibration feedback**: name any dimension where the self-score was off by more than one level.
6. **Decide** with the overall rule above. Below "meets": give 2–3 concrete revision tasks and redo on a different
   day with a variation (suggested: "design a document vault for a multi-tenant banking platform: members and
   relationship managers upload statements, KYC documents and cheque images from phones and branches; retention of
   seven years with legal holds; per-credit-union isolation and keys; virus scanning before anyone else can open a
   file; an audit trail of every access").

## Recording

`sd record design C10 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C10 --self meets --tutor approaching --notes "upload path solid; copied CDC and global dedupe from unit 1; no tiering numbers"`
