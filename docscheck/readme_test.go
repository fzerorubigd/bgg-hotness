package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// fzerorubigdRef matches any reference to a github.com/fzerorubigd/<repo> path,
// whether it appears as a bare module path or inside an https:// URL. The repo
// segment stops at the first character that cannot be part of a path element, so
// a link like https://github.com/fzerorubigd/bggo) yields github.com/fzerorubigd/bggo.
var fzerorubigdRef = regexp.MustCompile(`github\.com/fzerorubigd/[a-zA-Z0-9._-]+`)

// TestREADMEReferencesMatchGoMod fails when the README names a
// github.com/fzerorubigd/... path that go.mod does not contain. The check is
// mechanical rather than careful-reading because the pair that actually drifted
// here — gobgg vs bggo — differs only by a transposition, so a human who does
// open go.mod to verify can read one identifier and see the other; the manual
// check gets performed and still passes. Machine string comparison is the only
// reliable detector for that class, so the claim is derived, not re-read.
//
// It is deliberately strict: any fzerorubigd link the module does not depend on
// is treated as drift, since "the front page names a library we no longer use"
// is precisely the failure this guards. If an intentional reference to a
// non-dependency fzerorubigd repo is ever wanted in the README, that is a
// deliberate change to this test, not something it should silently permit.
func TestREADMEReferencesMatchGoMod(t *testing.T) {
	root := repoRoot(t)

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	// Build the set of github.com/fzerorubigd/... paths go.mod actually declares —
	// its own module line and every require entry — with the same regexp, then test
	// membership EXACTLY. Not substring containment: the pair this guard exists for,
	// gobgg vs bggo, is a near-miss, and a substring test passes whenever a referenced
	// path is a prefix of a real one (bgg ⊂ bggo, bggo ⊂ bggo-renamed) — which reproduces
	// the very failure the guard was added to catch, now under a green check, strictly
	// worse than the manual read it replaces. A near-miss detector has to be exact.
	// Deriving the set from the same regexp also makes the "a reference to this module's
	// own path always satisfies the check" behaviour explicit rather than incidental.
	declared := map[string]bool{}
	for _, p := range fzerorubigdRef.FindAllString(string(gomod), -1) {
		declared[p] = true
	}

	refs := fzerorubigdRef.FindAllString(string(readme), -1)
	if len(refs) == 0 {
		t.Fatal("expected at least one github.com/fzerorubigd/... reference in README.md; " +
			"if the README genuinely names no such module the regexp or this guard has drifted")
	}

	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if !declared[ref] {
			t.Errorf("README.md references %q but go.mod declares no such module path (exact match) — "+
				"stale after a dependency rename/migration, or a link to a repo this module does not use", ref)
		}
	}
}

// repoRoot returns the module root, located relative to this test file rather
// than the working directory so the test passes under `go test ./...` from any
// starting directory. This file lives in <root>/docscheck, so the parent is root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to locate this test file")
	}
	return filepath.Dir(filepath.Dir(thisFile))
}
