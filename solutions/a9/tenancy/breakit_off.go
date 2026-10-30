//go:build !breakit

package tenancy

// breakNoTenantPredicate makes Store.List scan every tenant's partition when
// the package is built with -tags breakit (see breakit_on.go). Default: off.
const breakNoTenantPredicate = false
