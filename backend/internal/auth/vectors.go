package auth

import _ "embed"

// RecoveryVectors is the shared recovery-code vector file, replayed by TestRecoveryVectors here and
// by the DPC's RecoveryVectorsTest from a copy the Gradle build stages onto the unit-test classpath.
// One file, two suites: a fold or a work factor the two sides disagree about is a red build rather
// than a phone that rejects the code the console printed.
//
// Embedded rather than read from disk by the test, for the reason measured on internal/policy's
// vectors: while that file lived outside the module, `go test` served a *cached pass* across an edit
// to it, so adding a vector the code fails could still print "ok". Embedding makes it a build input,
// and any change now invalidates the cache.
//
//go:embed recovery-vectors.json
var RecoveryVectors []byte
