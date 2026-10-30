# A2 Teach-back

## Prompt

A mid-level engineer on your team has been asked to review a pull request that adds a "transfer between my accounts"
endpoint. The code runs `SELECT balance`, checks it against the amount in Go, then runs `UPDATE accounts SET balance = ?`
with the value it computed. The database is PostgreSQL at its default isolation level. The author says "it's inside a
transaction, so it's safe, and the database is fast because it's a B-tree".

Explain to them, in your own words, **why a database is shaped the way it is** and **why this code can double-spend**:
how an index and a storage engine make reads and writes cheap (B-tree versus LSM, and what the write-ahead log is for),
what "committed" actually promises, and what `READ COMMITTED` does and does not protect, including the fixes you would ask
for and what each one prevents.

Form: written, 500–900 words, may include one diagram (Mermaid or a sketch). No copying from the unit text: if a sentence
from the course appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, not for an
examiner: concrete beats complete.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Explains the design pressure behind storage engines: disks are read and written in pages, random I/O and `fsync`
      are the expensive operations, memory is small compared with data.
- [ ] **B-tree**: pages with a high fan-out give a shallow tree (a few levels for billions of keys, top levels cached);
      a lookup reads one page per level; inserts split full pages; range scans follow linked leaves; the cost is
      random page updates (write amplification) and, with random keys, cache misses.
- [ ] **Write-ahead log**: changes are logged and the log is forced to disk (`fsync`) before the commit is acknowledged;
      data pages are written later; recovery replays the log from the last checkpoint and discards a torn tail;
      group commit shares the `fsync` cost across concurrent commits.
- [ ] **LSM tree**: writes go to a memtable (protected by a log) and are flushed as immutable sorted files that are merged
      by compaction; reads check newest to oldest, helped by bloom filters; deletes are tombstones; the trade-off is stated
      with write / read / space amplification, and when each engine fits (with at least one number or concrete workload).
- [ ] **Why this code double-spends**: at `READ COMMITTED` each statement sees the latest commit, so two transactions can
      both read a sufficient balance, both pass the check and both write; the transaction is atomic but not isolated
      from this interleaving. Names the anomaly (lost update; and, for a rule over several rows, write skew).
- [ ] Gives **at least three fixes** and says what each does and does not prevent: a single conditional `UPDATE`
      (`… WHERE balance >= ?`), `SELECT … FOR UPDATE` (with lock ordering), an optimistic version column or snapshot
      isolation (first-committer-wins, needs retry), and `SERIALIZABLE`/guard row for multi-row rules.

Should-hit (at least 4 of 7):
- [ ] The index-cost point: each index is another B-tree to update on every write; random primary keys (UUIDv4) thrash
      the cache once the index outgrows memory, while ascending or time-ordered keys touch one edge.
- [ ] Write skew example with a shared limit across two accounts and why snapshot isolation does not catch it.
- [ ] Retries: serialization failures and deadlocks are normal; retry the whole transaction, keep it idempotent and free
      of external side effects; acquire locks in a canonical order.
- [ ] Durability settings: asynchronous commit loses recent acknowledged commits but stays consistent, while `fsync` off can
      corrupt; replica failover with lag can also lose acknowledged commits.
- [ ] Tombstones cannot be dropped until nothing older can contain the key, or deleted data returns.
- [ ] Isolation level names are not portable (PostgreSQL's REPEATABLE READ is snapshot isolation; MySQL's is not the same).
- [ ] Mentions schema changes or long transactions as a hazard (a waiting `ALTER` blocks everyone; a long snapshot bloats
      tables), or that a transaction does not span services (A6).

Red flags (any one = revise):
- [ ] Says "it's in a transaction, so it's safe" about the read-modify-write, or that transactions prevent all
      concurrency anomalies.
- [ ] Claims LSM trees are "faster" or B-trees are "slower" without stating the workload and the amplification trade-off.
- [ ] Says data is durable once the application writes it (or `write()` returns) without `fsync`, or treats the WAL as
      a backup.
- [ ] Says snapshot isolation (or REPEATABLE READ) is the same as serializable, or that `SELECT … FOR UPDATE` prevents
      phantoms.
- [ ] Proposes fixing the race with an application-level check (`if balance >= amount` in Go) or an in-process mutex
      as the mechanism for a multi-instance service.

## Recording

`sd record teachback A2 --result <pass|revise> --notes "<missed items>"`
