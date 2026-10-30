//go:build breakit

package cacheaside

// Built with -tags breakit: Cache.Get skips Group.Do and calls the loader
// directly from every caller (no stampede protection), and a load that was
// invalidated while in flight stores its result anyway (the stale-set race).
// Run
//
//	go test -tags breakit -race -run 'Cache_ColdKey|Cache_Invalidate' -v ./labs/a3/cacheaside/
//
// and watch those tests fail.
const (
	breakNoSingleflight    = true
	breakNoInvalidateGuard = true
)
