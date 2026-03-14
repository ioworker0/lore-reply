package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"unicode"
)

var draftTemplate = template.Must(template.New("draft").Parse(`From: {{ .FromHeader }}
Subject: {{ .Subject }}
Content-Type: text/plain; charset=UTF-8
Content-Transfer-Encoding: 8bit

{{ .Body }}`))

// Draft is the editable mail state exchanged with the web layer.
type Draft struct {
	FromName  string `json:"from_name"`
	FromEmail string `json:"from_email"`
	To        string `json:"to"`
	Cc        string `json:"cc"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	MessageID string `json:"message_id"`
	DraftPath string `json:"draft_path"`
	SourceURL string `json:"source_url,omitempty"`
}

// LoadOptions controls draft generation.
type LoadOptions struct {
	URL       string
	FromName  string
	FromEmail string
}

// SaveResult is returned after writing a draft to disk.
type SaveResult struct {
	DraftPath   string `json:"draft_path"`
	SendCommand string `json:"send_command"`
}

// B4Error carries the raw command output for UI display.
type B4Error struct {
	Output string
}

func (e *B4Error) Error() string {
	output := strings.TrimSpace(e.Output)
	if output == "" {
		return "b4 failed"
	}
	return output
}

// Service wraps the b4 integration and draft file generation.
type Service struct {
	B4Path    string
	DraftsDir string
}

type parsedMessage struct {
	To           string
	Cc           string
	Subject      string
	MessageID    string
	CitationDate string
	AuthorName   string
	QuotedBody   string
}

// LoadDraft fetches the lore URL with b4 and returns a browser-editable draft.
func (s Service) LoadDraft(ctx context.Context, opts LoadOptions) (Draft, error) {
	if strings.TrimSpace(opts.URL) == "" {
		return Draft{}, errors.New("url is required")
	}

	data, output, err := s.fetchMessage(ctx, opts.URL)
	if err != nil {
		return Draft{}, &B4Error{Output: output}
	}

	msg, err := parseMessageContent(data)
	if err != nil {
		return Draft{}, err
	}

	body := "\n" + buildCitation(msg.CitationDate, msg.AuthorName) + "\n" + msg.QuotedBody
	draftPath := buildDraftPath(s.DraftsDir, msg.MessageID, msg.Subject)

	return Draft{
		FromName:  opts.FromName,
		FromEmail: opts.FromEmail,
		To:        msg.To,
		Cc:        msg.Cc,
		Subject:   ensureReplySubject(msg.Subject),
		Body:      body,
		MessageID: msg.MessageID,
		DraftPath: draftPath,
		SourceURL: opts.URL,
	}, nil
}

// SaveDraft writes the current editable draft to disk and returns the send command.
func (s Service) SaveDraft(draft Draft) (SaveResult, error) {
	if strings.TrimSpace(draft.MessageID) == "" {
		return SaveResult{}, errors.New("message_id is required")
	}

	draftPath, err := s.resolveDraftPath(draft)
	if err != nil {
		return SaveResult{}, err
	}

	if err := os.MkdirAll(filepath.Dir(draftPath), 0o755); err != nil {
		return SaveResult{}, fmt.Errorf("create draft directory: %w", err)
	}

	content, err := renderDraftFile(draft)
	if err != nil {
		return SaveResult{}, err
	}

	if err := os.WriteFile(draftPath, []byte(content), 0o644); err != nil {
		return SaveResult{}, fmt.Errorf("write draft: %w", err)
	}

	return SaveResult{
		DraftPath:   draftPath,
		SendCommand: buildSendCommand(draft, draftPath),
	}, nil
}

func (s Service) fetchMessage(ctx context.Context, url string) ([]byte, string, error) {
	tempDir, err := os.MkdirTemp("", "lore-reply-")
	if err != nil {
		return nil, "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	cmd := exec.CommandContext(ctx, s.B4Path, "mbox", "--single-message", "-o", tempDir, url)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(output), err
	}

	entries, err := filepath.Glob(filepath.Join(tempDir, "*.mbx"))
	if err != nil {
		return nil, string(output), fmt.Errorf("find mbox output: %w", err)
	}
	if len(entries) == 0 {
		return nil, string(output), errors.New("b4 did not produce an mbox file")
	}

	sort.Strings(entries)

	data, err := os.ReadFile(entries[0])
	if err != nil {
		return nil, string(output), fmt.Errorf("read mbox file: %w", err)
	}

	return data, string(output), nil
}

func parseMessageContent(data []byte) (parsedMessage, error) {
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.HasPrefix(content, "From ") {
		if newline := strings.Index(content, "\n"); newline >= 0 {
			content = content[newline+1:]
		}
	}

	message, err := netmail.ReadMessage(strings.NewReader(content))
	if err != nil {
		return parsedMessage{}, fmt.Errorf("parse message: %w", err)
	}

	subject := decodeHeaderValue(message.Header.Get("Subject"))
	msgID := strings.Trim(strings.TrimSpace(message.Header.Get("Message-ID")), "<>")
	if msgID == "" {
		return parsedMessage{}, errors.New("message-id header is required")
	}

	fromHeader := message.Header.Get("From")
	replyTo, replyCc := buildReplyRecipients(fromHeader, message.Header.Get("To"), message.Header.Get("Cc"))
	return parsedMessage{
		To:           replyTo,
		Cc:           replyCc,
		Subject:      subject,
		MessageID:    msgID,
		CitationDate: formatCitationDate(message.Header.Get("Date")),
		AuthorName:   extractAuthorName(fromHeader),
		QuotedBody:   quoteBody(cleanBody(message)),
	}, nil
}

func cleanBody(message *netmail.Message) string {
	bodyData, err := io.ReadAll(message.Body)
	if err != nil {
		return ""
	}

	rawBody := strings.ReplaceAll(string(bodyData), "\r\n", "\n")
	lines := strings.Split(rawBody, "\n")
	for index, line := range lines {
		if line == "-- " {
			lines = lines[:index]
			break
		}
	}
	rawBody = strings.Join(lines, "\n")

	decoded := []byte(rawBody)
	if strings.Contains(strings.ToLower(message.Header.Get("Content-Transfer-Encoding")), "quoted-printable") {
		reader := quotedprintable.NewReader(bytes.NewReader(decoded))
		decoded, err = io.ReadAll(reader)
		if err != nil {
			decoded = []byte(rawBody)
		}
	}

	body := strings.ReplaceAll(string(decoded), "\r\n", "\n")
	lines = strings.Split(body, "\n")
	for index, line := range lines {
		if line == "-- " || line == "--" {
			lines = lines[:index]
			break
		}
	}

	return strings.Join(lines, "\n")
}

func quoteBody(body string) string {
	lines := strings.Split(body, "\n")
	for index := range lines {
		lines[index] = ">" + lines[index]
	}
	return strings.Join(lines, "\n")
}

func buildCitation(date string, author string) string {
	if author == "" {
		author = "unknown"
	}
	if date == "" {
		return fmt.Sprintf("On an unknown date, %s wrote:", author)
	}
	return fmt.Sprintf("On %s, %s wrote:", date, author)
}

func ensureReplySubject(subject string) string {
	if strings.HasPrefix(strings.ToLower(subject), "re:") {
		return subject
	}
	return "Re: " + subject
}

func formatCitationDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := netmail.ParseDate(raw)
	if err != nil {
		return raw
	}

	return parsed.Format("Mon, Jan 02, 2006 at 03:04:05PM -0700")
}

func extractAuthorName(raw string) string {
	if address, err := netmail.ParseAddress(raw); err == nil {
		if address.Name != "" {
			return address.Name
		}
		if address.Address != "" {
			return address.Address
		}
	}

	trimmed := strings.TrimSpace(raw)
	if cut := strings.Index(trimmed, "<"); cut >= 0 {
		trimmed = strings.TrimSpace(trimmed[:cut])
	}
	return trimmed
}

func normalizeAddressHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if addresses, err := netmail.ParseAddressList(raw); err == nil {
		parts := make([]string, 0, len(addresses))
		for _, address := range addresses {
			parts = append(parts, formatAddress(address))
		}
		return strings.Join(parts, ", ")
	}

	return strings.Join(strings.Fields(decodeHeaderValue(raw)), " ")
}

func buildReplyRecipients(fromHeader, toHeader, ccHeader string) (string, string) {
	replyTo := normalizeAddressHeader(fromHeader)
	replyToAddresses := parseAddresses(fromHeader)

	seen := make(map[string]struct{}, len(replyToAddresses))
	for _, address := range replyToAddresses {
		if key := addressKey(address); key != "" {
			seen[key] = struct{}{}
		}
	}

	ccAddresses := make([]*netmail.Address, 0)
	for _, address := range append(parseAddresses(toHeader), parseAddresses(ccHeader)...) {
		key := addressKey(address)
		if key != "" {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		ccAddresses = append(ccAddresses, address)
	}

	return replyTo, joinAddresses(ccAddresses)
}

func decodeHeaderValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	decoder := &mime.WordDecoder{}
	decoded, err := decoder.DecodeHeader(raw)
	if err != nil {
		return raw
	}
	return decoded
}

func parseAddresses(raw string) []*netmail.Address {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	if addresses, err := netmail.ParseAddressList(raw); err == nil {
		return addresses
	}

	parts := strings.Split(raw, ",")
	addresses := make([]*netmail.Address, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(decodeHeaderValue(part))
		if part == "" {
			continue
		}
		if address, err := netmail.ParseAddress(part); err == nil {
			addresses = append(addresses, address)
			continue
		}
		addresses = append(addresses, &netmail.Address{Address: part})
	}
	return addresses
}

func joinAddresses(addresses []*netmail.Address) string {
	parts := make([]string, 0, len(addresses))
	for _, address := range addresses {
		value := formatAddress(address)
		if value == "" {
			continue
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, ", ")
}

func addressKey(address *netmail.Address) string {
	if address == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(address.Address))
}

func buildDraftPath(draftsDir, messageID, subject string) string {
	messageSlug := sanitizeSlug(messageID)
	if messageSlug == "" {
		messageSlug = "message"
	}

	subjectSlug := sanitizeSlug(stripPatchPrefix(subject))
	filename := "reply-" + messageSlug
	if subjectSlug != "" {
		filename += "-" + subjectSlug
	}
	filename += ".txt"

	return filepath.Join(draftsDir, filename)
}

func stripPatchPrefix(subject string) string {
	subject = strings.TrimSpace(subject)
	lower := strings.ToLower(subject)
	if strings.HasPrefix(lower, "re:") {
		subject = strings.TrimSpace(subject[3:])
	}
	if strings.HasPrefix(subject, "[") {
		if end := strings.Index(subject, "]"); end >= 0 {
			subject = strings.TrimSpace(subject[end+1:])
		}
	}
	return subject
}

func sanitizeSlug(value string) string {
	var builder strings.Builder
	lastDash := false

	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(builder.String(), "-")
}

func (s Service) resolveDraftPath(draft Draft) (string, error) {
	if strings.TrimSpace(draft.DraftPath) == "" {
		return buildDraftPath(s.DraftsDir, draft.MessageID, draft.Subject), nil
	}

	resolvedDraftsDir, err := filepath.Abs(s.DraftsDir)
	if err != nil {
		return "", fmt.Errorf("resolve drafts dir: %w", err)
	}

	resolvedPath, err := filepath.Abs(draft.DraftPath)
	if err != nil {
		return "", fmt.Errorf("resolve draft path: %w", err)
	}

	if resolvedPath == resolvedDraftsDir || !strings.HasPrefix(resolvedPath, resolvedDraftsDir+string(os.PathSeparator)) {
		return "", errors.New("draft path must stay inside the drafts directory")
	}

	return resolvedPath, nil
}

func renderDraftFile(draft Draft) (string, error) {
	subject := strings.TrimSpace(draft.Subject)
	if subject == "" {
		return "", errors.New("subject is required")
	}

	body := strings.ReplaceAll(draft.Body, "\r\n", "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	data := struct {
		FromHeader string
		Subject    string
		Body       string
	}{
		FromHeader: formatFromHeader(draft.FromName, draft.FromEmail),
		Subject:    subject,
		Body:       body,
	}

	var builder strings.Builder
	if err := draftTemplate.Execute(&builder, data); err != nil {
		return "", fmt.Errorf("render draft: %w", err)
	}

	return builder.String(), nil
}

func formatFromHeader(name, email string) string {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)

	switch {
	case name != "" && email != "":
		return fmt.Sprintf("%s <%s>", name, email)
	case email != "":
		return "<" + email + ">"
	default:
		return name
	}
}

func buildSendCommand(draft Draft, draftPath string) string {
	lines := []string{"git send-email"}

	if from := formatFromHeader(draft.FromName, draft.FromEmail); strings.TrimSpace(from) != "" {
		lines = append(lines, `--from=`+doubleQuote(from))
	}
	if draft.MessageID != "" {
		lines = append(lines, `--in-reply-to=`+doubleQuote(draft.MessageID))
	}

	for _, to := range splitRecipientEmails(draft.To) {
		lines = append(lines, `--to=`+doubleQuote(to))
	}
	for _, cc := range splitRecipientEmails(draft.Cc) {
		lines = append(lines, `--cc=`+doubleQuote(cc))
	}

	lines = append(lines, doubleQuote(draftPath))

	if len(lines) == 1 {
		return lines[0]
	}

	var builder strings.Builder
	for index, line := range lines {
		if index == 0 {
			builder.WriteString(line)
			continue
		}
		if index == len(lines)-1 {
			builder.WriteString(" \\\n    ")
			builder.WriteString(line)
			continue
		}
		builder.WriteString(" \\\n    ")
		builder.WriteString(line)
	}

	return builder.String()
}

func splitRecipientEmails(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	if addresses, err := netmail.ParseAddressList(raw); err == nil {
		values := make([]string, 0, len(addresses))
		for _, address := range addresses {
			if strings.TrimSpace(address.Address) == "" {
				continue
			}
			values = append(values, address.Address)
		}
		return values
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		values = append(values, part)
	}
	return values
}

func doubleQuote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func formatAddress(address *netmail.Address) string {
	if address == nil {
		return ""
	}
	if strings.TrimSpace(address.Name) == "" {
		return address.Address
	}
	return address.String()
}
