package data

import "testing"

func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		"林晚":      "林晚",
		"100%":    `100\%`,
		"a_b":     `a\_b`,
		`c:\tmp`:  `c:\\tmp`,
		`\%_`:     `\\\%\_`,
		"x@y.com": "x@y.com",
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}
