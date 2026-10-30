# C11 Teach-back

## Prompt

A mid-level engineer on your team has been asked to add video to the product. Their plan: "an upload endpoint on our
API servers that receives the file; a cron job every five minutes that runs `ffmpeg` on new uploads to make a 720p
MP4, retrying anything that fails on the next run; the MP4 is served from our API servers behind NGINX; view counts
are `UPDATE videos SET views = views + 1` on each play; we'll add a CDN if it gets slow." They have never run a media
product.

Explain to them, in your own words, **how a video platform has to be built, and why**: which direction the bytes
mostly flow and what that does to the design and the bill, how an upload gets in, how one upload becomes many files
and why the work is split the way it is, what happens when a transcode fails (and why not every failure should be
retried), how a player chooses what quality to fetch and why that needs segments and a ladder, what a CDN must be
configured to do for video and where storage money goes, and how to count views without a hot row.

Form: written, 500–900 words, one diagram (Mermaid or a sketch) allowed. Use numbers: pick a scale (theirs or the
module's) and show two or three calculations. No copying from the unit files; if a sentence from the course appears
verbatim, the evaluator will ask you to rephrase it. Write for that engineer, so they can go and change their
design, not for an examiner.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Egress dominates**: estimates output bandwidth from concurrent viewers × delivered bitrate (with numbers and
      units), contrasts it with ingest (orders of magnitude smaller), and concludes that a CDN is required from the
      start and that bytes per hour watched and price per GB are the main cost levers.
- [ ] **Uploads go direct to the blob store** via signed, resumable, chunked uploads with idempotent create and
      complete, metadata kept separately from bytes; video never passes through API servers.
- [ ] **Segments and a ladder**: the video is encoded into several renditions with aligned, keyframe-started
      segments and a manifest (HLS/DASH, ideally CMAF), so the player can change quality between segments.
- [ ] **ABR lives in the player**: picks a rendition per segment from measured throughput with a safety margin,
      switches down immediately, switches up only with a healthy buffer, drops to the lowest rung when the buffer is
      nearly empty; the server serves static, cacheable files.
- [ ] **The pipeline is a dependency graph run in parallel**: transcode per (segment, rendition) with bounded
      workers, package when a segment's renditions exist, manifest when all segments are packaged; explains why the
      segment is the unit of parallelism (turnaround independent of video length).
- [ ] **Failure handling with idempotent resume**: deterministic (versioned) output keys so a rerun skips finished
      steps; bounded retries with backoff for transient failures; per-attempt timeouts; corrupt inputs fail fast to a
      dead letter with a message to the creator rather than being retried forever.
- [ ] **CDN configured for video**: immutable versioned segment URLs with long TTLs, auth tokens validated at the
      edge but kept out of the cache key, a shield/mid-tier with request collapsing, and byte hit ratio as the metric
      (with the miss-ratio arithmetic: 95% → 99% is 5× less origin traffic).
- [ ] **View counts from an event log**: batched player events, windowed aggregation, batched counter writes,
      dedupe/fraud filtering; explains why a per-play row increment fails on a viral video.

Should-hit (at least 5 of 9):
- [ ] Storage estimate per minute across the ladder plus source, and tiering by last access for the long tail
      (infrequent-access renditions, archived sources, never playable content in hour-latency archive).
- [ ] Spending more encode compute (newer codec, per-title ladder) pays only where views are high; the
      upload-to-watch ratio decides it.
- [ ] Publishing as an atomic version flip after the manifest exists, so players never see a manifest pointing at
      missing segments; re-encodes as new versions.
- [ ] Fairness across jobs (a 3-hour upload must not starve short ones) and a fast path that publishes low rungs
      first.
- [ ] Cancellation of a deleted video's job without leaking workers or processes.
- [ ] Multi-CDN for resilience and price, with the warm-cache caveat on failover.
- [ ] Watch progress saved every ≈ 30 s and on pause/exit, last-writer-wins, partitioned by user.
- [ ] QoE metrics from players (startup time, rebuffer ratio, errors by CDN/region/device) as the real SLIs.
- [ ] Segment duration trade-off (startup and adaptation vs request rate and compression), ≈ 4–6 s for VOD.

Red flags (any one = revise):
- [ ] Sizes the system from uploads, or confuses bits and bytes (an 8× error) without catching it.
- [ ] Serves video from application servers, or treats the CDN as an optional later optimisation at this scale.
- [ ] A single rendition, or server-side bitrate selection per viewer.
- [ ] Retries every failure the same way, forever, or restarts failed jobs from zero with no resume.
- [ ] Overwrites published files in place (no versioning) while a CDN caches them.
- [ ] Counts views with a per-play database increment, or claims exact real-time counts are needed for display.
- [ ] Numbers stated as facts with no scale assumption.

## Recording

`sd record teachback C11 --result <pass|revise> --notes "<missed items>"`
