package main

import (
	"testing"
)

func TestSanitizeSessionName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"my-session", "my-session"},
		{"my:session.name", "my-session-name"},
		{"my session name", "my-session-name"},
		{"  my session  ", "my-session"},
		{"my\nsession\rname", "mysessionname"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeSessionName(c.in); got != c.want {
			t.Errorf("sanitizeSessionName(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
