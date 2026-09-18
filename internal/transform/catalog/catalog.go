package catalog

import (
	"sort"

	"github.com/DMokong/data-platform/internal/transform"
	"github.com/DMokong/data-platform/internal/transform/mentions"
)

// registered lists every Go transform this build knows about. Adding a transform means adding
// one entry here; nothing else discovers transforms implicitly.
var registered = []transform.Transform{
	mentions.Transform{},
}

// All returns every registered transform, sorted by Contract().Name.
func All() []transform.Transform {
	all := make([]transform.Transform, len(registered))
	copy(all, registered)
	sort.Slice(all, func(i, j int) bool {
		return all[i].Contract().Name < all[j].Contract().Name
	})
	return all
}

// Lookup finds a registered transform by its Contract().Name.
func Lookup(name string) (transform.Transform, bool) {
	for _, t := range registered {
		if t.Contract().Name == name {
			return t, true
		}
	}
	return nil, false
}
