package httpapi

import "testing"

// TestFilterListURLIsRefusedUnlessItIsHTTPS pins the one validation on this endpoint whose failure
// mode is irreversible.
//
// A filter list decides what a child's phone refuses to connect to. A list fetched over plain HTTP
// can be rewritten by anything on the path between the phone and the server hosting it, and one of
// the names it could then add is this control plane's own — after which the phone stops syncing and
// no parent can switch the filter off again. There is no remote remedy for that; the phone has to
// be set up from scratch.
//
// The device applies the same rule (FilterListStore), so a url accepted here that the phone refuses
// would be stored, shown in the console as the list in use, and quietly never load. Both ends
// refuse for the same reason and this is the end that can say why.
func TestFilterListURLIsRefusedUnlessItIsHTTPS(t *testing.T) {
	for _, ok := range []string{
		"",
		"   ",
		"https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt",
		"  https://lists.example.com/a.txt  ",
		"https://lists.example.com:8443/a.txt",
	} {
		if problem := filterListURLProblem(ok); problem != "" {
			t.Errorf("%q was refused with %q; empty means \"no list\" and https is the only scheme "+
				"the phone will fetch", ok, problem)
		}
	}

	for _, bad := range []string{
		"http://lists.example.com/a.txt",
		"HTTP://lists.example.com/a.txt",
		"ftp://lists.example.com/a.txt",
		"file:///etc/hosts",
		"lists.example.com/a.txt",
		"not a url at all",
		"https://",
		"://lists.example.com",
	} {
		if problem := filterListURLProblem(bad); problem == "" {
			t.Errorf("%q was accepted: a list the phone cannot fetch, or one anything on the path "+
				"can rewrite, must never reach the column a parent reads as the list in use", bad)
		}
	}
}
