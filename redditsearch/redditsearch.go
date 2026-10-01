// Package redditsearch serves nanoflux's reddit search watcher as an
// out-of-process plugin. Reddit's search pages have no RSS/Atom feed and are
// login-gated, so this plugin reads old.reddit.com's search HTML with a
// logged-in session and turns the results into a feed.
//
// It is a separate plugin from nanoflux's built-in reddit support on purpose:
// the built-in support reads reddit's public .rss feeds and stays within what
// reddit's terms allow, while watching an arbitrary search query is a
// different, login-gated scrape that a user opts into explicitly.
//
// The plugin also owns the search feed's URL shape (canonicalization), so a
// pasted URL is normalized to old.reddit.com with newest-first/all-time sorting
// and a /r/{sub}/search path folded into a subreddit: query term.
package redditsearch

import (
	"context"
	_ "embed"
	"errors"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier (stored in feeds.plugin_name).
const Name = "reddit-search"

// readme is the plugin's Markdown documentation, shown in the app's docs modal.
//
//go:embed readme.md
var readme string

const (
	// webBase is the origin used to build item permalinks. Reddit serves the
	// same permalinks on the bare/www hosts; www is the one that answers without
	// a redirect.
	webBase = "https://www.reddit.com"
	// searchBase is the origin searched. old.reddit.com's HTML is stable and
	// easy to scrape, unlike the new site's.
	searchBase = "https://old.reddit.com"
)

// userAgent is a desktop Firefox identity. Reddit rejects a default or empty
// User-Agent, so the host sends this for every mediated request.
const userAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"

// loginURL is reddit's legacy form-login endpoint, which returns a session
// cookie for accounts without 2FA. A var so tests can point it at a mock host.
var loginURL = "https://www.reddit.com/api/login"

// hosts lists hosts treated as reddit. A var so tests can inject a mock host.
var hosts = []string{"reddit.com", "www.reddit.com", "old.reddit.com", "np.reddit.com", "m.reddit.com"}

// Plugin is the reddit search Fetcher. It holds the configured account and a
// cached logged-in session; nanoflux keeps one instance alive for the life of
// the subprocess.
type Plugin struct {
	mu      sync.Mutex
	cfg     Config
	session string // cached cookie header from a successful login
}

var (
	_ pluginapi.Fetcher      = (*Plugin)(nil)
	_ pluginapi.URLPolicy    = (*Plugin)(nil)
	_ pluginapi.Docser       = (*Plugin)(nil)
	_ pluginapi.Configurable = (*Plugin)(nil)
)

// Meta declares the plugin and its browser User-Agent. RawNetwork is set because
// a username/password login needs its own cookie-handling HTTP client; the
// search fetches themselves still go through the host so its rate-limit pacing
// applies.
func (*Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{
		Name:       Name,
		APIVersion: pluginapi.APIVersion,
		UserAgent:  userAgent,
		RawNetwork: true,
		Summary:    "reddit search: watches a search query with your own logged-in session",
	}
}

// Docs returns this plugin's Markdown documentation.
func (*Plugin) Docs() string { return readme }

// Match handles discovery, fetch, URL rules and docs for reddit search URLs.
func (*Plugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if u == nil || !isRedditHost(u.Hostname()) || !isSearchURL(u) {
		return false
	}
	switch cap {
	case pluginapi.CapFetch, pluginapi.CapDiscover, pluginapi.CapURLPolicy, pluginapi.CapDocs:
		return true
	default:
		return false
	}
}

// Discover offers the canonical search URL as the feed. The candidate is
// Derived: the feed URL follows from the page URL alone, so the host never
// fetches the search page to validate it.
func (p *Plugin) Discover(_ context.Context, pageURL string, _ pluginapi.Host) ([]pluginapi.Candidate, error) {
	canonical := canonicalSearchURL(pageURL)
	if canonical == "" {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	return []pluginapi.Candidate{{
		FeedURL: canonical,
		Title:   searchTitle(canonical),
		HomeURL: canonical,
		Derived: true,
	}}, nil
}

// CanonicalizeFeedURL normalizes a search URL to the shape the plugin fetches:
// the old.reddit.com host, sort=new and t=all, and a /r/{sub}/search path
// folded into a subreddit: query term. It is idempotent and returns the input
// unchanged for a non-search URL.
func (*Plugin) CanonicalizeFeedURL(raw string) string {
	if c := canonicalSearchURL(raw); c != "" {
		return c
	}
	return raw
}

// FeedToken is unsupported: a search feed is not a subreddit/user token.
func (*Plugin) FeedToken(string) string { return "" }

// Fetch scrapes one page of search results into feed items, newest first. It
// returns the next-page URL for "load older" when reddit offers one.
func (p *Plugin) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
	canonical := canonicalSearchURL(req.URL)
	if canonical == "" {
		return pluginapi.Result{}, errors.New("not a reddit search URL")
	}
	cfg := p.loadConfig()
	if !cfg.Complete() {
		return pluginapi.Result{}, errors.New("reddit search is not configured: set a reddit_session cookie, or a username and password, in the admin plugins page")
	}

	body, err := p.get(ctx, applyNSFW(canonical, cfg), h)
	if err != nil {
		return pluginapi.Result{}, err
	}

	items := parseSearchResults(body)
	if len(items) == 0 {
		if isLoginWall(body) {
			p.clearSession()
			return pluginapi.Result{}, errors.New("reddit returned a login page: the session is missing or expired")
		}
		return pluginapi.Result{}, errors.New("no results found for reddit search")
	}

	return pluginapi.Result{
		Feed: pluginapi.Feed{
			Title:   searchTitle(canonical),
			HomeURL: canonical,
		},
		Items:       items,
		NextPageURL: nextPageURL(body),
	}, nil
}

// loadConfig returns the configured account.
func (p *Plugin) loadConfig() Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg
}

func (p *Plugin) setSession(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.session = s
}

func (p *Plugin) clearSession() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.session = ""
}

// cookie returns the Cookie header for a search request, logging in with the
// configured credentials when no session cookie was supplied directly.
func (p *Plugin) cookie(ctx context.Context) (string, error) {
	cfg := p.loadConfig()
	if cfg.Session != "" {
		return "reddit_session=" + cfg.Session, nil
	}
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	if session != "" {
		return session, nil
	}
	if !cfg.Complete() {
		return "", errors.New("reddit search is not configured")
	}
	session, err := p.login(ctx, cfg)
	if err != nil {
		return "", err
	}
	p.setSession(session)
	return session, nil
}

// login performs reddit's legacy form login and returns the resulting cookie
// header. It uses its own client (with redirects disabled) so the session
// cookies can be read from the response; a 2FA- or captcha-protected account
// will not complete here, which is why a pasted session cookie is preferred.
func (p *Plugin) login(ctx context.Context, cfg Config) (string, error) {
	form := url.Values{
		"user":     {cfg.Username},
		"passwd":   {cfg.Password},
		"api_type": {"json"},
		"rem":      {"true"},
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(lctx, http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("User-Agent", userAgent)
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var parts []string
	for _, c := range resp.Cookies() {
		if c.Name != "" && c.Value != "" {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	if !hasCookie(parts, "reddit_session") {
		return "", errors.New("reddit login did not return a session: the account may need 2FA or a captcha; paste a reddit_session cookie instead")
	}
	return strings.Join(parts, "; "), nil
}

// get performs one host-mediated search request. A rate-limited host comes back
// as a typed RateLimit so the feed is parked; a login wall clears the cached
// session so the next poll logs in again.
func (p *Plugin) get(ctx context.Context, endpoint string, h pluginapi.Host) ([]byte, error) {
	cookie, err := p.cookie(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    endpoint,
		Headers: map[string]string{
			"Cookie": cookie,
			"Accept": "text/html,application/xhtml+xml",
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.RateLimited {
		return nil, &pluginapi.RateLimit{URL: endpoint, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		p.clearSession()
		return nil, &pluginapi.StatusError{Code: resp.Status, URL: endpoint}
	}
	if resp.Status >= 400 {
		return nil, &pluginapi.StatusError{Code: resp.Status, URL: endpoint}
	}
	return resp.Body, nil
}

// --- URL shape ---

// canonicalSearchURL normalizes a reddit search URL, or returns "" when raw is
// not a reddit search page. It forces the old.reddit.com origin, sort=new
// and t=all, folds a /r/{sub}/search path into a subreddit: query term, and
// preserves the remaining query (paging cursors, include_over_18). It is
// idempotent.
func canonicalSearchURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !isRedditHost(u.Hostname()) || !isSearchURL(u) {
		return ""
	}
	q := strings.TrimSpace(u.Query().Get("q"))
	if sub := subredditFromPath(u.Path); sub != "" && !hasSubredditToken(q, sub) {
		q = strings.TrimSpace("subreddit:" + sub + " " + q)
	}
	if q == "" {
		return ""
	}

	vals := u.Query()
	vals.Del("restrict_sr")
	vals.Del("sr")
	vals.Set("q", q)
	vals.Set("sort", "new")
	vals.Set("t", "all")

	canonical := &url.URL{Scheme: "https", Host: "old.reddit.com", Path: "/search", RawQuery: vals.Encode()}
	return canonical.String()
}

// applyNSFW adds include_over_18=on when the account opted in. A URL that
// already carries the param keeps it (the URL wins).
func applyNSFW(canonical string, cfg Config) string {
	if !cfg.IncludeNSFW {
		return canonical
	}
	u, err := url.Parse(canonical)
	if err != nil {
		return canonical
	}
	vals := u.Query()
	if vals.Get("include_over_18") == "" {
		vals.Set("include_over_18", "on")
		u.RawQuery = vals.Encode()
	}
	return u.String()
}

// searchTitle renders a feed title from a canonical search URL's query.
func searchTitle(canonical string) string {
	u, err := url.Parse(canonical)
	if err != nil {
		return "search"
	}
	if q := strings.TrimSpace(u.Query().Get("q")); q != "" {
		return "search: " + q
	}
	return "search"
}

// subredditFromPath returns the subreddit in an /r/{sub}/search path, or "".
func subredditFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) >= 3 && strings.EqualFold(parts[0], "r") && strings.EqualFold(parts[2], "search") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// hasSubredditToken reports whether q already scopes to sub (so folding does
// not duplicate it on a second canonicalization).
func hasSubredditToken(q, sub string) bool {
	want := "subreddit:" + strings.ToLower(strings.TrimSpace(sub))
	for _, tok := range strings.Fields(q) {
		if strings.EqualFold(tok, want) {
			return true
		}
	}
	return false
}

// isRedditHost reports whether host is one of reddit's.
func isRedditHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range hosts {
		if host == h {
			return true
		}
	}
	return false
}

// isSearchURL reports whether a reddit URL is a search page (/search or
// /r/{sub}/search).
func isSearchURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if strings.EqualFold(seg, "search") {
			return true
		}
	}
	return false
}

// hasCookie reports whether a Cookie header's "name=value" parts include name.
func hasCookie(parts []string, name string) bool {
	for _, p := range parts {
		if strings.HasPrefix(p, name+"=") {
			return true
		}
	}
	return false
}

// --- scraping ---

var (
	// resultMarkerRe locates the start of each post result block. old reddit
	// tags link results with search-result-link; comment/subreddit results use
	// other classes and are skipped.
	resultMarkerRe = regexp.MustCompile(`(?i)<div class="[^"]*search-result-link[^"]*"`)

	fullnameRe = regexp.MustCompile(`data-fullname="([^"]+)"`)

	// The current result markup carries the post identity only in data-fullname;
	// the title/permalink/author/subreddit/time live in anchors and a <time>
	// element. The data-* fallbacks cover an older markup.
	authorDataRe    = regexp.MustCompile(`data-author="([^"]+)"`)
	subredditDataRe = regexp.MustCompile(`data-subreddit="([^"]+)"`)
	permalinkDataRe = regexp.MustCompile(`data-permalink="([^"]+)"`)
	timestampDataRe = regexp.MustCompile(`data-timestamp="(\d+)"`)

	titleAnchorRe     = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*search-title[^"]*"[^>]*>(.*?)</a>`)
	hrefAttrRe        = regexp.MustCompile(`(?i)href="([^"]+)"`)
	authorAnchorRe    = regexp.MustCompile(`(?is)<span class="search-author">.*?<a[^>]*>(.*?)</a>`)
	subredditAnchorRe = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*search-subreddit-link[^"]*"[^>]*>(.*?)</a>`)
	footerRe          = regexp.MustCompile(`(?is)<div class="search-result-footer">(.*?)</div>`)
	timeRe            = regexp.MustCompile(`(?is)<time[^>]*datetime="([^"]+)"`)
	nextRe            = regexp.MustCompile(`(?is)<span class="next-button">\s*<a[^>]*href="([^"]+)"`)

	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// hostTimeFormat is the plugin API's published-at format.
const hostTimeFormat = "2006-01-02 15:04:05"

// parseSearchResults extracts the post results from a search page, in page
// order, skipping any result without a post fullname.
func parseSearchResults(body []byte) []pluginapi.Item {
	idx := resultMarkerRe.FindAllIndex(body, -1)
	out := make([]pluginapi.Item, 0, len(idx))
	seen := map[string]bool{}
	for i, m := range idx {
		end := len(body)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		it, ok := itemFor(body[m[0]:end])
		if !ok || seen[it.GUID] {
			continue
		}
		seen[it.GUID] = true
		out = append(out, it)
	}
	return out
}

// itemFor builds one feed item from a result block. The post fullname (t3_<id>)
// is the GUID/identity, so the host's reddit shared-key capability cross-dedupes
// a search result with a subscribed feed.
//
// A result's image is taken from the external destination in the result footer,
// not from reddit's preview thumbnail: the thumbnail is a signed, square-cropped
// URL that expires, while the destination (i.redd.it) is the stable full image.
// Setting it as the image and mirroring it into the summary makes the host
// render the post as an image (card thumbnail + modal lightbox).
func itemFor(block []byte) (pluginapi.Item, bool) {
	fullname := firstGroup(fullnameRe, block)
	if !strings.HasPrefix(fullname, "t3_") {
		return pluginapi.Item{}, false
	}
	id := strings.TrimPrefix(fullname, "t3_")

	title, link := titleAndLink(block, id)
	it := pluginapi.Item{
		GUID:        fullname,
		Identity:    fullname,
		Title:       nonEmpty(title, "Post "+id),
		Link:        link,
		PublishedAt: publishedTime(block),
	}

	if sub := subredditFromBlock(block); sub != "" {
		it.Categories = append(it.Categories, "r/"+sub)
	}
	if author := authorFromBlock(block); author != "" {
		it.Categories = append(it.Categories, "u/"+author)
	}

	dest := destinationURL(block)
	switch {
	case imageURL(dest) != "":
		it.ImageURL = imageURL(dest)
		it.Summary = imageSummary(it.ImageURL)
	case dest != "" && !isRedditHost(hostOf(dest)):
		// A link post: show the destination in the modal body.
		it.Summary = linkSummary(dest)
	}
	return it, true
}

// titleAndLink returns the result's title text and its reddit permalink, from
// the search-title anchor (falling back to data-permalink, then a constructed
// permalink).
func titleAndLink(block []byte, id string) (title, link string) {
	if m := titleAnchorRe.FindSubmatch(block); len(m) >= 2 {
		title = cleanText(string(m[1]))
		if href := firstGroup(hrefAttrRe, m[0]); href != "" {
			link = redditPermalink(href)
		}
	}
	if link == "" {
		if pl := firstGroup(permalinkDataRe, block); pl != "" {
			link = redditPermalink(pl)
		}
	}
	if link == "" {
		link = webBase + "/comments/" + id
	}
	return title, link
}

// redditPermalink normalizes a reddit post permalink (absolute or a
// host-relative path like /r/{sub}/comments/...) to the www host, or "" when
// raw is not a reddit post URL.
func redditPermalink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Host == "" {
		// A bare path only makes sense as a reddit permalink.
		if !strings.Contains(u.Path, "/comments/") {
			return ""
		}
		base, _ := url.Parse(webBase)
		u = base.ResolveReference(u)
	}
	if !isRedditHost(u.Hostname()) || !strings.Contains(u.Path, "/comments/") {
		return ""
	}
	u.Scheme = "https"
	u.Host = "www.reddit.com"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// publishedTime reads a result's post time: the <time datetime> (RFC3339) in
// the current markup, else the older data-timestamp (milliseconds).
func publishedTime(block []byte) string {
	if d := strings.TrimSpace(firstGroup(timeRe, block)); d != "" {
		if t, err := time.Parse(time.RFC3339, d); err == nil {
			return t.UTC().Format(hostTimeFormat)
		}
	}
	return formatMillis(firstGroup(timestampDataRe, block))
}

// subredditFromBlock extracts the result's subreddit name (without the "r/"
// prefix) from the search-subreddit-link anchor's href (reliable even when the
// name is split by highlight spans), else its text, else data-subreddit.
func subredditFromBlock(block []byte) string {
	if href := firstGroup(hrefAttrRe, firstMatch(subredditAnchorRe, block)); href != "" {
		if sub := subredditFromHref(href); sub != "" {
			return sub
		}
	}
	// The text form can be split by highlight spans ("r/keyholder" + "captions"),
	// so drop whitespace before reading it.
	if s := cleanText(firstGroup(subredditAnchorRe, block)); s != "" {
		s = strings.ReplaceAll(s, " ", "")
		if s = strings.TrimPrefix(s, "r/"); s != "" {
			return s
		}
	}
	return strings.TrimSpace(firstGroup(subredditDataRe, block))
}

// subredditFromHref returns the subreddit in an /r/{sub}/... URL path, or "".
func subredditFromHref(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "r") && parts[1] != "" {
		return parts[1]
	}
	return ""
}

// authorFromBlock extracts the result's author from the search-author anchor,
// else data-author.
func authorFromBlock(block []byte) string {
	if a := cleanText(firstGroup(authorAnchorRe, block)); a != "" {
		return a
	}
	return strings.TrimSpace(firstGroup(authorDataRe, block))
}

// destinationURL returns the external destination in a result's footer ("[link]"
// for a link post), or "".
func destinationURL(block []byte) string {
	m := footerRe.FindSubmatch(block)
	if len(m) < 2 {
		return ""
	}
	raw := html.UnescapeString(strings.TrimSpace(firstGroup(hrefAttrRe, m[1])))
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// imageURL returns raw when it points at an image, else "". A reddit/imgur image
// host counts even without a file extension.
func imageURL(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	if !isImagePath(u.Path) && !isImageHost(u.Hostname()) {
		return ""
	}
	return u.String()
}

// isImagePath reports whether a URL path ends in a known image extension.
func isImagePath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".bmp":
		return true
	}
	return false
}

// isImageHost reports whether a host serves images even without a file
// extension.
func isImageHost(host string) bool {
	switch strings.ToLower(host) {
	case "i.redd.it", "preview.redd.it", "i.imgur.com":
		return true
	}
	return false
}

// imageSummary renders an image as the item body so the host classifies the post
// as an image (single <img>, no text) and the modal shows it.
func imageSummary(img string) string {
	e := html.EscapeString(img)
	return `<a href="` + e + `"><img src="` + e + `" alt=""></a>`
}

// linkSummary renders a link post's destination as its body.
func linkSummary(dest string) string {
	e := html.EscapeString(dest)
	return `<p><a href="` + e + `" class="external">` + e + `</a></p>`
}

// nextPageURL extracts the "next" pagination link from a search page, as an
// absolute old.reddit.com URL the host stores for "load older".
func nextPageURL(body []byte) string {
	href := firstGroup(nextRe, body)
	if href == "" {
		return ""
	}
	href = html.UnescapeString(strings.TrimSpace(href))
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		base, _ := url.Parse(searchBase)
		u = base.ResolveReference(u)
	}
	if c := canonicalSearchURL(u.String()); c != "" {
		return c
	}
	return u.String()
}

// isLoginWall reports whether a page is reddit's login form rather than a
// result listing (a missing or expired session).
func isLoginWall(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "login-form") || strings.Contains(s, "you must be logged in") || strings.Contains(s, "id=\"login\"")
}

// --- small helpers ---

func firstGroup(re *regexp.Regexp, b []byte) string {
	m := re.FindSubmatch(b)
	if len(m) < 2 {
		return ""
	}
	return string(m[1])
}

// firstMatch returns the first full regexp match, or nil.
func firstMatch(re *regexp.Regexp, b []byte) []byte {
	return re.Find(b)
}

// cleanText strips tags and collapses whitespace from an HTML fragment.
func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// formatMillis converts a "data-timestamp" millisecond epoch to the host's
// published-at format ("" when missing or unparsable).
func formatMillis(raw string) string {
	ms, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(hostTimeFormat)
}

// nonEmpty returns the first non-blank value.
func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// hostOf returns a URL's lowercase host, or "" when it cannot be parsed.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
