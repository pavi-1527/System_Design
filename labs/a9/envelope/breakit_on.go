//go:build breakit

package envelope

// Built with -tags breakit: wire this constant into your Seal and Open so
// that, when it is true, ciphertexts are no longer bound to their tenant and
// object (pass nil as the AAD). Run
//
//	go test -tags breakit -run BreakIt_SwapWithoutAAD -v ./labs/a9/envelope/
//
// to see a cross-tenant ciphertext swap succeed, and
//
//	go test -tags breakit ./labs/a9/envelope/
//
// to see which tests notice.
const breakNoAAD = true
