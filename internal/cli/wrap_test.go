package cli

import "testing"

func TestMergeStreams(t *testing.T) {
	cases := []struct {
		name           string
		stdout, stderr string
		want           string
	}{
		{"both newline-terminated", "body line\n", "log line\n", "body line\nlog line\n"},
		{"unterminated stdout gets separator", "{\"k\":\"v\"}", "Resolving host...\n", "{\"k\":\"v\"}\nResolving host...\n"},
		{"empty stdout passes stderr through", "", "log line\n", "log line\n"},
		{"empty stderr passes stdout through", "body", "", "body"},
		{"both empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergeStreams(c.stdout, c.stderr); got != c.want {
				t.Errorf("mergeStreams(%q, %q) = %q, want %q", c.stdout, c.stderr, got, c.want)
			}
		})
	}
}
