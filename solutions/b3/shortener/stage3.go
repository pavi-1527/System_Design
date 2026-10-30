package shortener

import (
	"context"
	"hash/fnv"
	"sync"
)

// ShardedStore is a Store that spreads codes over independently locked shards
// (FNV-1a hash of the code), so writers to different codes rarely contend.
type ShardedStore struct {
	shards []storeShard
}

type storeShard struct {
	mu   sync.RWMutex
	recs map[string]Record
}

// NewShardedStore returns a store with n shards (n < 1 means 16).
func NewShardedStore(n int) *ShardedStore {
	if n < 1 {
		n = 16
	}
	s := &ShardedStore{shards: make([]storeShard, n)}
	for i := range s.shards {
		s.shards[i].recs = map[string]Record{}
	}
	return s
}

func (s *ShardedStore) shard(code string) *storeShard {
	h := fnv.New32a()
	h.Write([]byte(code))
	return &s.shards[h.Sum32()%uint32(len(s.shards))]
}

func (s *ShardedStore) Put(_ context.Context, rec Record) error {
	sh := s.shard(rec.Code)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if _, ok := sh.recs[rec.Code]; ok {
		return ErrExists
	}
	sh.recs[rec.Code] = rec
	return nil
}

func (s *ShardedStore) Get(_ context.Context, code string) (Record, error) {
	sh := s.shard(code)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	rec, ok := sh.recs[code]
	if !ok {
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (s *ShardedStore) Delete(_ context.Context, code string) error {
	sh := s.shard(code)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if _, ok := sh.recs[code]; !ok {
		return ErrNotFound
	}
	delete(sh.recs, code)
	return nil
}

// ShardLens returns the number of records in each shard (for tests and
// capacity planning). It is a point-in-time estimate under concurrent writes.
func (s *ShardedStore) ShardLens() []int {
	out := make([]int, len(s.shards))
	for i := range s.shards {
		s.shards[i].mu.RLock()
		out[i] = len(s.shards[i].recs)
		s.shards[i].mu.RUnlock()
	}
	return out
}
