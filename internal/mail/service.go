package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	"time"
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
	URL         string
	FromName    string
	FromEmail   string
	ForceReload bool
}

// NewOptions controls empty draft creation.
type NewOptions struct {
	FromName  string
	FromEmail string
}

// SaveResult is returned after writing a draft to disk.
type SaveResult struct {
	DraftPath   string `json:"draft_path"`
	SendCommand string `json:"send_command"`
}

// SendResult is returned after writing and sending a draft.
type SendResult struct {
	DraftPath   string `json:"draft_path"`
	SendCommand string `json:"send_command"`
	Output      string `json:"output"`
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

// SendError carries git send-email output for UI display.
type SendError struct {
	Output string
	Err    error
}

func (e *SendError) Error() string {
	output := strings.TrimSpace(e.Output)
	if output != "" {
		return output
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "git send-email failed"
}

func (e *SendError) Unwrap() error {
	return e.Err
}

// Service wraps the b4 integration and draft file generation.
type Service struct {
	B4Path    string
	GitPath   string
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

type savedDraftMetadata struct {
	MessageID string `json:"message_id"`
	To        string `json:"to"`
	Cc        string `json:"cc"`
}

type savedDraftContent struct {
	FromName  string
	FromEmail string
	Subject   string
	Body      string
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

	if opts.ForceReload {
		if err := removeSavedDraft(draftPath); err != nil {
			return Draft{}, err
		}
	} else {
		if draft, restored, err := s.loadSavedDraft(draftPath, opts.URL, msg); err != nil {
			return Draft{}, err
		} else if restored {
			return draft, nil
		}
	}

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

// NewDraft initializes a brand-new draft that is not tied to a lore message.
func (s Service) NewDraft(opts NewOptions) (Draft, error) {
	draftPath, err := buildNewDraftPath(s.DraftsDir)
	if err != nil {
		return Draft{}, err
	}

	return Draft{
		FromName:  opts.FromName,
		FromEmail: opts.FromEmail,
		DraftPath: draftPath,
	}, nil
}

// SaveDraft writes the current editable draft to disk and returns the send command.
func (s Service) SaveDraft(draft Draft) (SaveResult, error) {
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

	if err := writeDraftMetadata(draftPath, draft); err != nil {
		return SaveResult{}, err
	}

	return SaveResult{
		DraftPath:   draftPath,
		SendCommand: buildSendCommand(draft, draftPath),
	}, nil
}

// SendDraft saves the current draft and runs git send-email non-interactively.
func (s Service) SendDraft(ctx context.Context, draft Draft) (SendResult, error) {
	saveResult, err := s.SaveDraft(draft)
	if err != nil {
		return SendResult{}, err
	}

	output, err := s.runSendEmail(ctx, draft, saveResult.DraftPath)
	result := SendResult{
		DraftPath:   saveResult.DraftPath,
		SendCommand: saveResult.SendCommand,
		Output:      string(output),
	}
	if err != nil {
		return result, &SendError{Output: string(output), Err: err}
	}

	return result, nil
}

func (s Service) fetchMessage(ctx context.Context, url string) ([]byte, string, error) {
	return s.fetchMbox(ctx, url, true)
}

func (s Service) fetchMbox(ctx context.Context, url string, singleMessage bool) ([]byte, string, error) {
	tempDir, err := os.MkdirTemp("", "lore-reply-")
	if err != nil {
		return nil, "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	args := []string{"mbox"}
	if singleMessage {
		args = append(args, "--single-message")
	}
	args = append(args, "-o", tempDir, url)

	cmd := exec.CommandContext(ctx, s.B4Path, args...)
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

func (s Service) loadSavedDraft(draftPath, sourceURL string, message parsedMessage) (Draft, bool, error) {
	savedData, err := os.ReadFile(draftPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Draft{}, false, nil
		}
		return Draft{}, false, fmt.Errorf("read saved draft: %w", err)
	}

	saved, err := parseSavedDraftFile(savedData)
	if err != nil {
		return Draft{}, false, fmt.Errorf("parse saved draft: %w", err)
	}

	metadata, err := readDraftMetadata(draftPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Draft{}, false, err
	}

	to := message.To
	cc := message.Cc
	if metadata.MessageID == message.MessageID || metadata.MessageID == "" {
		if strings.TrimSpace(metadata.To) != "" {
			to = metadata.To
		}
		if strings.TrimSpace(metadata.Cc) != "" {
			cc = metadata.Cc
		}
	}

	return Draft{
		FromName:  saved.FromName,
		FromEmail: saved.FromEmail,
		To:        to,
		Cc:        cc,
		Subject:   saved.Subject,
		Body:      saved.Body,
		MessageID: message.MessageID,
		DraftPath: draftPath,
		SourceURL: sourceURL,
	}, true, nil
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
	decoded := []byte(rawBody)
	if strings.Contains(strings.ToLower(message.Header.Get("Content-Transfer-Encoding")), "quoted-printable") {
		reader := quotedprintable.NewReader(bytes.NewReader(decoded))
		decoded, err = io.ReadAll(reader)
		if err != nil {
			decoded = []byte(rawBody)
		}
	}

	body := strings.ReplaceAll(string(decoded), "\r\n", "\n")
	return strings.TrimSuffix(body, "\n")
}

func parseSavedDraftFile(data []byte) (savedDraftContent, error) {
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	message, err := netmail.ReadMessage(strings.NewReader(content))
	if err != nil {
		return savedDraftContent{}, fmt.Errorf("parse saved draft message: %w", err)
	}

	bodyData, err := io.ReadAll(message.Body)
	if err != nil {
		return savedDraftContent{}, fmt.Errorf("read saved draft body: %w", err)
	}

	fromName, fromEmail := parseFromHeader(message.Header.Get("From"))
	body := strings.ReplaceAll(string(bodyData), "\r\n", "\n")
	body = strings.TrimSuffix(body, "\n")

	return savedDraftContent{
		FromName:  fromName,
		FromEmail: fromEmail,
		Subject:   decodeHeaderValue(message.Header.Get("Subject")),
		Body:      body,
	}, nil
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

func buildNewDraftPath(draftsDir string) (string, error) {
	if strings.TrimSpace(draftsDir) == "" {
		return "", errors.New("drafts dir is required")
	}

	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("generate draft token: %w", err)
	}

	filename := fmt.Sprintf(
		"compose-%s-%s.txt",
		time.Now().UTC().Format("20060102-150405"),
		hex.EncodeToString(token),
	)
	return filepath.Join(draftsDir, filename), nil
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
		if strings.TrimSpace(draft.MessageID) != "" {
			return buildDraftPath(s.DraftsDir, draft.MessageID, draft.Subject), nil
		}
		return buildNewDraftPath(s.DraftsDir)
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

func parseFromHeader(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}

	address, err := netmail.ParseAddress(raw)
	if err == nil {
		return address.Name, address.Address
	}

	if strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") {
		return "", strings.Trim(raw, "<>")
	}

	return raw, ""
}

func draftMetadataPath(draftPath string) string {
	return draftPath + ".meta.json"
}

func writeDraftMetadata(draftPath string, draft Draft) error {
	metadata := savedDraftMetadata{
		MessageID: draft.MessageID,
		To:        draft.To,
		Cc:        draft.Cc,
	}

	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode draft metadata: %w", err)
	}

	if err := os.WriteFile(draftMetadataPath(draftPath), data, 0o644); err != nil {
		return fmt.Errorf("write draft metadata: %w", err)
	}

	return nil
}

func readDraftMetadata(draftPath string) (savedDraftMetadata, error) {
	data, err := os.ReadFile(draftMetadataPath(draftPath))
	if err != nil {
		return savedDraftMetadata{}, err
	}

	var metadata savedDraftMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return savedDraftMetadata{}, fmt.Errorf("parse draft metadata: %w", err)
	}

	return metadata, nil
}

func removeSavedDraft(draftPath string) error {
	if err := os.Remove(draftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove saved draft: %w", err)
	}

	if err := os.Remove(draftMetadataPath(draftPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove saved draft metadata: %w", err)
	}

	return nil
}

func (s Service) runSendEmail(ctx context.Context, draft Draft, draftPath string) ([]byte, error) {
	gitPath := s.GitPath
	if strings.TrimSpace(gitPath) == "" {
		gitPath = "git"
	}

	cmd := exec.CommandContext(ctx, gitPath, buildSendArgs(draft, draftPath, "never")...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.CombinedOutput()
}

func buildSendArgs(draft Draft, draftPath, confirm string) []string {
	args := []string{"send-email"}
	if strings.TrimSpace(confirm) != "" {
		args = append(args, "--confirm="+confirm)
	}

	if from := formatFromHeader(draft.FromName, draft.FromEmail); strings.TrimSpace(from) != "" {
		args = append(args, "--from="+from)
	}
	if draft.MessageID != "" {
		args = append(args, "--in-reply-to="+draft.MessageID)
	}

	for _, to := range splitRecipientEmails(draft.To) {
		args = append(args, "--to="+to)
	}
	for _, cc := range splitRecipientEmails(draft.Cc) {
		args = append(args, "--cc="+cc)
	}

	args = append(args, draftPath)
	return args
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
