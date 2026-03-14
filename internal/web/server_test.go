package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ioworker0/lore-reply/internal/config"
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

	cfg.B4Path = b4Path
	cfg.DraftsDir = draftsDir

	handler, err := New(cfg)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	return handler
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

func mustJSON(t *testing.T, payload any) []byte {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return data
}
