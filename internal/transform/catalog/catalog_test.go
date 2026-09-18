package catalog_test

// Behavioral tests for the transform registry, anchored to AC-28 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md ("internal/transform defines
// Contract{Name, Inputs, Output, Grain, Tests} ... and a registry the orchestrator can list.
// issue_mentions declares inputs [dim_issues], output issue_mentions, grain Day. Check: unit test
// over the registry.") and tasks/07-go-transform/brief.md's Required content, which pins the
// registry's exact exported API:
//
//	func All() []transform.Transform   // sorted by name
//	func Lookup(name string) (transform.Transform, bool)
//
// This is a black-box test (package catalog_test): every assertion goes through catalog's
// exported API and each returned Transform's own Contract(), never any unexported registry state.
//
// Until catalog.go (and, transitively, mentions.go/extract.go/transform.go) exist, this package
// fails to build because it imports both "internal/transform/catalog" and "internal/transform",
// neither of which currently has any non-test Go file -- the expected Mode-A result: the entire
// registry feature this file exercises is genuinely absent, not a mistake in this file's own
// (correct, brief-specified) import paths.

import (
	"reflect"
	"sort"
	"testing"

	"github.com/DMokong/data-platform/internal/transform"
	"github.com/DMokong/data-platform/internal/transform/catalog"
	"github.com/DMokong/data-platform/internal/window"
)

// names extracts each transform's Contract().Name, in slice order.
func names(all []transform.Transform) []string {
	out := make([]string, len(all))
	for i, tr := range all {
		out[i] = tr.Contract().Name
	}
	return out
}

// containsStr reports whether want appears anywhere in got.
func containsStr(got []string, want string) bool {
	for _, s := range got {
		if s == want {
			return true
		}
	}
	return false
}

// AC-28: the registry lists issue_mentions, declaring exactly the inputs, output and grain the AC
// text pins: inputs [dim_issues], output issue_mentions, grain Day.
func TestAll_ListsIssueMentionsWithExactContract_AC28(t *testing.T) {
	all := catalog.All()

	var found *transform.Contract
	for _, tr := range all {
		c := tr.Contract()
		if c.Name == "issue_mentions" {
			found = &c
			break
		}
	}
	if found == nil {
		t.Fatalf("catalog.All() = %v, want an entry named %q", names(all), "issue_mentions")
	}

	if got, want := found.Inputs, []string{"dim_issues"}; !reflect.DeepEqual(got, want) {
		t.Errorf("issue_mentions Contract.Inputs = %v, want %v", got, want)
	}
	if found.Output != "issue_mentions" {
		t.Errorf("issue_mentions Contract.Output = %q, want %q", found.Output, "issue_mentions")
	}
	if found.Grain != window.Day {
		t.Errorf("issue_mentions Contract.Grain = %v, want window.Day", found.Grain)
	}

	// Contract.Tests names the dbt tests guarding this output; the brief's own Required content
	// gives these two literal values for issue_mentions specifically (not just as a generic
	// illustration -- they match the real source_freshness check on source `derived` and the real
	// mart name `mart_issue_mention_drift` this same task declares), so this checks their
	// presence. It does not require these to be the *only* entries, so a later round can extend
	// Tests without breaking this check.
	for _, want := range []string{"source_freshness:derived.issue_mentions", "mart_issue_mention_drift"} {
		if !containsStr(found.Tests, want) {
			t.Errorf("issue_mentions Contract.Tests = %v, want it to contain %q", found.Tests, want)
		}
	}
}

// AC-28: "func All() []transform.Transform" is documented as sorted by name. With today's single
// registered transform this check cannot discriminate a real ordering bug (any one-element slice
// is trivially "sorted"); it is written to hold regardless, so it starts enforcing the guarantee
// the moment a second transform is registered, without needing to change then.
func TestAll_SortedByName_AC28(t *testing.T) {
	all := catalog.All()
	got := names(all)
	if !sort.StringsAreSorted(got) {
		t.Errorf("catalog.All() names = %v, want sorted by name", got)
	}
}

// AC-28: Lookup finds the registered issue_mentions transform by name, and its Contract agrees
// with the one catalog.All() reports for the same name.
func TestLookup_FindsIssueMentions_AC28(t *testing.T) {
	tr, ok := catalog.Lookup("issue_mentions")
	if !ok {
		t.Fatal(`catalog.Lookup("issue_mentions") ok = false, want true`)
	}
	if got := tr.Contract().Name; got != "issue_mentions" {
		t.Errorf(`catalog.Lookup("issue_mentions").Contract().Name = %q, want %q`, got, "issue_mentions")
	}
}

// AC-28: an unregistered name is reported as not found, not as a zero-value success.
func TestLookup_UnknownNameNotFound_AC28(t *testing.T) {
	if _, ok := catalog.Lookup("does_not_exist"); ok {
		t.Error(`catalog.Lookup("does_not_exist") ok = true, want false`)
	}
}

// AC-28: Lookup and All agree -- "a registry the orchestrator can list" must let an orchestrator
// list every name via All() and then resolve each one back to the same transform via Lookup(),
// which is exactly how task 09's orchestrator is expected to use this package.
func TestLookup_AgreesWithAll_AC28(t *testing.T) {
	for _, tr := range catalog.All() {
		name := tr.Contract().Name
		got, ok := catalog.Lookup(name)
		if !ok {
			t.Errorf("catalog.Lookup(%q) ok = false, want true (name came from catalog.All())", name)
			continue
		}
		if got.Contract().Name != name {
			t.Errorf("catalog.Lookup(%q).Contract().Name = %q, want %q", name, got.Contract().Name, name)
		}
	}
}
