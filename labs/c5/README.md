# C5 labs

One Go package, stdlib only: the hardest component of the news-feed case study, the **hybrid fan-out timeline
service**. The stub compiles and returns zero values or `errNotImplemented`, so every test fails fast until you
implement it. The reference solution lives in `solutions/c5/feed/`; the `*_test.go` files there are byte-identical
to the ones here. Edit the code, never the tests.

Run what the gate checks with `sd lab C5` (it runs `go test -race` over the package). While working, run it
directly:

```sh
go test -race ./labs/c5/feed/
```

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`feed`](feed/) | 2 (§5–§7) | CORE |

Requirements: Go 1.22+ and a working race detector (`go env CGO_ENABLED` prints `1`). Every test that could block
runs under a 5 s timeout, so a deadlock fails with a message instead of hanging.

---

## 1. `feed` — hybrid fan-out home timeline

**Goal.** An in-memory feed service that makes the Unit 2 design concrete: fan-out on write into bounded per-user
timelines for ordinary authors, merge on read for celebrities, cursor pagination that never duplicates or skips,
idempotent posting, lazy handling of deletes, eager handling of unfollows, and all of it safe under `-race`.

**Edit** `labs/c5/feed/feed.go`. Provided: `Config` and defaults, `Post`, `Key`, `KeyOf`, `Before` (the total
order: newest first, then larger ID first), the unsigned cursor codec, `Page`, `Stats`, and `New`. Implement, in
this order:

1. `Follow`, `Unfollow`, `Followers` — the graph in both directions; `Unfollow` filters the follower's timeline in
   place so the author's posts vanish on the next read.
2. `Post` — `ctx.Err()` first; validate; dedupe by `(author, clientID)` (a retry returns the original post and
   fans out nothing); assign a fixed-width ID from a counter and `CreatedAt` from `Config.Now`; then **decide at
   write time**: fewer than `CelebrityThreshold` followers → insert an entry into every follower's timeline (kept
   sorted by `Before`, truncated to `MaxTimeline`), otherwise append to the author's pull list.
3. `Feed` — validate `limit` and the cursor before touching state; k-way merge the reader's timeline and the pull
   lists of every followee, newest first, starting strictly after the cursor (`sort.Search` per source); skip
   entries whose post is gone **before** counting toward `limit`; set `NextCursor` only when another live post
   exists.
4. `Delete`, `Timeline`, `Stats`.

Acceptance tests (`go test -race ./labs/c5/feed/`): `TestFollow_ValidationAndIdempotence`,
`TestPost_FansOutToEveryFollowerAndNobodyElse`, `TestPost_DuplicateClientIDReturnsSamePost`,
`TestPost_CelebrityIsNotFannedOutButAppearsInFeeds`, `TestPost_ThresholdCrossingNeitherDuplicatesNorLosesPosts`,
`TestFeed_NewestFirstWithIDTieBreak`, `TestFeed_MergesTimelineAndCelebritySourcesInGlobalOrder`,
`TestFeed_PagesWithoutDuplicatesOrSkipsUnderMutation`, `TestFeed_ValidatesCursorAndLimit`,
`TestFeed_DepthIsBoundedByMaxTimeline`, `TestFeed_UnknownUserGetsEmptyNonNilPage`,
`TestDelete_DeletedPostsAreNeverServedAndPagesStayFull`, `TestUnfollow_RemovesThatAuthorsPostsFromFeed`,
`TestConcurrent_PostsFollowsAndReadsUnderRace`. Run it a few times (`-count=10`) before calling it done.

**Design decisions the tests pin down** (each is a sentence you should be able to say in an interview):

- A post is either in timelines *or* in its author's pull list, never both: the decision is recorded per post at
  write time, so an author crossing the celebrity threshold in either direction neither duplicates nor loses posts.
- `Follow` does not backfill an ordinary author's old posts; a celebrity's old posts do appear, because they are
  merged on read. The lesson's trade-off table says when you would pay for backfill.
- Deletes are lazy (skipped at hydration), unfollows are eager (one timeline filtered). Scanning every follower's
  timeline on delete is the cost the lesson quantifies for a 10 M-follower account.
- Feed depth is bounded by `MaxTimeline` for ordinary authors: an 800-entry cache is the product decision, and
  "scroll further → fall back to pulling from the posts store" is the extension the lesson discusses.

### Break it

1. **Fan-out amplification.** The experiment behind the build tag runs one social graph (2,000 users, 20 random
   follows each, everyone follows user `u0`, 5 posts per user) three times: every author a celebrity
   (`CelebrityThreshold: 1`, pure fan-out on read), the hybrid (`100`), and nobody a celebrity (`math.MaxInt`,
   pure fan-out on write). Predict the `fanout_writes` column for each row first (posts × followers), then run:

   ```sh
   go test -tags breakit -run BreakIt -v ./labs/c5/feed/
   ```

   Reference solution on a laptop: all-pull **0** writes but reads ≈ 4× slower; hybrid ≈ 199k writes; all-push
   ≈ 209k writes (the extra ≈ 10k are `u0`'s 5 posts × 1,999 followers). Then change `followsEach` to 200 and
   `users` to 5,000 and watch the write phase: that is the write amplification the lesson's estimate predicts.
   What would the table look like if `u0` had 50 M followers?

2. **Decide at read time instead.** In your copy, make `Feed` merge an author's posts whenever that author
   *currently* has ≥ `CelebrityThreshold` followers, instead of using the per-post record. Run
   `go test -race -run ThresholdCrossing ./labs/c5/feed/`. Predict which post is duplicated and which disappears
   when alice crosses the threshold both ways.

3. **Remove the lock.** Comment out the mutex calls in `Post` and `Feed` and run `go test -race -run Concurrent
   ./labs/c5/feed/`. Read the race report: which map is written while being read? Then put back only a `RWMutex`
   around `Feed` (read lock) and leave `Post` unlocked — is `-race` satisfied? Why not?

4. **Shrink the window.** Set `MaxTimeline: 10` in `TestFeed_DepthIsBoundedByMaxTimeline`'s config in a scratch
   copy and page through a user who follows 50 active authors. How many of their posts are reachable? At 1 feed
   read per user per minute and 10 posts per followee per day, how long is an 800-entry window "fresh" for a user
   who follows 500 accounts? (Answer: ≈ 5,000 posts/day enter the timeline, so the 800 newest cover ≈ 4 hours.)

Answers and the reasoning are in `modules/C5-news-feed/unit-2-deep-dives.md` §5–§6.
