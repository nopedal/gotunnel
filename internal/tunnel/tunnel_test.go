package tunnel

import (
	"strings"
	"testing"
)

func TestNormalizeSubdomain(t *testing.T) {
	valid := map[string]string{
		"myapp":                 "myapp",
		"MyApp":                 "myapp",
		"my-app-2":              "my-app-2",
		"a":                     "a",
		"0day":                  "0day",
		strings.Repeat("a", 63): strings.Repeat("a", 63),
	}
	for in, want := range valid {
		got, err := NormalizeSubdomain(in)
		if err != nil || got != want {
			t.Errorf("NormalizeSubdomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	invalid := []string{
		"",
		"a.b",
		"-app",
		"app-",
		"my_app",
		"my app",
		"app/..",
		"café",
		strings.Repeat("a", 64),
	}
	for _, in := range invalid {
		if got, err := NormalizeSubdomain(in); err == nil {
			t.Errorf("NormalizeSubdomain(%q) = %q, want error", in, got)
		}
	}
}

func TestGenerateSlugIsValidSubdomain(t *testing.T) {
	for i := 0; i < 100; i++ {
		slug := GenerateSlug()
		if got, err := NormalizeSubdomain(slug); err != nil || got != slug {
			t.Fatalf("generated slug %q is not a valid subdomain: %v", slug, err)
		}
	}
}
