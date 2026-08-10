// Package release finds the newest published release of a tagged stream on
// GitHub.
//
// This is a deliberate port of server/internal/versioncheck, not an import: the
// server and the CLI are separate modules and that package lives under the
// server's internal/, so it is structurally unreachable from here. The
// filtering and comparison rules are kept identical so a tag is judged newer by
// the same standard on both sides, and the tests below mirror that package's.
//
// One rule is deliberately NOT shared. versioncheck.isNewer treats a `-dev`
// build as "anything is newer", which is right for a dashboard banner nudging
// an operator onto a release. It is wrong for something that replaces the
// running application: a development build is usually newer than the last
// release, and offering to overwrite it with an older one is a regression
// disguised as an update. Callers here skip the check for dev builds instead.
package release

import (
	"strconv"
	"strings"
)

// CompareSemver compares two MAJOR.MINOR.PATCH strings numerically, returning
// -1, 0 or 1. Non-numeric components fall back to a lexicographic comparison —
// a safety net, since the release filter rejects anything that is not plain
// numeric to begin with.
func CompareSemver(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	n := max(len(pa), len(pb))

	for i := range n {
		ai, aOK := 0, true
		bi, bOK := 0, true
		if i < len(pa) {
			ai, aOK = atoi(pa[i])
		}
		if i < len(pb) {
			bi, bOK = atoi(pb[i])
		}
		if aOK && bOK {
			if ai != bi {
				if ai < bi {
					return -1
				}
				return 1
			}
			continue
		}
		var as, bs string
		if i < len(pa) {
			as = pa[i]
		}
		if i < len(pb) {
			bs = pb[i]
		}
		if as != bs {
			if as < bs {
				return -1
			}
			return 1
		}
	}
	return 0
}

// IsNewer reports whether latest is a strictly newer version than current.
//
// Unlike the server's equivalent, an unparseable current version is NOT treated
// as "anything is newer" — see the package comment. Here it means "do not
// offer", because the only safe answer when we cannot tell which build is newer
// is to leave the installed one alone.
func IsNewer(current, latest string) bool {
	if latest == "" {
		return false
	}
	cur := strings.TrimPrefix(current, "v")
	if cur == "" || !looksNumeric(cur) {
		return false
	}
	return CompareSemver(latest, cur) > 0
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// looksNumeric reports whether every dot-separated component parses as a
// number — i.e. the string is a plain MAJOR.MINOR.PATCH with no suffix.
func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	for part := range strings.SplitSeq(s, ".") {
		if _, ok := atoi(part); !ok {
			return false
		}
	}
	return true
}
