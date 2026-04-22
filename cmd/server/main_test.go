package main

import "testing"

func TestRedactURLStripsPassword(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "password is redacted",
			in:   "redis://default:hunter2@redis.example.com:6379/0",
			want: "redis://default:***@redis.example.com:6379/0",
		},
		{
			name: "no user info is passthrough",
			in:   "redis://redis:6379/0",
			want: "redis://redis:6379/0",
		},
		{
			name: "username only (no password) is passthrough",
			in:   "redis://user@redis:6379/0",
			want: "redis://user@redis:6379/0",
		},
		{
			name: "unparseable returns original",
			in:   "://not a url",
			want: "://not a url",
		},
		{
			name: "empty returns empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURL(tc.in)
			if got != tc.want {
				t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
