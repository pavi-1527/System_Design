# C10 Teach-back

## Prompt

A mid-level engineer on your team has been asked to build document storage and sync for the platform's members and
relationship managers. Their plan: "the client `POST`s each saved file to our API, which writes the bytes to S3 and
a row with the path and `modified_at` to Postgres; every client polls `GET /files?modified_after=<last poll>` every
30 seconds and downloads anything newer; if two people edit the same file, the later `modified_at` wins." They have
never run a storage system at scale.

Explain to them, in your own words, **how a file storage and sync service has to be built, and why**: why metadata
and file bytes live in different systems and the one rule that keeps them consistent; how a large file survives a
bad network and why only part of an edited file should be uploaded; what content addressing buys (and what global
deduplication leaks); how you know the bytes you serve are the bytes you stored; how devices learn about changes
without polling and without missing any; what happens when two devices edit the same file; and how the bytes
survive disk, rack and region failures without tripling the bill.

Form: written, 500–900 words, one diagram (Mermaid or a sketch) allowed. Use numbers: pick a scale (theirs or the
module's) and show two or three calculations. No copying from the unit files; if a sentence from the course
appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, so they can go and change
their design, not for an examiner.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Separates metadata from blobs** (a sharded, transactional metadata store; a content-addressed blob store) and
      states **bytes first, metadata last**: a version is committed in one metadata transaction only after every
      chunk it lists is durable and verified, so no version points at missing bytes.
- [ ] **Resumable, chunked uploads**: a session opened with a manifest, the server answering with only the missing
      chunks, chunks uploaded in any order and in parallel (directly to the blob tier, not through the API), resume
      after disconnects, and an **idempotent commit**.
- [ ] **Content-defined chunking** versus fixed-size chunking, with the insertion argument (fixed boundaries shift;
      CDC boundaries follow the content), and at least one number for the bandwidth saved.
- [ ] **Content addressing and dedupe** with the caveat: dedupe scope (per user or tenant versus global) is a privacy
      decision because a global "already have it" check reveals existence, and a hash must never act as a
      download right.
- [ ] **Integrity end to end**: the server re-hashes uploads, reads verify chunk hashes and the whole-file hash, and
      a scrubber finds latent corruption and repairs from redundancy.
- [ ] **Orphans and GC**: abandoned uploads leave unreferenced chunks; GC reclaims them only when no committed version
      references them and no live session holds them, after a grace period.
- [ ] **Sync by change journal and cursor**: a per-namespace sequence assigned in the commit, devices pulling changes
      after their cursor, notifications (long-poll or push) as hints only, and why `modified_after` timestamps skip
      changes.
- [ ] **Concurrent edits are detected, not overwritten**: a base-version check or version vectors classify
      "remote descends from mine", "mine descends from remote" and "concurrent", and concurrent edits produce a
      conflicted copy; device clocks never decide.

Should-hit (at least 5 of 9):
- [ ] An estimate that ties to decisions: storage with replication versus erasure coding, upload bandwidth with and
      without delta sync, or devices (not users) for the notification fleet.
- [ ] Presigned URLs scoped to one chunk, a size limit and a short expiry, with uncommitted bytes counted against
      quota.
- [ ] `file_id` as identity rather than path, so renames are cheap and edits follow the file.
- [ ] Shared folders as their own namespace mounted into members' trees, with one journal.
- [ ] Notifications published through an outbox so commits never wait on the notification fleet, and what happens
      when that fleet is down (polling fallback, nothing lost).
- [ ] Crash-safe client: apply a change and advance the cursor in one local transaction; downloads assembled to a
      temp file, verified and renamed atomically; only missing chunks downloaded.
- [ ] Replication for hot, newly written data and erasure coding for sealed data, placed across racks and zones,
      with the repair-traffic trade-off.
- [ ] Hot shared folders or public links: fan-out at the notification edge, jitter, a cached journal tail, a CDN for
      immutable chunks.
- [ ] A banking angle: per-tenant dedupe and keys, retention and legal holds keeping versions referenced, an audit
      trail of access.

Red flags (any one = revise):
- [ ] Stores file bytes in the metadata database, or routes uploads through the API servers as the design.
- [ ] Commits metadata before the bytes exist, or has no story for uploads that never finish.
- [ ] Uses timestamps (device or server) as the sync cursor, or device clocks to resolve conflicts.
- [ ] Resolves concurrent edits by last-writer-wins on file contents without saying that it loses data.
- [ ] Claims global dedupe is free, or lets a client reference a chunk just by knowing its hash.
- [ ] Trusts client-supplied hashes without re-hashing on the server.
- [ ] Numbers stated as facts with no scale assumption, or a ≥ 8× unit error (bits versus bytes, per day versus per
      second) that isn't caught.

## Recording

`sd record teachback C10 --result <pass|revise> --notes "<missed items>"`
