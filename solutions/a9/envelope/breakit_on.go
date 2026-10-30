//go:build breakit

package envelope

// Built with -tags breakit: ciphertexts are no longer bound to their tenant
// and object (nil AAD). Run
//
//	go test -tags breakit -run BreakIt_SwapWithoutAAD -v ./solutions/a9/envelope/
//
// to see a cross-tenant ciphertext swap succeed, and
//
//	go test -tags breakit ./solutions/a9/envelope/
//
// to see which tests notice.
const breakNoAAD = true
