//go:build !breakit

package envelope

// breakNoAAD drops the associated data (tenant and object binding) from every
// Seal and Open when the package is built with -tags breakit (see
// breakit_on.go). Default: off.
const breakNoAAD = false
