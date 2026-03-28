package inbox

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	loremail "github.com/ioworker0/lore-reply/internal/mail"
)

func TestSyncPersistsThreadsAndDetail(t *testing.T) {
	t.Parallel()

	service := &Service{
		StorePath: filepath.Join(t.TempDir(), "inbox-state.json"),
		Discoverer: staticDiscoverer{
			hits: []SearchHit{
				{URL: "https://lore.kernel.org/r/root@example.com"},
			},
		},
		Loader: staticLoader{
			messages: []loremail.ThreadMessage{
				{
					MessageID: "root@example.com",
					Subject:   "[PATCH] mm: example",
					From:      "Alice Example <alice@example.com>",
					To:        "linux-mm@kvack.org",
					Date:      "2026-03-01T10:00:00Z",
					DateUnix:  1772359200,
					Body:      "Initial patch mail",
					URL:       "https://lore.kernel.org/r/root@example.com",
				},
				{
					MessageID: "reply@example.com",
					Subject:   "Re: [PATCH] mm: example",
					From:      "Reviewer <reviewer@example.com>",
					To:        "Alice Example <alice@example.com>",
					Date:      "2026-03-02T11:00:00Z",
					DateUnix:  1772449200,
					Body:      "Please fix one thing.",
					URL:       "https://lore.kernel.org/r/reply@example.com",
					InReplyTo: "root@example.com",
				},
			},
		},
	}

	result, err := service.Sync(context.Background(), SyncOptions{
		ListURL:     DefaultListURL,
		MyEmails:    []string{"alice@example.com"},
		RangeDays:   90,
		MatchMode:   MatchModeAllRelated,
		MaxMessages: 20,
	})
	if err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	if len(result.Threads) != 1 {
		t.Fatalf("unexpected thread count: %#v", result)
	}
	thread := result.Threads[0]
	if thread.ID != "root@example.com" {
		t.Fatalf("unexpected thread id: %#v", thread)
	}
	if !thread.HasFromMe {
		t.Fatalf("expected thread to include a message from me: %#v", thread)
	}
	if !thread.HasMention {
		t.Fatalf("expected thread to mention me: %#v", thread)
	}
	if !thread.NeedsReply {
		t.Fatalf("expected thread to need reply: %#v", thread)
	}
	if thread.MessageCount != 2 {
		t.Fatalf("unexpected message count: %#v", thread)
	}

	listed, err := service.ListThreads()
	if err != nil {
		t.Fatalf("ListThreads returned error: %v", err)
	}
	if len(listed.Threads) != 1 {
		t.Fatalf("unexpected listed thread count: %#v", listed)
	}

	detail, err := service.GetThread("root@example.com")
	if err != nil {
		t.Fatalf("GetThread returned error: %v", err)
	}
	if len(detail.Messages) != 2 {
		t.Fatalf("unexpected detail message count: %#v", detail)
	}
	if detail.Messages[0].MessageID != "root@example.com" {
		t.Fatalf("unexpected first message ordering: %#v", detail.Messages)
	}
	if detail.Messages[1].MessageID != "reply@example.com" {
		t.Fatalf("unexpected second message ordering: %#v", detail.Messages)
	}
}

func TestDecodeFeedSupportsASCIICharset(t *testing.T) {
	t.Parallel()

	response := &http.Response{
		Header: http.Header{
			"Content-Type": []string{"application/atom+xml"},
		},
		Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="us-ascii"?>
<feed>
  <entry>
    <link href="https://lore.kernel.org/r/root@example.com"></link>
  </entry>
</feed>`)),
	}

	var feed struct {
		Entries []struct {
			Link struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}

	if err := decodeFeed(response, &feed); err != nil {
		t.Fatalf("decodeFeed returned error: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Fatalf("unexpected feed entry count: %#v", feed)
	}
	if feed.Entries[0].Link.Href != "https://lore.kernel.org/r/root@example.com" {
		t.Fatalf("unexpected feed link: %#v", feed.Entries[0])
	}
}

type staticDiscoverer struct {
	hits []SearchHit
}

func (d staticDiscoverer) Discover(_ context.Context, _ DiscoveryQuery) ([]SearchHit, error) {
	return append([]SearchHit(nil), d.hits...), nil
}

type staticLoader struct {
	messages []loremail.ThreadMessage
}

func (l staticLoader) LoadThread(_ context.Context, _ string) ([]loremail.ThreadMessage, error) {
	return append([]loremail.ThreadMessage(nil), l.messages...), nil
}
