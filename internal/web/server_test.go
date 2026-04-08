package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ioworker0/lore-reply/internal/config"
	"github.com/ioworker0/lore-reply/internal/inbox"
	"github.com/ioworker0/lore-reply/internal/mail"
)

func TestIndexRendersDefaults(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "nobody@kernel.org") {
		t.Fatalf("response does not include default sender email:\n%s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `id="openLoreButton"`) {
		t.Fatalf("response does not include open lore button:\n%s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `id="newButton"`) {
		t.Fatalf("response does not include new mail button:\n%s", recorder.Body.String())
	}
}

func TestIndexRendersAutoLoadURL(t *testing.T) {
	t.Parallel()

	handler := newTestHandlerWithConfig(t, config.Config{
		FromName:    "nobody",
		FromEmail:   "nobody@kernel.org",
		Listen:      "127.0.0.1:9110",
		AutoLoadURL: "https://lore.kernel.org/linux-mm/test",
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `value="https://lore.kernel.org/linux-mm/test"`) {
		t.Fatalf("response does not include auto-load URL:\n%s", recorder.Body.String())
	}
}

func TestInboxSyncAndDetailFlow(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	syncBody := mustJSON(t, inbox.SyncOptions{
		ListURL:     "https://lore.kernel.org/linux-mm/",
		MyEmails:    []string{"alice@example.com"},
		RangeDays:   90,
		MatchMode:   inbox.MatchModeAllRelated,
		MaxMessages: 20,
	})

	syncRequest := httptest.NewRequest(http.MethodPost, "/api/inbox/sync", bytes.NewReader(syncBody))
	syncRecorder := httptest.NewRecorder()
	handler.ServeHTTP(syncRecorder, syncRequest)

	if syncRecorder.Code != http.StatusOK {
		t.Fatalf("sync request failed: %d %s", syncRecorder.Code, syncRecorder.Body.String())
	}

	var result inbox.SyncResult
	if err := json.Unmarshal(syncRecorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode sync result: %v", err)
	}
	if len(result.Threads) != 1 {
		t.Fatalf("unexpected thread count: %#v", result)
	}
	if result.Threads[0].ID != "abc.123@example.com" {
		t.Fatalf("unexpected thread id: %#v", result.Threads[0])
	}
	if !strings.Contains(strings.Join(result.Threads[0].MatchReasons, ","), "to/cc me") {
		t.Fatalf("expected merged match reasons in thread summary: %#v", result.Threads[0])
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/inbox/threads", nil)
	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, listRequest)

	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list request failed: %d %s", listRecorder.Code, listRecorder.Body.String())
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/inbox/thread?id=abc.123@example.com", nil)
	detailRecorder := httptest.NewRecorder()
	handler.ServeHTTP(detailRecorder, detailRequest)

	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("detail request failed: %d %s", detailRecorder.Code, detailRecorder.Body.String())
	}

	var detail inbox.ThreadDetail
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode thread detail: %v", err)
	}
	if len(detail.Messages) != 1 {
		t.Fatalf("unexpected message count: %#v", detail)
	}
	if detail.Messages[0].URL != "https://lore.kernel.org/r/abc.123@example.com" {
		t.Fatalf("unexpected message url: %#v", detail.Messages[0])
	}
}

func TestLoadAndSaveFlow(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	loadBody := mustJSON(t, loadRequest{
		URL:       "https://lore.kernel.org/linux-mm/test",
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
	})

	request := httptest.NewRequest(http.MethodPost, "/api/load", bytes.NewReader(loadBody))
	loadRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loadRecorder, request)

	if loadRecorder.Code != http.StatusOK {
		t.Fatalf("load request failed: %d %s", loadRecorder.Code, loadRecorder.Body.String())
	}

	var draft mail.Draft
	if err := json.Unmarshal(loadRecorder.Body.Bytes(), &draft); err != nil {
		t.Fatalf("decode load response: %v", err)
	}

	if draft.Subject != "Re: [PATCH] Example patch" {
		t.Fatalf("unexpected subject: %q", draft.Subject)
	}
	if draft.To != "\"Example Author\" <author@example.com>" {
		t.Fatalf("unexpected To field: %q", draft.To)
	}
	if draft.Cc != "linux-mm@kvack.org, \"Andrew Morton\" <akpm@example.com>, \"Jane Doe\" <jane@example.com>" {
		t.Fatalf("unexpected Cc field: %q", draft.Cc)
	}
	if !strings.Contains(draft.Body, "On Sun, Feb 01, 2026 at 09:20:35AM -0500, Example Author wrote:") {
		t.Fatalf("draft body misses citation:\n%s", draft.Body)
	}
	if !filepath.IsAbs(draft.DraftPath) {
		t.Fatalf("draft path is not absolute: %q", draft.DraftPath)
	}
	if draft.SourceURL != "https://lore.kernel.org/linux-mm/test" {
		t.Fatalf("unexpected source url: %q", draft.SourceURL)
	}

	draft.Body = draft.Body + "\nLooks good to me."

	saveBody := mustJSON(t, draft)
	saveRequest := httptest.NewRequest(http.MethodPost, "/api/save", bytes.NewReader(saveBody))
	saveRecorder := httptest.NewRecorder()
	handler.ServeHTTP(saveRecorder, saveRequest)

	if saveRecorder.Code != http.StatusOK {
		t.Fatalf("save request failed: %d %s", saveRecorder.Code, saveRecorder.Body.String())
	}

	var result mail.SaveResult
	if err := json.Unmarshal(saveRecorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode save response: %v", err)
	}

	savedData, err := os.ReadFile(result.DraftPath)
	if err != nil {
		t.Fatalf("read saved draft: %v", err)
	}

	saved := string(savedData)
	if !strings.Contains(saved, "From: Alice Example <alice@example.com>") {
		t.Fatalf("saved draft misses From header:\n%s", saved)
	}
	if !strings.Contains(saved, "Looks good to me.") {
		t.Fatalf("saved draft misses edited body:\n%s", saved)
	}
	if !strings.Contains(result.SendCommand, "git send-email \\") {
		t.Fatalf("unexpected send command: %s", result.SendCommand)
	}
	if !strings.Contains(result.SendCommand, "    --in-reply-to=\"abc.123@example.com\" \\") {
		t.Fatalf("send command misses message id: %s", result.SendCommand)
	}

	draft.Body = "updated body"
	secondSaveBody := mustJSON(t, draft)
	secondSaveRequest := httptest.NewRequest(http.MethodPost, "/api/save", bytes.NewReader(secondSaveBody))
	secondSaveRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondSaveRecorder, secondSaveRequest)

	if secondSaveRecorder.Code != http.StatusOK {
		t.Fatalf("second save failed: %d %s", secondSaveRecorder.Code, secondSaveRecorder.Body.String())
	}

	updatedData, err := os.ReadFile(result.DraftPath)
	if err != nil {
		t.Fatalf("read updated draft: %v", err)
	}
	if !strings.Contains(string(updatedData), "updated body") {
		t.Fatalf("draft was not overwritten:\n%s", string(updatedData))
	}
}

func TestNewDraftFlow(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	newBody := mustJSON(t, newRequest{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
	})

	newRequest := httptest.NewRequest(http.MethodPost, "/api/new", bytes.NewReader(newBody))
	newRecorder := httptest.NewRecorder()
	handler.ServeHTTP(newRecorder, newRequest)

	if newRecorder.Code != http.StatusOK {
		t.Fatalf("new draft request failed: %d %s", newRecorder.Code, newRecorder.Body.String())
	}

	var draft mail.Draft
	if err := json.Unmarshal(newRecorder.Body.Bytes(), &draft); err != nil {
		t.Fatalf("decode new draft response: %v", err)
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

	draft.To = `"Reviewer" <reviewer@example.com>`
	draft.Cc = "list@example.com"
	draft.Subject = "[PATCH 0/1] Example cover letter"
	draft.Body = "A brand-new mail body"

	saveRequest := httptest.NewRequest(http.MethodPost, "/api/save", bytes.NewReader(mustJSON(t, draft)))
	saveRecorder := httptest.NewRecorder()
	handler.ServeHTTP(saveRecorder, saveRequest)

	if saveRecorder.Code != http.StatusOK {
		t.Fatalf("save new draft request failed: %d %s", saveRecorder.Code, saveRecorder.Body.String())
	}

	var saveResult mail.SaveResult
	if err := json.Unmarshal(saveRecorder.Body.Bytes(), &saveResult); err != nil {
		t.Fatalf("decode new save response: %v", err)
	}
	if strings.Contains(saveResult.SendCommand, "--in-reply-to") {
		t.Fatalf("new mail send command unexpectedly replies to a message:\n%s", saveResult.SendCommand)
	}

	savedData, err := os.ReadFile(saveResult.DraftPath)
	if err != nil {
		t.Fatalf("read saved new draft: %v", err)
	}
	if !strings.Contains(string(savedData), "Subject: [PATCH 0/1] Example cover letter") {
		t.Fatalf("saved new draft misses subject:\n%s", string(savedData))
	}

	sendRequest := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(mustJSON(t, draft)))
	sendRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sendRecorder, sendRequest)

	if sendRecorder.Code != http.StatusOK {
		t.Fatalf("send new draft request failed: %d %s", sendRecorder.Code, sendRecorder.Body.String())
	}

	var sendResult mail.SendResult
	if err := json.Unmarshal(sendRecorder.Body.Bytes(), &sendResult); err != nil {
		t.Fatalf("decode new send response: %v", err)
	}
	if strings.Contains(sendResult.SendCommand, "--in-reply-to") {
		t.Fatalf("new mail send unexpectedly includes reply threading:\n%s", sendResult.SendCommand)
	}
	if !strings.Contains(sendResult.Output, "Result: OK") {
		t.Fatalf("send output missing success marker:\n%s", sendResult.Output)
	}
}

func TestLoadFailureReturnsRawB4Output(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	body := mustJSON(t, loadRequest{
		URL:       "fail",
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
	})

	request := httptest.NewRequest(http.MethodPost, "/api/load", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "b4 exploded") {
		t.Fatalf("raw b4 output missing from error response: %s", recorder.Body.String())
	}
}

func TestSendFlowReturnsOutput(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	draft := mail.Draft{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
		To:        `"Example Author" <author@example.com>`,
		Subject:   "Re: [PATCH] Example patch",
		Body:      "reply body",
		MessageID: "abc.123@example.com",
	}

	request := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(mustJSON(t, draft)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("send request failed: %d %s", recorder.Code, recorder.Body.String())
	}

	var result mail.SendResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode send response: %v", err)
	}

	if !strings.Contains(result.Output, "Result: OK") {
		t.Fatalf("send output missing success marker:\n%s", result.Output)
	}
	if !strings.Contains(result.SendCommand, "git send-email \\") {
		t.Fatalf("unexpected send command: %s", result.SendCommand)
	}
}

func TestSendFailureReturnsCommandOutput(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	draft := mail.Draft{
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
		To:        "fail@example.com",
		Subject:   "Re: [PATCH] Example patch",
		Body:      "reply body",
		MessageID: "abc.123@example.com",
	}

	request := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(mustJSON(t, draft)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "simulated send failure") {
		t.Fatalf("send failure output missing from error response: %s", recorder.Body.String())
	}
}

func TestLoadForceReloadIgnoresSavedDraft(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	loadBody := mustJSON(t, loadRequest{
		URL:       "https://lore.kernel.org/linux-mm/test",
		FromName:  "Alice Example",
		FromEmail: "alice@example.com",
	})
	request := httptest.NewRequest(http.MethodPost, "/api/load", bytes.NewReader(loadBody))
	loadRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loadRecorder, request)

	if loadRecorder.Code != http.StatusOK {
		t.Fatalf("initial load failed: %d %s", loadRecorder.Code, loadRecorder.Body.String())
	}

	var draft mail.Draft
	if err := json.Unmarshal(loadRecorder.Body.Bytes(), &draft); err != nil {
		t.Fatalf("decode initial load response: %v", err)
	}

	draft.Body = "saved body"
	saveBody := mustJSON(t, draft)
	saveRequest := httptest.NewRequest(http.MethodPost, "/api/save", bytes.NewReader(saveBody))
	saveRecorder := httptest.NewRecorder()
	handler.ServeHTTP(saveRecorder, saveRequest)

	if saveRecorder.Code != http.StatusOK {
		t.Fatalf("save request failed: %d %s", saveRecorder.Code, saveRecorder.Body.String())
	}

	forceBody := mustJSON(t, loadRequest{
		URL:         "https://lore.kernel.org/linux-mm/test",
		FromName:    "Fresh Name",
		FromEmail:   "fresh@example.com",
		ForceReload: true,
	})
	forceRequest := httptest.NewRequest(http.MethodPost, "/api/load", bytes.NewReader(forceBody))
	forceRecorder := httptest.NewRecorder()
	handler.ServeHTTP(forceRecorder, forceRequest)

	if forceRecorder.Code != http.StatusOK {
		t.Fatalf("force reload failed: %d %s", forceRecorder.Code, forceRecorder.Body.String())
	}

	var reloaded mail.Draft
	if err := json.Unmarshal(forceRecorder.Body.Bytes(), &reloaded); err != nil {
		t.Fatalf("decode force reload response: %v", err)
	}

	if reloaded.Body == "saved body" {
		t.Fatalf("force reload unexpectedly restored saved body")
	}
	if reloaded.FromName != "Fresh Name" || reloaded.FromEmail != "fresh@example.com" {
		t.Fatalf("force reload did not use fresh sender identity: %#v", reloaded)
	}

	revisitBody := mustJSON(t, loadRequest{
		URL:       "https://lore.kernel.org/linux-mm/test",
		FromName:  "Later Name",
		FromEmail: "later@example.com",
	})
	revisitRequest := httptest.NewRequest(http.MethodPost, "/api/load", bytes.NewReader(revisitBody))
	revisitRecorder := httptest.NewRecorder()
	handler.ServeHTTP(revisitRecorder, revisitRequest)

	if revisitRecorder.Code != http.StatusOK {
		t.Fatalf("revisit load failed: %d %s", revisitRecorder.Code, revisitRecorder.Body.String())
	}

	var revisited mail.Draft
	if err := json.Unmarshal(revisitRecorder.Body.Bytes(), &revisited); err != nil {
		t.Fatalf("decode revisit load response: %v", err)
	}

	if revisited.Body == "saved body" {
		t.Fatalf("revisit load unexpectedly restored the old saved body")
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()

	handler := newTestHandlerWithConfig(t, config.Config{
		FromName:  "nobody",
		FromEmail: "nobody@kernel.org",
		Listen:    "127.0.0.1:9110",
	})
	return handler
}

func newTestHandlerWithConfig(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()

	draftsDir := t.TempDir()
	b4Path := writeTestB4(t)
	gitPath, _ := writeTestGit(t)
	mailService := mail.Service{
		B4Path:    b4Path,
		GitPath:   gitPath,
		DraftsDir: draftsDir,
	}
	inboxService := &inbox.Service{
		StorePath:  defaultInboxStorePath(draftsDir),
		Discoverer: testDiscoverer{},
		Loader:     mailService,
	}

	cfg.B4Path = b4Path
	cfg.GitPath = gitPath
	cfg.DraftsDir = draftsDir

	handler, err := newHandler(cfg, mailService, inboxService)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	return handler
}

type testDiscoverer struct{}

func (testDiscoverer) Discover(_ context.Context, query inbox.DiscoveryQuery) ([]inbox.SearchHit, error) {
	if !strings.Contains(query.Query, "alice@example.com") {
		return nil, nil
	}
	return []inbox.SearchHit{
		{URL: "https://lore.kernel.org/r/abc.123@example.com"},
	}, nil
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

func mustJSON(t *testing.T, payload any) []byte {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return data
}
