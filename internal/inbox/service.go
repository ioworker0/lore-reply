package inbox

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	netmail "net/mail"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	loremail "github.com/ioworker0/lore-reply/internal/mail"
)

const (
	DefaultListURL     = "https://lore.kernel.org/linux-mm/"
	DefaultRangeDays   = 7
	DefaultMaxMessages = 100

	reasonFromMe  = "from me"
	reasonMention = "to/cc me"

	MatchModeAllRelated = "all_related"
	MatchModeFromMe     = "from_me"
	MatchModeToCcMe     = "to_cc_me"
)

type Discoverer interface {
	Discover(ctx context.Context, query DiscoveryQuery) ([]SearchHit, error)
}

type ThreadLoader interface {
	LoadThread(ctx context.Context, sourceURL string) ([]loremail.ThreadMessage, error)
}

type Service struct {
	StorePath  string
	Discoverer Discoverer
	Loader     ThreadLoader

	mu sync.Mutex
}

type SyncOptions struct {
	ListURL     string   `json:"list_url"`
	MyEmails    []string `json:"my_emails"`
	RangeDays   int      `json:"range_days"`
	MatchMode   string   `json:"match_mode"`
	MaxMessages int      `json:"max_messages"`
}

type SyncResult struct {
	Threads        []ThreadSummary `json:"threads"`
	SyncedAt       string          `json:"synced_at"`
	ListURL        string          `json:"list_url"`
	MyEmails       []string        `json:"my_emails"`
	RangeDays      int             `json:"range_days"`
	MatchMode      string          `json:"match_mode"`
	MaxMessages    int             `json:"max_messages"`
	MatchedMessage int             `json:"matched_message_count"`
}

type ThreadSummary struct {
	ID           string   `json:"id"`
	Subject      string   `json:"subject"`
	Author       string   `json:"author"`
	LatestFrom   string   `json:"latest_from"`
	LatestDate   string   `json:"latest_date"`
	LatestURL    string   `json:"latest_url"`
	MessageCount int      `json:"message_count"`
	MatchReasons []string `json:"match_reasons"`
	HasFromMe    bool     `json:"has_from_me"`
	HasMention   bool     `json:"has_mention"`
	NeedsReply   bool     `json:"needs_reply"`
}

type ThreadDetail struct {
	Thread   ThreadSummary    `json:"thread"`
	Messages []MessageSummary `json:"messages"`
}

type MessageSummary struct {
	MessageID    string   `json:"message_id"`
	Subject      string   `json:"subject"`
	From         string   `json:"from"`
	To           string   `json:"to,omitempty"`
	Cc           string   `json:"cc,omitempty"`
	Date         string   `json:"date"`
	Body         string   `json:"body"`
	URL          string   `json:"url"`
	InReplyTo    string   `json:"in_reply_to,omitempty"`
	References   []string `json:"references,omitempty"`
	MatchReasons []string `json:"match_reasons,omitempty"`
	IsHit        bool     `json:"is_hit"`
}

type DiscoveryQuery struct {
	ListURL string
	Query   string
	Limit   int
}

type SearchHit struct {
	URL     string   `json:"url"`
	Reasons []string `json:"reasons,omitempty"`
}

type HTTPDiscoverer struct {
	Client *http.Client
}

type persistedState struct {
	Config    SyncOptions      `json:"config"`
	SyncedAt  string           `json:"synced_at"`
	Threads   []ThreadDetail   `json:"threads"`
	Preflight []queryWatermark `json:"preflight,omitempty"`
}

type searchPlan struct {
	ListURL string
	Key     string
	Query   string
	Reason  string
}

type queryWatermark struct {
	Key       string `json:"key"`
	LatestHit string `json:"latest_hit,omitempty"`
}

func (s *Service) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	opts = normalizeOptions(opts)
	if len(opts.MyEmails) == 0 {
		return SyncResult{}, errors.New("at least one email is required")
	}
	if strings.TrimSpace(s.StorePath) == "" {
		return SyncResult{}, errors.New("store path is required")
	}
	if s.Discoverer == nil {
		return SyncResult{}, errors.New("discoverer is required")
	}
	if s.Loader == nil {
		return SyncResult{}, errors.New("thread loader is required")
	}

	queries := buildQueries(opts)
	if len(queries) == 0 {
		return SyncResult{}, errors.New("no discovery queries were generated")
	}

	cachedState, canUsePreflight, err := s.loadCachedState(opts)
	if err != nil {
		return SyncResult{}, err
	}
	if canUsePreflight {
		_, changed, err := s.preflightQueries(ctx, queries, cachedState.Preflight)
		if err != nil {
			return SyncResult{}, err
		}
		if !changed {
			return syncResultFromState(cachedState), nil
		}
	}

	hits, latestHits, err := s.discoverHits(ctx, queries, opts.MaxMessages)
	if err != nil {
		return SyncResult{}, err
	}

	threads, err := s.loadThreads(ctx, opts, hits)
	if err != nil {
		return SyncResult{}, err
	}

	result := SyncResult{
		Threads:        make([]ThreadSummary, 0, len(threads)),
		SyncedAt:       time.Now().UTC().Format(time.RFC3339),
		ListURL:        opts.ListURL,
		MyEmails:       append([]string(nil), opts.MyEmails...),
		RangeDays:      opts.RangeDays,
		MatchMode:      opts.MatchMode,
		MaxMessages:    opts.MaxMessages,
		MatchedMessage: len(hits),
	}
	for _, thread := range threads {
		result.Threads = append(result.Threads, thread.Thread)
	}

	state := persistedState{
		Config:    opts,
		SyncedAt:  result.SyncedAt,
		Threads:   threads,
		Preflight: latestHits,
	}
	if err := s.writeState(state); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

func (s *Service) ListThreads() (SyncResult, error) {
	state, err := s.readState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SyncResult{
				Threads:     []ThreadSummary{},
				ListURL:     DefaultListURL,
				RangeDays:   DefaultRangeDays,
				MatchMode:   MatchModeAllRelated,
				MaxMessages: DefaultMaxMessages,
			}, nil
		}
		return SyncResult{}, err
	}

	result := SyncResult{
		Threads:     make([]ThreadSummary, 0, len(state.Threads)),
		SyncedAt:    state.SyncedAt,
		ListURL:     state.Config.ListURL,
		MyEmails:    append([]string(nil), state.Config.MyEmails...),
		RangeDays:   state.Config.RangeDays,
		MatchMode:   state.Config.MatchMode,
		MaxMessages: state.Config.MaxMessages,
	}
	for _, thread := range state.Threads {
		result.Threads = append(result.Threads, thread.Thread)
	}
	return result, nil
}

func (s *Service) GetThread(threadID string) (ThreadDetail, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ThreadDetail{}, errors.New("thread id is required")
	}

	state, err := s.readState()
	if err != nil {
		return ThreadDetail{}, err
	}
	for _, thread := range state.Threads {
		if thread.Thread.ID == threadID {
			return thread, nil
		}
	}
	return ThreadDetail{}, os.ErrNotExist
}

func (s *Service) discoverHits(ctx context.Context, queries []searchPlan, maxMessages int) ([]SearchHit, []queryWatermark, error) {
	merged := make([]SearchHit, 0, maxMessages)
	watermarks := make([]queryWatermark, 0, len(queries))
	index := make(map[string]int)
	reachedLimit := false
	for _, query := range queries {
		results, err := s.Discoverer.Discover(ctx, DiscoveryQuery{
			ListURL: query.ListURL,
			Query:   query.Query,
			Limit:   maxMessages,
		})
		if err != nil {
			return nil, nil, err
		}
		watermarks = append(watermarks, queryWatermark{
			Key:       query.Key,
			LatestHit: latestQueryHit(results),
		})
		for _, result := range results {
			key := canonicalMessageKey(result.URL)
			if key == "" {
				continue
			}
			if idx, ok := index[key]; ok {
				merged[idx].Reasons = appendReason(merged[idx].Reasons, query.Reason)
				continue
			}
			if reachedLimit {
				continue
			}
			index[key] = len(merged)
			merged = append(merged, SearchHit{
				URL:     result.URL,
				Reasons: appendReason(nil, query.Reason),
			})
			if len(merged) >= maxMessages {
				reachedLimit = true
			}
		}
	}
	return merged, watermarks, nil
}

func (s *Service) loadThreads(ctx context.Context, opts SyncOptions, hits []SearchHit) ([]ThreadDetail, error) {
	covered := make(map[string]struct{})
	threads := make([]ThreadDetail, 0, len(hits))
	for _, hit := range hits {
		key := canonicalMessageKey(hit.URL)
		if key == "" {
			continue
		}
		if _, seen := covered[key]; seen {
			continue
		}

		threadMessages, err := s.Loader.LoadThread(ctx, hit.URL)
		if err != nil {
			return nil, err
		}

		thread := buildThreadDetail(threadMessages, hits, opts.MyEmails)
		threads = append(threads, thread)
		for _, message := range thread.Messages {
			if message.MessageID != "" {
				covered[canonicalMessageKey(message.MessageID)] = struct{}{}
			}
			if message.URL != "" {
				covered[canonicalMessageKey(message.URL)] = struct{}{}
			}
		}
	}
	sort.SliceStable(threads, func(i, j int) bool {
		return latestDateUnix(threads[i]) > latestDateUnix(threads[j])
	})
	return threads, nil
}

func (s *Service) loadCachedState(opts SyncOptions) (persistedState, bool, error) {
	state, err := s.readState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return persistedState{}, false, nil
		}
		return persistedState{}, false, err
	}

	if !sameSyncOptions(state.Config, opts) {
		return persistedState{}, false, nil
	}
	return state, true, nil
}

func (s *Service) preflightQueries(ctx context.Context, queries []searchPlan, previous []queryWatermark) ([]queryWatermark, bool, error) {
	current := make([]queryWatermark, 0, len(queries))
	previousMap := make(map[string]string, len(previous))
	for _, watermark := range previous {
		previousMap[watermark.Key] = watermark.LatestHit
	}

	changed := len(previous) != len(queries)
	for _, query := range queries {
		results, err := s.Discoverer.Discover(ctx, DiscoveryQuery{
			ListURL: query.ListURL,
			Query:   query.Query,
			Limit:   1,
		})
		if err != nil {
			return nil, false, err
		}

		currentWatermark := queryWatermark{
			Key:       query.Key,
			LatestHit: latestQueryHit(results),
		}
		current = append(current, currentWatermark)
		if previousMap[query.Key] != currentWatermark.LatestHit {
			changed = true
		}
		delete(previousMap, query.Key)
	}

	if len(previousMap) > 0 {
		changed = true
	}
	return current, changed, nil
}

func syncResultFromState(state persistedState) SyncResult {
	result := SyncResult{
		Threads:     make([]ThreadSummary, 0, len(state.Threads)),
		SyncedAt:    state.SyncedAt,
		ListURL:     state.Config.ListURL,
		MyEmails:    append([]string(nil), state.Config.MyEmails...),
		RangeDays:   state.Config.RangeDays,
		MatchMode:   state.Config.MatchMode,
		MaxMessages: state.Config.MaxMessages,
	}
	for _, thread := range state.Threads {
		result.Threads = append(result.Threads, thread.Thread)
	}
	return result
}

func (s *Service) writeState(state persistedState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.StorePath), 0o755); err != nil {
		return fmt.Errorf("create inbox cache directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode inbox state: %w", err)
	}
	if err := os.WriteFile(s.StorePath, data, 0o644); err != nil {
		return fmt.Errorf("write inbox state: %w", err)
	}
	return nil
}

func (s *Service) readState() (persistedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.StorePath)
	if err != nil {
		return persistedState{}, err
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return persistedState{}, fmt.Errorf("parse inbox state: %w", err)
	}
	normalizePersistedState(&state)
	return state, nil
}

func (d HTTPDiscoverer) Discover(ctx context.Context, query DiscoveryQuery) ([]SearchHit, error) {
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}

	feedURL, err := buildFeedURL(query.ListURL, query.Query, query.Limit)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build inbox query request: %w", err)
	}
	request.Header.Set("User-Agent", "lore-reply/inbox")

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query lore inbox feed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query lore inbox feed: unexpected status %s", response.Status)
	}

	type atomFeed struct {
		Entries []struct {
			Link struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}

	var feed atomFeed
	if err := decodeFeed(response, &feed); err != nil {
		return nil, err
	}

	baseURL, _ := url.Parse(feedURL)
	hits := make([]SearchHit, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		href := strings.TrimSpace(entry.Link.Href)
		if href == "" {
			continue
		}
		if baseURL != nil {
			if parsedHref, err := url.Parse(href); err == nil {
				href = baseURL.ResolveReference(parsedHref).String()
			}
		}
		hits = append(hits, SearchHit{URL: href})
		if query.Limit > 0 && len(hits) >= query.Limit {
			break
		}
	}
	return hits, nil
}

func buildFeedURL(listURL, query string, limit int) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(listURL))
	if err != nil {
		return "", fmt.Errorf("parse list url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("list url must include scheme and host")
	}

	values := parsed.Query()
	values.Set("q", query)
	values.Set("x", "A")
	if limit > 0 {
		values.Set("l", strconv.Itoa(limit))
	}
	parsed.RawQuery = values.Encode()
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

func buildQueries(opts SyncOptions) []searchPlan {
	dateStart := time.Now().UTC().AddDate(0, 0, -opts.RangeDays).Format("2006-01-02")
	queries := make([]searchPlan, 0, len(opts.MyEmails)*3)
	appendQuery := func(email, field, reason string) {
		key := field + ":" + email
		queries = append(queries, searchPlan{
			ListURL: opts.ListURL,
			Key:     key,
			Query:   fmt.Sprintf("%s d:%s..", key, dateStart),
			Reason:  reason,
		})
	}

	for _, email := range opts.MyEmails {
		switch opts.MatchMode {
		case MatchModeFromMe:
			appendQuery(email, "f", reasonFromMe)
		case MatchModeToCcMe:
			appendQuery(email, "t", reasonMention)
			appendQuery(email, "c", reasonMention)
		default:
			appendQuery(email, "f", reasonFromMe)
			appendQuery(email, "t", reasonMention)
			appendQuery(email, "c", reasonMention)
		}
	}
	return queries
}

func normalizeOptions(opts SyncOptions) SyncOptions {
	opts.ListURL = strings.TrimSpace(opts.ListURL)
	if opts.ListURL == "" {
		opts.ListURL = DefaultListURL
	}
	opts.MyEmails = normalizeEmails(opts.MyEmails)
	if opts.RangeDays <= 0 {
		opts.RangeDays = DefaultRangeDays
	}
	if opts.MaxMessages <= 0 {
		opts.MaxMessages = DefaultMaxMessages
	}
	switch opts.MatchMode {
	case MatchModeFromMe, MatchModeToCcMe:
	default:
		opts.MatchMode = MatchModeAllRelated
	}
	return opts
}

func normalizeEmails(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	emails := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		emails = append(emails, value)
	}
	return emails
}

func sameSyncOptions(left, right SyncOptions) bool {
	left = normalizeOptions(left)
	right = normalizeOptions(right)
	if left.ListURL != right.ListURL ||
		left.RangeDays != right.RangeDays ||
		left.MatchMode != right.MatchMode ||
		left.MaxMessages != right.MaxMessages {
		return false
	}

	if len(left.MyEmails) != len(right.MyEmails) {
		return false
	}

	leftEmails := append([]string(nil), left.MyEmails...)
	rightEmails := append([]string(nil), right.MyEmails...)
	sort.Strings(leftEmails)
	sort.Strings(rightEmails)
	return slices.Equal(leftEmails, rightEmails)
}

func buildThreadDetail(messages []loremail.ThreadMessage, hits []SearchHit, myEmails []string) ThreadDetail {
	hitMap := make(map[string][]string, len(hits))
	for _, hit := range hits {
		key := canonicalMessageKey(hit.URL)
		if key == "" {
			continue
		}
		hitMap[key] = appendReasons(hitMap[key], hit.Reasons...)
	}

	sorted := append([]loremail.ThreadMessage(nil), messages...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].DateUnix == sorted[j].DateUnix {
			return sorted[i].MessageID < sorted[j].MessageID
		}
		return sorted[i].DateUnix < sorted[j].DateUnix
	})

	root := sorted[0]
	known := make(map[string]struct{}, len(sorted))
	for _, message := range sorted {
		known[message.MessageID] = struct{}{}
	}
	for _, message := range sorted {
		if message.InReplyTo == "" {
			root = message
			break
		}
		if _, ok := known[message.InReplyTo]; !ok {
			root = message
			break
		}
	}

	detail := ThreadDetail{
		Thread: ThreadSummary{
			ID:      root.MessageID,
			Subject: firstNonEmpty(root.Subject, sorted[0].Subject),
			Author:  firstNonEmpty(root.From, sorted[0].From),
		},
		Messages: make([]MessageSummary, 0, len(sorted)),
	}

	reasons := make([]string, 0, 2)
	for _, message := range sorted {
		messageReasons := hitMap[canonicalMessageKey(message.MessageID)]
		if len(messageReasons) == 0 {
			messageReasons = hitMap[canonicalMessageKey(message.URL)]
		}
		detail.Messages = append(detail.Messages, MessageSummary{
			MessageID:    message.MessageID,
			Subject:      message.Subject,
			From:         message.From,
			To:           message.To,
			Cc:           message.Cc,
			Date:         message.Date,
			Body:         message.Body,
			URL:          message.URL,
			InReplyTo:    message.InReplyTo,
			References:   append([]string(nil), message.References...),
			MatchReasons: append([]string(nil), messageReasons...),
			IsHit:        len(messageReasons) > 0,
		})
		reasons = appendReasons(reasons, messageReasons...)
		if isFromMe(message.From, myEmails) {
			detail.Thread.HasFromMe = true
		}
		if mentionsMe(message.To, myEmails) || mentionsMe(message.Cc, myEmails) {
			detail.Thread.HasMention = true
		}
	}

	latest := detail.Messages[len(detail.Messages)-1]
	detail.Thread.LatestFrom = latest.From
	detail.Thread.LatestDate = latest.Date
	detail.Thread.LatestURL = latest.URL
	detail.Thread.MessageCount = len(detail.Messages)
	detail.Thread.MatchReasons = reasons
	detail.Thread.NeedsReply = (detail.Thread.HasFromMe || detail.Thread.HasMention) && !isFromMe(latest.From, myEmails)
	if detail.Thread.Subject == "" {
		detail.Thread.Subject = latest.Subject
	}
	return detail
}

func latestDateUnix(detail ThreadDetail) int64 {
	if len(detail.Messages) == 0 {
		return 0
	}
	last := detail.Messages[len(detail.Messages)-1]
	if last.Date == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339, last.Date)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

func normalizePersistedState(state *persistedState) {
	if state == nil {
		return
	}
	for index := range state.Threads {
		backfillThreadSummary(&state.Threads[index])
	}
}

func backfillThreadSummary(detail *ThreadDetail) {
	if detail == nil || len(detail.Messages) == 0 {
		return
	}

	root := inferThreadRoot(detail.Messages)
	if strings.TrimSpace(detail.Thread.ID) == "" {
		detail.Thread.ID = root.MessageID
	}
	if strings.TrimSpace(detail.Thread.Subject) == "" {
		detail.Thread.Subject = firstNonEmpty(root.Subject, detail.Messages[0].Subject)
	}
	if strings.TrimSpace(detail.Thread.Author) == "" {
		detail.Thread.Author = firstNonEmpty(root.From, detail.Messages[0].From)
	}

	latest := detail.Messages[len(detail.Messages)-1]
	if strings.TrimSpace(detail.Thread.LatestFrom) == "" {
		detail.Thread.LatestFrom = latest.From
	}
	if strings.TrimSpace(detail.Thread.LatestDate) == "" {
		detail.Thread.LatestDate = latest.Date
	}
	if strings.TrimSpace(detail.Thread.LatestURL) == "" {
		detail.Thread.LatestURL = latest.URL
	}
	if detail.Thread.MessageCount == 0 {
		detail.Thread.MessageCount = len(detail.Messages)
	}
}

func inferThreadRoot(messages []MessageSummary) MessageSummary {
	root := messages[0]
	known := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		known[message.MessageID] = struct{}{}
	}
	for _, message := range messages {
		if message.InReplyTo == "" {
			return message
		}
		if _, ok := known[message.InReplyTo]; !ok {
			return message
		}
	}
	return root
}

func canonicalMessageKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if strings.Contains(value, "@") && !strings.Contains(value, "://") {
		return strings.ToLower(strings.Trim(value, "<>"))
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return strings.ToLower(strings.Trim(value, "<>"))
	}
	segment := pathTail(parsed.Path)
	if segment == "" {
		return strings.ToLower(strings.Trim(value, "<>"))
	}
	decoded, err := url.PathUnescape(segment)
	if err == nil && decoded != "" {
		segment = decoded
	}
	return strings.ToLower(strings.Trim(segment, "<>"))
}

func latestQueryHit(hits []SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	return canonicalMessageKey(hits[0].URL)
}

func pathTail(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

func appendReason(values []string, reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" || slices.Contains(values, reason) {
		return values
	}
	return append(values, reason)
}

func appendReasons(values []string, reasons ...string) []string {
	for _, reason := range reasons {
		values = appendReason(values, reason)
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mentionsMe(header string, myEmails []string) bool {
	header = strings.TrimSpace(header)
	if header == "" || len(myEmails) == 0 {
		return false
	}
	addresses, err := netmail.ParseAddressList(header)
	if err == nil {
		for _, address := range addresses {
			if slices.Contains(myEmails, strings.ToLower(address.Address)) {
				return true
			}
		}
	}
	lower := strings.ToLower(header)
	for _, email := range myEmails {
		if strings.Contains(lower, email) {
			return true
		}
	}
	return false
}

func isFromMe(header string, myEmails []string) bool {
	header = strings.TrimSpace(header)
	if header == "" || len(myEmails) == 0 {
		return false
	}
	address, err := netmail.ParseAddress(header)
	if err == nil {
		return slices.Contains(myEmails, strings.ToLower(address.Address))
	}
	lower := strings.ToLower(header)
	for _, email := range myEmails {
		if strings.Contains(lower, email) {
			return true
		}
	}
	return false
}

func decodeFeed(response *http.Response, dst any) error {
	contentType := response.Header.Get("Content-Type")
	if strings.Contains(contentType, "json") {
		if err := json.NewDecoder(response.Body).Decode(dst); err != nil {
			return fmt.Errorf("parse inbox feed: %w", err)
		}
		return nil
	}

	decoder := xml.NewDecoder(response.Body)
	decoder.CharsetReader = xmlCharsetReader
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("parse inbox feed: %w", err)
	}
	return nil
}

func xmlCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	default:
		return nil, fmt.Errorf("unsupported XML charset %q", charset)
	}
}
