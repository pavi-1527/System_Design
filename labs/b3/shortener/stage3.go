package shortener

import "context"

// Stage 3 (≈ 15 min): concurrency guarantees, and a second Store to prove the
// seam. The concurrency requirements apply to your Service as a whole:
//
//   - Parallel Shorten of one URL (or of equivalent spellings of it) returns
//     one code and leaves exactly one record in the Store.
//   - Parallel ShortenWith claims of one alias have exactly one winner, also
//     when the alias is expired.
//   - Click counts are exact under concurrent Resolve calls.
//   - A Store failure leaves no half-created state: no dedupe entry pointing
//     at a record that was never written, no burned alias, no reset counter.
//     If the Store cannot be read while you verify a dedupe hit, return the
//     error; do not mint a second code for the same URL.
//   - Do not hold a lock across Store calls that every request needs.

// ShardedStore is a Store that spreads codes over independently locked shards
// (hash of the code), so writers to different codes rarely contend. It must
// pass the same contract suite as MemoryStore.
type ShardedStore struct {
	// TODO(lab)
}

// NewShardedStore returns a store with n shards (n < 1 means 16).
func NewShardedStore(n int) *ShardedStore {
	// TODO(lab)
	_ = n
	return &ShardedStore{}
}

func (s *ShardedStore) Put(ctx context.Context, rec Record) error {
	// TODO(lab)
	_, _ = ctx, rec
	return errNotImplemented
}

func (s *ShardedStore) Get(ctx context.Context, code string) (Record, error) {
	// TODO(lab)
	_, _ = ctx, code
	return Record{}, errNotImplemented
}

func (s *ShardedStore) Delete(ctx context.Context, code string) error {
	// TODO(lab)
	_, _ = ctx, code
	return errNotImplemented
}

// ShardLens returns the number of records in each shard, one entry per shard.
func (s *ShardedStore) ShardLens() []int {
	// TODO(lab)
	return nil
}
