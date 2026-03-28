package mail

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var messageIDPattern = regexp.MustCompile(`<([^>]+)>`)

// ThreadMessage is a parsed message from a lore thread mbox.
type ThreadMessage struct {
	MessageID  string   `json:"message_id"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References []string `json:"references,omitempty"`
	Subject    string   `json:"subject"`
	From       string   `json:"from"`
	To         string   `json:"to,omitempty"`
	Cc         string   `json:"cc,omitempty"`
	Date       string   `json:"date"`
	DateUnix   int64    `json:"date_unix"`
	Body       string   `json:"body"`
	URL        string   `json:"url"`
}

// LoadThread fetches a lore thread and parses every message in the returned mbox.
func (s Service) LoadThread(ctx context.Context, sourceURL string) ([]ThreadMessage, error) {
	if strings.TrimSpace(sourceURL) == "" {
		return nil, errors.New("url is required")
	}

	data, output, err := s.fetchMbox(ctx, sourceURL, false)
	if err != nil {
		return nil, &B4Error{Output: output}
	}

	return ParseThreadMbox(data, sourceURL)
}

// ParseThreadMbox parses a b4-produced thread mbox into individual messages.
func ParseThreadMbox(data []byte, sourceURL string) ([]ThreadMessage, error) {
	parts := splitMboxMessages(string(data))
	messages := make([]ThreadMessage, 0, len(parts))
	for _, part := range parts {
		msg, err := parseThreadMessage(part, sourceURL)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if len(messages) == 0 {
		return nil, errors.New("mbox did not contain any messages")
	}
	return messages, nil
}

func splitMboxMessages(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	parts := make([]string, 0, 8)
	current := make([]string, 0, len(lines))

	for index, line := range lines {
		if strings.HasPrefix(line, "From ") {
			if index == 0 {
				continue
			}
			if block := strings.TrimSpace(strings.Join(current, "\n")); block != "" {
				parts = append(parts, block)
			}
			current = current[:0]
			continue
		}
		current = append(current, line)
	}

	if block := strings.TrimSpace(strings.Join(current, "\n")); block != "" {
		parts = append(parts, block)
	}
	return parts
}

func parseThreadMessage(content string, sourceURL string) (ThreadMessage, error) {
	msg, err := netmail.ReadMessage(strings.NewReader(content))
	if err != nil {
		return ThreadMessage{}, fmt.Errorf("parse thread message: %w", err)
	}

	messageID := strings.Trim(strings.TrimSpace(msg.Header.Get("Message-ID")), "<>")
	if messageID == "" {
		return ThreadMessage{}, errors.New("thread message-id header is required")
	}

	body := cleanBody(msg)
	dateText, dateUnix := parseThreadDate(msg.Header.Get("Date"))

	return ThreadMessage{
		MessageID:  messageID,
		InReplyTo:  firstMessageID(msg.Header.Get("In-Reply-To")),
		References: extractMessageIDs(msg.Header.Get("References")),
		Subject:    decodeHeaderValue(msg.Header.Get("Subject")),
		From:       normalizeAddressHeader(msg.Header.Get("From")),
		To:         normalizeAddressHeader(msg.Header.Get("To")),
		Cc:         normalizeAddressHeader(msg.Header.Get("Cc")),
		Date:       dateText,
		DateUnix:   dateUnix,
		Body:       body,
		URL:        buildThreadMessageURL(sourceURL, messageID),
	}, nil
}

func parseThreadDate(raw string) (string, int64) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0
	}

	parsed, err := netmail.ParseDate(raw)
	if err != nil {
		return raw, 0
	}
	return parsed.Format(time.RFC3339), parsed.Unix()
}

func firstMessageID(raw string) string {
	ids := extractMessageIDs(raw)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func extractMessageIDs(raw string) []string {
	matches := messageIDPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		raw = strings.Trim(strings.TrimSpace(raw), "<>")
		if raw == "" {
			return nil
		}
		return []string{raw}
	}

	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		id := strings.TrimSpace(match[1])
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func buildThreadMessageURL(sourceURL, messageID string) string {
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	parsed.Path = "/r/" + url.PathEscape(messageID)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
