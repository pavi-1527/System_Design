//go:build breakit

package tenancy

// Built with -tags breakit: Store.List forgets the tenant predicate (it scans
// every partition). Run
//
//	go test -tags breakit -run 'BreakIt_CrossTenantLeak' -v ./solutions/a9/tenancy/
//
// to see how many foreign rows one tenant can read, and
//
//	go test -tags breakit ./solutions/a9/tenancy/
//
// to watch the isolation tests catch it.
const breakNoTenantPredicate = true
