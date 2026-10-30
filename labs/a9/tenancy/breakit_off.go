//go:build !breakit

package tenancy

// breakNoTenantPredicate makes Store.List ignore the caller's tenant when the
// package is built with -tags breakit (see breakit_on.go). Default: off.
const breakNoTenantPredicate = false
