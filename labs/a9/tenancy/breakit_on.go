//go:build breakit

package tenancy

// Built with -tags breakit: wire this constant into your Store.List so that,
// when it is true, List forgets the tenant (for example by scanning every
// tenant's rows, like a hurried "global search" helper). Then run
//
//	go test -tags breakit -run 'BreakIt_CrossTenantLeak' -v ./labs/a9/tenancy/
//
// to see how many foreign rows one tenant can read, and
//
//	go test -tags breakit ./labs/a9/tenancy/
//
// to watch the isolation tests catch it.
const breakNoTenantPredicate = true
