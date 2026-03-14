package main

import "testing"

func TestBrowserURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		want    string
	}{
		{
			name:    "loopback",
			address: "127.0.0.1:9110",
			want:    "http://127.0.0.1:9110",
		},
		{
			name:    "wildcard ipv4",
			address: "0.0.0.0:9110",
			want:    "http://127.0.0.1:9110",
		},
		{
			name:    "wildcard ipv6",
			address: "[::]:9110",
			want:    "http://127.0.0.1:9110",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := browserURL(test.address)
			if got != test.want {
				t.Fatalf("unexpected browser URL:\nwant: %s\ngot:  %s", test.want, got)
			}
		})
	}
}
