// The end-to-end suite is its own module, and it deliberately has no dependencies at all.
//
// Separate module: it cannot import the server's packages, so it cannot drift into checking the
// server against the server's own constants. Everything it knows about the API is written out here
// in the same form a third-party client would have to write it.
//
// No dependencies: the ID tokens are signed with crypto/rsa and encoding/json rather than with the
// same JWT library the server verifies with. A suite that mints tokens with the library under test
// agrees with that library's bugs by construction.
module familyguard.local/e2e

go 1.27
