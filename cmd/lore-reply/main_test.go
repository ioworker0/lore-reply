package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ioworker0/lore-reply/internal/config"
)

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

func TestWithIdleSupportHeartbeat(t *testing.T) {
	t.Parallel()

	tracker := newIdleTracker(true)
	handler := withIdleSupport(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Fatal("heartbeat request should not reach the wrapped handler")
	}), tracker)

	request := httptest.NewRequest(http.MethodPost, heartbeatPath, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
	if tracker.IdleFor(time.Now()) > time.Second {
		t.Fatalf("heartbeat did not refresh tracker activity")
	}
}

func TestShouldOpenBrowser(t *testing.T) {
	t.Parallel()

	if !shouldOpenBrowser(config.Config{}) {
		t.Fatal("expected browser to open by default")
	}

	if !shouldOpenBrowser(config.Config{AutoLoadURL: "https://lore.kernel.org/linux-mm/test"}) {
		t.Fatal("expected browser to open when auto-load URL is set")
	}
}
