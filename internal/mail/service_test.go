package mail

import (
	"strings"
	"testing"
)

func TestParseMessageContent(t *testing.T) {
	t.Parallel()

	data := []byte(`From nobody Mon Jan 01 00:00:00 2024
From: Example Author <author@example.com>
To: linux-mm@kvack.org, Andrew Morton <akpm@example.com>
Cc: Jane Doe <jane@example.com>
Subject: [PATCH v2 1/2] mm: add test
Message-ID: <abc.123@example.com>
Date: Sun, 01 Feb 2026 09:20:35 -0500
Content-Transfer-Encoding: quoted-printable

Line one=
 line continued

Second line
-- 
Signature
`)

	parsed, err := parseMessageContent(data)
	if err != nil {
		t.Fatalf("parseMessageContent returned error: %v", err)
	}

	if parsed.MessageID != "abc.123@example.com" {
		t.Fatalf("unexpected message id: %q", parsed.MessageID)
	}
	if parsed.AuthorName != "Example Author" {
		t.Fatalf("unexpected author name: %q", parsed.AuthorName)
	}
	if parsed.CitationDate != "Sun, Feb 01, 2026 at 09:20:35AM -0500" {
		t.Fatalf("unexpected citation date: %q", parsed.CitationDate)
	}
	if parsed.To != "\"Example Author\" <author@example.com>" {
		t.Fatalf("unexpected To header: %q", parsed.To)
	}
	if parsed.Cc != "linux-mm@kvack.org, \"Andrew Morton\" <akpm@example.com>, \"Jane Doe\" <jane@example.com>" {
		t.Fatalf("unexpected Cc header: %q", parsed.Cc)
	}
	if want := ">Line one line continued\n>\n>Second line"; parsed.QuotedBody != want {
		t.Fatalf("unexpected quoted body:\nwant:\n%q\ngot:\n%q", want, parsed.QuotedBody)
	}
}

func TestBuildDraftPath(t *testing.T) {
	t.Parallel()

	path := buildDraftPath("/tmp/lore-reply/drafts", "abc.123@example.com", "[PATCH] mm: add test")
	want := "/tmp/lore-reply/drafts/reply-abc-123-example-com-mm-add-test.txt"
	if path != want {
		t.Fatalf("unexpected path:\nwant: %s\ngot:  %s", want, path)
	}
}

func TestRenderDraftFile(t *testing.T) {
	t.Parallel()

	draft := Draft{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
		Subject:   "Re: [PATCH] mm: add test",
		Body:      "\nOn Sun, Feb 01, 2026 at 09:20:35AM -0500, Example Author wrote:\n>text",
	}

	content, err := renderDraftFile(draft)
	if err != nil {
		t.Fatalf("renderDraftFile returned error: %v", err)
	}

	if !strings.Contains(content, "From: Alice Example <alice@example.com>") {
		t.Fatalf("rendered content misses From header:\n%s", content)
	}
	if !strings.Contains(content, "Subject: Re: [PATCH] mm: add test") {
		t.Fatalf("rendered content misses Subject header:\n%s", content)
	}
	if !strings.Contains(content, "\n\n\nOn Sun, Feb 01, 2026 at 09:20:35AM -0500, Example Author wrote:\n>text\n") {
		t.Fatalf("rendered body did not preserve reply style:\n%s", content)
	}
}

func TestBuildSendCommand(t *testing.T) {
	t.Parallel()

	draft := Draft{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
		To:        `Example Author <author@example.com>`,
		Cc:        `linux-mm@kvack.org, Andrew Morton <akpm@example.com>, Jane Doe <jane@example.com>`,
		MessageID: "abc.123@example.com",
	}

	command := buildSendCommand(draft, "/tmp/lore-reply/drafts/reply-abc.txt")
	wantParts := []string{
		"git send-email \\",
		"    --from=\"Alice Example <alice@example.com>\" \\",
		"    --in-reply-to=\"abc.123@example.com\" \\",
		"    --to=\"author@example.com\" \\",
		"    --cc=\"linux-mm@kvack.org\" \\",
		"    --cc=\"akpm@example.com\" \\",
		"    --cc=\"jane@example.com\" \\",
		"    \"/tmp/lore-reply/drafts/reply-abc.txt\"",
	}
	for _, part := range wantParts {
		if !strings.Contains(command, part) {
			t.Fatalf("command missing %q:\n%s", part, command)
		}
	}
}
