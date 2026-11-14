package catalogs_test

import (
	"io/fs"
	"testing"

	"github.com/ArjunRachithcf/homerslab/catalogs"
	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The official catalog starts with three apps: BentoPDF, which the edge guards; Mealie, which takes the household
// account itself and is handed its client (#20); and Immich, which takes it too. Each has its icon, screenshots, a
// description and a category.
func TestTheOfficialCatalogHasAGuardedAppAndNativeOnes(t *testing.T) {
	testenv.Isolate(t)
	c, err := catalog.Load(catalogs.Official())
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "official" || len(c.Apps) != 3 {
		t.Fatalf("catalog %s with %d apps", c.ID, len(c.Apps))
	}
	want := map[string]catalog.SignInMethod{"immich": catalog.OIDC, "bentopdf": catalog.Guard, "mealie": catalog.OIDC}
	for _, a := range c.Apps {
		if m, ok := want[a.ID]; !ok || a.SignIn[0].Method != m {
			t.Errorf("%s signs in with %v", a.ID, a.SignIn)
		}
		if a.Description == "" || a.Category == "" || len(a.Screenshots) < 2 || a.Compose == nil {
			t.Errorf("%s: description %q, category %q, %d screenshots", a.ID, a.Description, a.Category, len(a.Screenshots))
		}
		for _, f := range append([]string{a.Icon}, a.Screenshots[0].File, a.Screenshots[1].File) {
			if b, err := fs.ReadFile(catalogs.Official(), "apps/"+a.ID+"/"+f); err != nil || len(b) < 100 {
				t.Errorf("%s: %s is missing or empty", a.ID, f)
			}
		}
	}
}
