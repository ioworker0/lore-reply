package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestNewDraft(t *testing.T) {
	t.Parallel()

	service := Service{
		DraftsDir: t.TempDir(),
	}

	draft, err := service.NewDraft(NewOptions{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("NewDraft returned error: %v", err)
	}

	if draft.FromName != "Alice Example" || draft.FromEmail != "alice@example.com" {
		t.Fatalf("unexpected new draft identity: %#v", draft)
	}
	if draft.MessageID != "" {
		t.Fatalf("new draft should not have message id: %#v", draft)
	}
	if draft.SourceURL != "" {
		t.Fatalf("new draft should not have source url: %#v", draft)
	}
	if !filepath.IsAbs(draft.DraftPath) {
		t.Fatalf("new draft path is not absolute: %q", draft.DraftPath)
	}
	if !strings.HasPrefix(filepath.Base(draft.DraftPath), "compose-") {
		t.Fatalf("unexpected new draft path: %q", draft.DraftPath)
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

func TestSaveDraftWithoutMessageID(t *testing.T) {
	t.Parallel()

	service := Service{
		DraftsDir: t.TempDir(),
	}

	result, err := service.SaveDraft(Draft{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
		To:        `Example Reviewer <reviewer@example.com>`,
		Cc:        `list@example.com`,
		Subject:   "[PATCH 0/1] Example cover letter",
		Body:      "brand-new mail body",
	})
	if err != nil {
		t.Fatalf("SaveDraft returned error: %v", err)
	}

	if !filepath.IsAbs(result.DraftPath) {
		t.Fatalf("saved draft path is not absolute: %q", result.DraftPath)
	}
	if !strings.HasPrefix(filepath.Base(result.DraftPath), "compose-") {
		t.Fatalf("unexpected saved draft path: %q", result.DraftPath)
	}
	if strings.Contains(result.SendCommand, "--in-reply-to") {
		t.Fatalf("new mail send command unexpectedly contains in-reply-to:\n%s", result.SendCommand)
	}
	if !strings.Contains(result.SendCommand, "--to=\"reviewer@example.com\"") {
		t.Fatalf("send command misses to recipient:\n%s", result.SendCommand)
	}

	savedData, err := os.ReadFile(result.DraftPath)
	if err != nil {
		t.Fatalf("read saved draft: %v", err)
	}
	if !strings.Contains(string(savedData), "Subject: [PATCH 0/1] Example cover letter") {
		t.Fatalf("saved draft misses subject:\n%s", string(savedData))
	}
}

func TestLoadDraftRestoresSavedDraft(t *testing.T) {
	t.Parallel()

	service := Service{
		B4Path:    writeTestB4(t),
		DraftsDir: t.TempDir(),
	}

	draft, err := service.LoadDraft(context.Background(), LoadOptions{
		URL:       "https://lore.kernel.org/linux-mm/test",
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("LoadDraft returned error: %v", err)
	}

	draft.FromName = "Saved Name"
	draft.FromEmail = "saved@example.com"
	draft.To = `"Saved Reviewer" <saved-to@example.com>`
	draft.Cc = `saved-cc@example.com`
	draft.Subject = "Re: [PATCH] Saved subject"
	draft.Body = "saved body"

	result, err := service.SaveDraft(draft)
	if err != nil {
		t.Fatalf("SaveDraft returned error: %v", err)
	}

	restored, err := service.LoadDraft(context.Background(), LoadOptions{
		URL:       "https://lore.kernel.org/linux-mm/test",
		FromName:  "Ignored Name",
		FromEmail: "ignored@example.com",
	})
	if err != nil {
		t.Fatalf("LoadDraft restore returned error: %v", err)
	}

	if restored.DraftPath != result.DraftPath {
		t.Fatalf("unexpected restored draft path:\nwant: %s\ngot:  %s", result.DraftPath, restored.DraftPath)
	}
	if restored.FromName != "Saved Name" || restored.FromEmail != "saved@example.com" {
		t.Fatalf("saved identity was not restored: %#v", restored)
	}
	if restored.To != `"Saved Reviewer" <saved-to@example.com>` {
		t.Fatalf("saved To header was not restored: %q", restored.To)
	}
	if restored.Cc != "saved-cc@example.com" {
		t.Fatalf("saved Cc header was not restored: %q", restored.Cc)
	}
	if restored.Subject != "Re: [PATCH] Saved subject" {
		t.Fatalf("saved subject was not restored: %q", restored.Subject)
	}
	if restored.Body != "saved body" {
		t.Fatalf("saved body was not restored: %q", restored.Body)
	}

	if _, err := os.Stat(result.DraftPath + ".meta.json"); err != nil {
		t.Fatalf("draft metadata file missing: %v", err)
	}

	reloaded, err := service.LoadDraft(context.Background(), LoadOptions{
		URL:         "https://lore.kernel.org/linux-mm/test",
		FromName:    "Fresh Name",
		FromEmail:   "fresh@example.com",
		ForceReload: true,
	})
	if err != nil {
		t.Fatalf("LoadDraft force reload returned error: %v", err)
	}

	if reloaded.FromName != "Fresh Name" || reloaded.FromEmail != "fresh@example.com" {
		t.Fatalf("force reload did not use fresh identity: %#v", reloaded)
	}
	if reloaded.To != `"Example Author" <author@example.com>` {
		t.Fatalf("force reload did not restore original To: %q", reloaded.To)
	}
	if reloaded.Cc != `linux-mm@kvack.org, "Andrew Morton" <akpm@example.com>, "Jane Doe" <jane@example.com>` {
		t.Fatalf("force reload did not restore original Cc: %q", reloaded.Cc)
	}
	if reloaded.Body == "saved body" {
		t.Fatalf("force reload unexpectedly restored saved body")
	}
	if _, err := os.Stat(result.DraftPath); !os.IsNotExist(err) {
		t.Fatalf("force reload should remove the old saved draft, got err=%v", err)
	}

	afterRefresh, err := service.LoadDraft(context.Background(), LoadOptions{
		URL:       "https://lore.kernel.org/linux-mm/test",
		FromName:  "Refreshed Name",
		FromEmail: "refreshed@example.com",
	})
	if err != nil {
		t.Fatalf("LoadDraft after force reload returned error: %v", err)
	}

	if afterRefresh.Body == "saved body" {
		t.Fatalf("refresh-style load unexpectedly restored the old saved body")
	}
}

func TestSendDraft(t *testing.T) {
	t.Parallel()

	gitPath, argsPath := writeTestGit(t)
	service := Service{
		GitPath:   gitPath,
		DraftsDir: t.TempDir(),
	}

	draft := Draft{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
		To:        `"Example Author" <author@example.com>`,
		Cc:        `linux-mm@kvack.org, Jane Doe <jane@example.com>`,
		Subject:   "Re: [PATCH] Example patch",
		Body:      "reply body",
		MessageID: "abc.123@example.com",
	}

	result, err := service.SendDraft(context.Background(), draft)
	if err != nil {
		t.Fatalf("SendDraft returned error: %v", err)
	}

	if !strings.Contains(result.Output, "Result: OK") {
		t.Fatalf("send output missing success marker:\n%s", result.Output)
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read git args: %v", err)
	}
	args := string(argsData)
	for _, want := range []string{
		"send-email\n",
		"--confirm=never\n",
		"--from=Alice Example <alice@example.com>\n",
		"--in-reply-to=abc.123@example.com\n",
		"--to=author@example.com\n",
		"--cc=linux-mm@kvack.org\n",
		"--cc=jane@example.com\n",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("git args missing %q:\n%s", want, args)
		}
	}

	savedData, err := os.ReadFile(result.DraftPath)
	if err != nil {
		t.Fatalf("read saved draft: %v", err)
	}
	if !strings.Contains(string(savedData), "reply body") {
		t.Fatalf("saved draft missing reply body:\n%s", string(savedData))
	}
}

func TestSendDraftReturnsOutputOnFailure(t *testing.T) {
	t.Parallel()

	gitPath, _ := writeTestGit(t)
	service := Service{
		GitPath:   gitPath,
		DraftsDir: t.TempDir(),
	}

	draft := Draft{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
		To:        "fail@example.com",
		Subject:   "Re: [PATCH] Example patch",
		Body:      "reply body",
		MessageID: "abc.123@example.com",
	}

	result, err := service.SendDraft(context.Background(), draft)
	if err == nil {
		t.Fatal("SendDraft unexpectedly succeeded")
	}

	var sendErr *SendError
	if !errors.As(err, &sendErr) {
		t.Fatalf("SendDraft returned wrong error type: %T", err)
	}
	if !strings.Contains(sendErr.Output, "simulated send failure") {
		t.Fatalf("send error output missing failure marker:\n%s", sendErr.Output)
	}
	if !strings.Contains(result.Output, "simulated send failure") {
		t.Fatalf("partial send result missing failure output:\n%s", result.Output)
	}
}

func writeTestB4(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "b4")
	script := `#!/bin/sh
out_dir=""
url=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o)
			out_dir="$2"
			shift 2
			;;
		*)
			url="$1"
			shift
			;;
	esac
done

if [ "$url" = "fail" ]; then
	echo "b4 exploded" >&2
	exit 1
fi

cat > "$out_dir/test.mbx" <<'EOF'
From nobody Mon Jan 01 00:00:00 2024
From: Example Author <author@example.com>
To: linux-mm@kvack.org, Andrew Morton <akpm@example.com>
Cc: Jane Doe <jane@example.com>
Subject: [PATCH] Example patch
Message-ID: <abc.123@example.com>
Date: Sun, 01 Feb 2026 09:20:35 -0500
Content-Transfer-Encoding: quoted-printable

Example=20body

-- 
Signature
EOF
`

	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write test b4: %v", err)
	}

	return path
}

func writeTestGit(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	argsPath := filepath.Join(dir, "git.args")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shellQuote(argsPath) + "\n" +
		"for arg in \"$@\"; do\n" +
		"\tif [ \"$arg\" = \"--to=fail@example.com\" ]; then\n" +
		"\t\techo \"simulated send failure\" >&2\n" +
		"\t\texit 1\n" +
		"\tfi\n" +
		"done\n" +
		"echo \"Sendmail: fake-sendmail $*\"\n" +
		"echo \"Result: OK\"\n"

	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write test git: %v", err)
	}

	return path, argsPath
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
