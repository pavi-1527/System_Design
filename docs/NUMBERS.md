# Numbers to know (one page)

Everything here is **approximate** (`≈`). The point is order of magnitude and ratios, not decimals. Hardware and
cloud pricing drift; anything marked `TODO-VERIFY` should be checked against current vendor docs before you quote
it as fact. In an interview, say "roughly" and show the arithmetic; nobody grades the third significant figure.

## Time conversions (memorise these)

| Quantity | Exact | Napkin |
|---|---|---|
| Seconds per day | 86,400 | ≈ 10^5 (overestimates seconds by ~16%, so it *underestimates* QPS by ~14%; fine, then apply a peak factor) |
| Seconds per month | ≈ 2.6 × 10^6 | ≈ 2.5 × 10^6 |
| Seconds per year | ≈ 3.15 × 10^7 | ≈ π × 10^7 ≈ 3 × 10^7 |
| 1 M requests/day | ≈ 11.6 req/s | ≈ 10 req/s |
| 100 M requests/day | ≈ 1,160 req/s | ≈ 1k req/s |
| 1 B requests/day | ≈ 11,600 req/s | ≈ 10k req/s |

## Powers of two / ten

| Power | ≈ Decimal | Name |
|---|---|---|
| 2^10 | 10^3 | KB (KiB) |
| 2^20 | 10^6 | MB |
| 2^30 | 10^9 | GB |
| 2^40 | 10^12 | TB |
| 2^50 | 10^15 | PB |

Bits vs bytes: network is quoted in **bits** per second, storage in **bytes**. 1 Gbps ≈ 125 MB/s. 10 Gbps ≈ 1.25 GB/s.
Mixing these up is the single most common estimation error (8×).

## Latency ladder (≈, commodity hardware, varies by generation)

| Operation | ≈ Latency | Mental model |
|---|---|---|
| L1 cache reference | 1 ns | |
| Branch mispredict | 3–5 ns | |
| L2 cache reference | 4 ns | |
| Uncontended mutex lock/unlock | 15–25 ns | contended can be µs |
| Main memory reference | 100 ns | 100× L1 |
| Compress 1 KB (fast codec, e.g. Snappy/LZ4) | 1–3 µs | |
| Send 1 KB over 1 Gbps link (serialization only) | 8–10 µs | |
| Random 4 KB read, NVMe SSD | 20–100 µs | |
| Read 1 MB sequentially from RAM | 10–50 µs | memory bandwidth ≈ 20–50 GB/s per socket (TODO-VERIFY per CPU) |
| Round trip inside one datacenter | 0.1–0.5 ms | |
| Read 1 MB sequentially from NVMe SSD | 0.2–1 ms | NVMe ≈ 2–7 GB/s; SATA SSD ≈ 0.5 GB/s |
| HDD seek | 5–10 ms | |
| Read 1 MB sequentially from HDD | 5–10 ms | ≈ 100–200 MB/s |
| Round trip, same region different AZ | ≈ 1–2 ms | TODO-VERIFY per cloud/region |
| Round trip, US east ↔ west coast | ≈ 60–80 ms | |
| Round trip, US ↔ Europe | ≈ 80–150 ms | |
| Speed of light in fibre | ≈ 200,000 km/s | ≈ 1 ms RTT per 100 km of fibre path, before any routing |

Connection setup costs, in round trips (RTT):

| Setup | RTTs before first request byte |
|---|---|
| TCP | 1 |
| TCP + TLS 1.2 (full handshake) | 3 |
| TCP + TLS 1.3 | 2 |
| TLS 1.3 resumption with 0-RTT | 1 (TCP) + 0 (data rides with the handshake; replayable!) |
| QUIC / HTTP/3 (new) | 1 |
| QUIC 0-RTT resumption | 0 (replayable) |

So a fresh HTTPS call over a 100 ms RTT path costs ≈ 200–300 ms before the server even sees the request. This is
why connection pooling and keep-alive matter.

## Single-box throughput (≈, very workload dependent — use as a starting assumption, then say so)

| Component | ≈ Throughput | Notes |
|---|---|---|
| Stateless HTTP service (Go/Java), real business logic + 1–2 downstream calls | 1k–10k req/s per server | "hello world" benchmarks are 10–100× higher and irrelevant |
| Relational DB primary (Postgres/MySQL), indexed point reads | ≈ 10k–50k QPS | cached working set; drops sharply when reads hit disk |
| Relational DB primary, small transactional writes | ≈ 1k–10k TPS | bounded by fsync/WAL and lock contention |
| Redis, single instance, simple GET/SET | ≈ 100k+ ops/s | one main thread; pipelining raises it a lot |
| Kafka broker | ≈ hundreds of MB/s | partition-level throughput lower; TODO-VERIFY for your hardware |
| Commodity server | 32–64 cores, 128–512 GB RAM, several TB NVMe, 10–25 Gbps NIC | |

Target utilisation for capacity planning: plan for ≈ 50–70% of a box's measured peak so you survive a node loss and
traffic spikes. "Servers needed = peak QPS / (per-server QPS × target utilisation)", then add N+1 or N+2.

## Sizes

| Thing | ≈ Size |
|---|---|
| ASCII char | 1 B (UTF-8: 1–4 B) |
| int64 / Unix timestamp (ms) | 8 B |
| UUID | 16 B binary, 36 B as text |
| Short text post + metadata | ≈ 0.5–1 KB |
| Typical row in an OLTP table | ≈ 100 B – 1 KB |
| Compressed photo | ≈ 200 KB – 2 MB; thumbnail ≈ 10–50 KB |
| 1080p video | ≈ 5 Mbps ≈ 40 MB/min (bitrate varies a lot by codec/content) |
| Audio, compressed | ≈ 128 kbps ≈ 1 MB/min |

## Availability (nines)

| SLO | Downtime / year | Downtime / 30 days |
|---|---|---|
| 99% | ≈ 3.65 days | ≈ 7.2 h |
| 99.9% | ≈ 8.8 h | ≈ 43 min |
| 99.95% | ≈ 4.4 h | ≈ 22 min |
| 99.99% | ≈ 53 min | ≈ 4.3 min |
| 99.999% | ≈ 5.3 min | ≈ 26 s |

Serial dependencies multiply: a request that must touch 3 services each at 99.9% has at best ≈ 99.7%. Redundant
(parallel) replicas improve it: two independent 99% replicas ≈ 1 − 0.01² = 99.99% *if failures are independent*,
which they usually are not (shared deploys, shared config, shared region).

## Rules of thumb (assumptions, not laws — state them out loud)

- **Peak factor:** consumer traffic peak ≈ 2–3× daily average; flash events can be 10×+. Batch/B2B can be spikier
  (end-of-day files, payroll runs).
- **Little's law:** concurrency L = arrival rate λ × time in system W. 2,000 req/s × 0.25 s = 500 requests in flight.
  This sizes thread pools, connection pools and queue depths.
- **80/20 hot set:** caching ≈ 20% of the daily-read objects often serves most reads. It's a heuristic; skewed
  workloads (celebrities, viral items) are more concentrated, long-tail catalogues less.
- **Replication factor:** storage × 3 for typical replicated stores; plus indexes (≈ 10–50% overhead) and headroom.
- **Tail latency:** with fan-out to N backends, the request is as slow as the slowest. If each backend's p99 is
  100 ms, a 100-way fan-out sees at least one >p99 response on ≈ 63% of requests (1 − 0.99^100).

## Cost ballpark (cloud, list prices drift — TODO-VERIFY before quoting)

| Item | ≈ Price | Notes |
|---|---|---|
| Object storage (S3 Standard, us-east-1) | ≈ $0.023 / GB-month | cheaper tiers for infrequent/archival access |
| Internet egress | ≈ $0.05–0.09 / GB | often the surprise line item for media products |
| Block storage SSD | ≈ $0.08–0.10 / GB-month | |
| Mid-size VM (≈ 8 vCPU, 32 GB) | ≈ $200–400 / month on demand | reserved/spot are much cheaper |

Use cost to compare designs ("CDN egress vs origin egress", "keep 1 year hot vs tier to cold after 30 days"), not
to produce a budget.
