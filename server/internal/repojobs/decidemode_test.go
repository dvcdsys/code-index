package repojobs

import (
	"testing"

	"github.com/dvcdsys/code-index/server/internal/repocloner"
)

// The mode decision is the one part of handleClone that can be tested without
// a live git remote, which is why it was pulled out. Cases are ordered the way
// the switch is: each row also asserts the priority of its branch over the
// ones below it.
func TestDecideMode(t *testing.T) {
	plain := &repocloner.ChangeSet{Modified: []string{"main.go"}, Added: []string{"pkg/new.go"}}
	withCix := &repocloner.ChangeSet{Modified: []string{"main.go", ".cixignore"}}
	withGit := &repocloner.ChangeSet{Modified: []string{".gitignore"}}
	nestedGit := &repocloner.ChangeSet{Added: []string{"sub/pkg/.gitignore"}}
	removedCix := &repocloner.ChangeSet{Deleted: []string{".cixignore"}}

	cases := []struct {
		name         string
		forceFull    bool
		indexedSHA   string
		changes      *repocloner.ChangeSet
		modelChanged bool
		wantMode     string
		wantReason   string
	}{
		{"plain diff stays incremental", false, "abc", plain, false, ModeIncremental, "tree-diff"},

		{"cixignore edited", false, "abc", withCix, false, ModeReconcile, "ignore-rules-changed"},
		{"gitignore edited", false, "abc", withGit, false, ModeReconcile, "ignore-rules-changed"},
		{"nested gitignore added", false, "abc", nestedGit, false, ModeReconcile, "ignore-rules-changed"},
		// Removing a rule is the un-ignore direction: only a full walk can
		// find the files that have to come back.
		{"cixignore removed", false, "abc", removedCix, false, ModeReconcile, "ignore-rules-changed"},

		// The escalation must lose to every branch that already walks or
		// wipes the whole tree — running reconcile instead of full would skip
		// the wipe those cases exist for.
		{"force-full wins over ignore change", true, "abc", withCix, false, ModeFull, "force-full"},
		{"model change wins over ignore change", false, "abc", withCix, true, ModeFull, "model-change"},
		{"first index wins over ignore change", false, "", withCix, false, ModeReconcile, "first-or-resume"},
		{"nil changeset wins over ignore change", false, "abc", nil, false, ModeReconcile, "no-changeset"},

		// Regressions guards for the pre-existing branches.
		{"force-full", true, "abc", plain, false, ModeFull, "force-full"},
		{"first or resume", false, "", plain, false, ModeReconcile, "first-or-resume"},
		{"no changeset", false, "abc", nil, false, ModeReconcile, "no-changeset"},
		{"model change", false, "abc", plain, true, ModeFull, "model-change"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, reason := decideMode(c.forceFull, c.indexedSHA, c.changes, c.modelChanged)
			if mode != c.wantMode || reason != c.wantReason {
				t.Errorf("decideMode = (%s, %s), want (%s, %s)", mode, reason, c.wantMode, c.wantReason)
			}
		})
	}
}

func TestChangesTouchIgnoreFile(t *testing.T) {
	cases := []struct {
		name string
		cs   *repocloner.ChangeSet
		want bool
	}{
		{"nil", nil, false},
		{"empty", &repocloner.ChangeSet{}, false},
		{"ordinary paths", &repocloner.ChangeSet{Modified: []string{"a/b.go", "README.md"}}, false},
		{"root cixignore", &repocloner.ChangeSet{Modified: []string{".cixignore"}}, true},
		{"root gitignore", &repocloner.ChangeSet{Added: []string{".gitignore"}}, true},
		{"nested", &repocloner.ChangeSet{Deleted: []string{"a/b/.cixignore"}}, true},
		// The collector never descends into these, so an ignore file inside
		// one cannot change any indexing decision — escalating would buy a
		// whole-tree reconcile for nothing.
		{"under vendor", &repocloner.ChangeSet{Modified: []string{"vendor/dep/.gitignore"}}, false},
		{"under node_modules", &repocloner.ChangeSet{Added: []string{"node_modules/pkg/.cixignore"}}, false},
		{"under .git", &repocloner.ChangeSet{Modified: []string{".git/.gitignore"}}, false},
		{"excluded and real", &repocloner.ChangeSet{Modified: []string{"vendor/dep/.gitignore", ".cixignore"}}, true},
		// A file that merely looks like one must not trigger a full walk.
		{"lookalike", &repocloner.ChangeSet{Modified: []string{"docs/gitignore.md", ".gitignore.bak"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := changesTouchIgnoreFile(c.cs); got != c.want {
				t.Errorf("changesTouchIgnoreFile = %v, want %v", got, c.want)
			}
		})
	}
}
