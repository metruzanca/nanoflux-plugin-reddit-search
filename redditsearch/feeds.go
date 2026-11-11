package redditsearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmlstd "html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	xhtml "golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// feedTarget is a subreddit or user posts feed resolved to the shapes this
// plugin fetches and falls back to. The beta listing fetch is gated by the
// fetch_feeds setting in Match.
type feedTarget struct {
	kind      string // "r" or "u"
	name      string // subreddit or username
	scrapeURL string // old.reddit /new listing
	rssURL    string // legacy .rss endpoint, used only as a temporary fallback
	title     string // "r/sub" or "u/name"
	homeURL   string // human page on www
}

// listingTarget resolves a reddit subreddit or user posts URL to a feedTarget.
// It accepts the legacy .rss shapes (existing subscriptions) and the listing
// shapes, but deliberately rejects search URLs and comment feeds: search is
// handled by the search path, and comments are out of scope.
func listingTarget(raw string) (feedTarget, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !isRedditHost(u.Hostname()) || isSearchURL(u) {
		return feedTarget{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return feedTarget{}, false
	}

	switch strings.ToLower(parts[0]) {
	case "r":
		sub := strings.TrimSuffix(parts[1], ".rss")
		if sub == "" {
			return feedTarget{}, false
		}
		if !subredditRestOK(parts[2:]) {
			return feedTarget{}, false
		}
		home := "https://www.reddit.com/r/" + sub
		scrape := &url.URL{Scheme: "https", Host: "old.reddit.com", Path: "/r/" + sub + "/new/"}
		if q := paginationQuery(u); len(q) > 0 {
			scrape.RawQuery = q.Encode()
		}
		return feedTarget{
			kind:      "r",
			name:      sub,
			scrapeURL: scrape.String(),
			rssURL:    "https://www.reddit.com/r/" + sub + "/.rss",
			title:     "r/" + sub,
			homeURL:   home,
		}, true
	case "user", "u":
		name := strings.TrimSuffix(parts[1], ".rss")
		if name == "" {
			return feedTarget{}, false
		}
		if !userRestOK(parts[2:]) {
			return feedTarget{}, false
		}
		home := "https://www.reddit.com/user/" + name
		scrape := &url.URL{Scheme: "https", Host: "old.reddit.com", Path: "/user/" + name + "/submitted/"}
		q := paginationQuery(u)
		q.Set("sort", "new")
		scrape.RawQuery = q.Encode()
		return feedTarget{
			kind:      "u",
			name:      name,
			scrapeURL: scrape.String(),
			rssURL:    "https://www.reddit.com/user/" + name + "/submitted.rss",
			title:     "u/" + name,
			homeURL:   home,
		}, true
	}
	return feedTarget{}, false
}

// subredditRestOK reports whether the segments after /r/{sub} are a posts
// listing shape. Empty, .rss, new and new/.rss are accepted. Anything else
// (comments, search, wiki, an explicit post permalink) is not a feed.
func subredditRestOK(rest []string) bool {
	rest = trimEmpty(rest)
	switch len(rest) {
	case 0:
		return true
	case 1:
		switch strings.ToLower(rest[0]) {
		case ".rss", "new", "new.rss", "hot", "hot.rss", "top", "top.rss",
			"rising", "rising.rss", "controversial", "controversial.rss":
			return true
		}
	case 2:
		switch strings.ToLower(rest[0]) {
		case "new", "hot", "top", "rising", "controversial":
			if strings.EqualFold(rest[1], ".rss") {
				return true
			}
		}
	}
	return false
}

// userRestOK reports whether the segments after /user/{name} are a submitted
// posts listing. Empty, .rss, submitted, submitted.rss and submitted/new are
// accepted. Comment and other profile tabs are rejected.
func userRestOK(rest []string) bool {
	rest = trimEmpty(rest)
	switch len(rest) {
	case 0:
		return true
	case 1:
		switch strings.ToLower(rest[0]) {
		case ".rss", "submitted", "submitted.rss":
			return true
		}
	case 2:
		if strings.EqualFold(rest[0], "submitted") {
			switch strings.ToLower(rest[1]) {
			case ".rss", "new", "new.rss", "hot", "hot.rss", "top", "top.rss":
				return true
			}
		}
	}
	return false
}

// paginationQuery copies the listing cursor and NSFW params from a feed or
// next-page URL. The fetch target itself is always newest-first, so a stored
// sort is not carried over.
func paginationQuery(u *url.URL) url.Values {
	out := url.Values{}
	if u == nil {
		return out
	}
	in := u.Query()
	for _, key := range []string{"after", "before", "count", "include_over_18"} {
		if v := in.Get(key); v != "" {
			out.Set(key, v)
		}
	}
	return out
}

// trimEmpty drops empty segments created by a trailing slash.
func trimEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// --- listing fetch ---

// fetchListing scrapes a subreddit or user /new listing. When the session is
// missing, expired or refused, it falls back to the still-live .rss endpoint so
// an install keeps working while the operator sets up the cookie. A rate limit
// never falls back: the host must back off the shared reddit host instead.
func (p *Plugin) fetchListing(ctx context.Context, target feedTarget, h pluginapi.Host) (pluginapi.Result, error) {
	cfg := p.loadConfig()
	var scrapeErr error

	if cfg.Complete() {
		cookie, err := p.cookie(ctx)
		if err != nil {
			scrapeErr = err
		} else {
			if cfg.IncludeNSFW {
				cookie += "; over18=1"
			}
			endpoint := target.scrapeURL
			if cfg.IncludeNSFW && target.kind == "u" {
				endpoint = addQueryParam(endpoint, "include_over_18", "on")
			}
			body, err := p.getWith(ctx, endpoint, cookie, h)
			switch {
			case err == nil:
				if isLoginWall(body) || !isListingPage(body) {
					p.clearSession()
					scrapeErr = errors.New("reddit returned a login or block page: the session is missing or expired")
					break
				}
				items := parseListing(body)
				if len(items) == 0 {
					h.Logf("reddit listing %s parsed no posts", target.scrapeURL)
				}
				return pluginapi.Result{
					Feed:        pluginapi.Feed{Title: target.title, HomeURL: target.homeURL},
					Items:       items,
					NextPageURL: listingNextPage(body),
				}, nil
			case isRateLimitError(err):
				return pluginapi.Result{}, err
			case isAuthError(err):
				scrapeErr = err
			default:
				return pluginapi.Result{}, err
			}
		}
	}

	res, rssErr := p.fetchRSS(ctx, target, h)
	if rssErr == nil {
		if scrapeErr != nil {
			h.Logf("reddit feed scrape failed, used .rss fallback: %v", scrapeErr)
		}
		return res, nil
	}
	if scrapeErr != nil {
		return pluginapi.Result{}, fmt.Errorf("reddit feed scrape failed (%v); .rss fallback failed: %w", scrapeErr, rssErr)
	}
	if !cfg.Complete() {
		return pluginapi.Result{}, fmt.Errorf("reddit feed is not configured: set a reddit_session cookie in the admin plugins page; .rss fallback failed: %w", rssErr)
	}
	return pluginapi.Result{}, rssErr
}

// fetchRSS reads the legacy .rss endpoint as a temporary bridge. It does not
// send the session cookie: .rss is public, and an expired cookie should not
// make the fallback fail too.
func (p *Plugin) fetchRSS(ctx context.Context, target feedTarget, h pluginapi.Host) (pluginapi.Result, error) {
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    target.rssURL,
		Headers: map[string]string{
			"Accept": "application/atom+xml, application/rss+xml, application/xml, text/xml, */*",
		},
	})
	if err != nil {
		return pluginapi.Result{}, err
	}
	if resp.RateLimited {
		return pluginapi.Result{}, &pluginapi.RateLimit{URL: target.rssURL, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status >= 400 {
		return pluginapi.Result{}, &pluginapi.StatusError{Code: resp.Status, URL: target.rssURL}
	}
	items, err := parseRSS(resp.Body, target)
	if err != nil {
		return pluginapi.Result{}, err
	}
	return pluginapi.Result{
		Feed:  pluginapi.Feed{Title: target.title, HomeURL: target.homeURL},
		Items: items,
	}, nil
}

// parseRSS maps the legacy Atom feed into the same item shape the listing
// scraper produces: a t3_ identity, a shared key, and r//u/ categories. It is
// deliberately best-effort; the fallback exists only until Nov 13.
func parseRSS(body []byte, target feedTarget) ([]pluginapi.Item, error) {
	parsed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.Item, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		if !strings.HasPrefix(it.GUID, "t3_") {
			continue
		}
		item := pluginapi.Item{
			GUID:        it.GUID,
			Identity:    it.GUID,
			SharedKey:   "reddit:" + it.GUID,
			Title:       it.Title,
			Link:        it.Link,
			Summary:     nonEmpty(it.Content, it.Description),
			PublishedAt: rssPublishedAt(it),
		}
		if target.kind == "r" {
			item.Categories = append(item.Categories, "r/"+target.name)
		} else {
			item.Categories = append(item.Categories, "u/"+target.name)
			for _, c := range it.Categories {
				if sub := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c)), "r/"); sub != "" {
					item.Categories = append(item.Categories, "r/"+sub)
				}
			}
		}
		if author := rssAuthorName(it); author != "" {
			item.Categories = append(item.Categories, "u/"+author)
		}
		if img := rssImage(it); img != "" {
			item.ImageURL = img
		}
		out = append(out, item)
	}
	return out, nil
}

// --- listing parsing ---

// isListingPage reports whether body is an old.reddit listing page rather than
// a login, block or error page. It is used when a page parses to zero items: a
// real empty listing still has the site table, while a login wall does not.
func isListingPage(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, `id="sitetable"`) ||
		strings.Contains(s, `class="thing`) ||
		strings.Contains(s, `class=" thing`)
}

// parseListing extracts post items from an old.reddit listing page, in page
// order. It walks div.thing nodes and ignores everything that is not a t3_
// post.
func parseListing(body []byte) []pluginapi.Item {
	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var out []pluginapi.Item
	seen := map[string]bool{}
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "div" && hasClass(n, "thing") {
			if it, ok := listingItem(n); ok && !seen[it.GUID] {
				seen[it.GUID] = true
				out = append(out, it)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// listingItem builds one item from a div.thing post block.
func listingItem(n *xhtml.Node) (pluginapi.Item, bool) {
	fullname := attrValue(n, "data-fullname")
	if !strings.HasPrefix(fullname, "t3_") {
		return pluginapi.Item{}, false
	}
	id := strings.TrimPrefix(fullname, "t3_")
	if id == "" {
		return pluginapi.Item{}, false
	}

	title := ""
	if a := firstDescendant(n, func(x *xhtml.Node) bool {
		return x.Data == "a" && hasClass(x, "title")
	}); a != nil {
		title = cleanText(nodeText(a))
	}

	link := redditPermalink(attrValue(n, "data-permalink"))
	if link == "" {
		link = webBase + "/comments/" + id
	}

	dest := attrValue(n, "data-url")
	if dest == "" {
		dest = link
	}

	it := pluginapi.Item{
		GUID:      fullname,
		Identity:  fullname,
		SharedKey: "reddit:" + fullname,
		Title:     nonEmpty(title, "Post "+id),
		Link:      link,
	}
	if sub := strings.TrimSpace(attrValue(n, "data-subreddit")); sub != "" {
		it.Categories = append(it.Categories, "r/"+sub)
	}
	if author := strings.TrimSpace(attrValue(n, "data-author")); author != "" {
		it.Categories = append(it.Categories, "u/"+author)
	}

	it.PublishedAt = listingPublishedAt(n)

	body := ""
	if div := firstDescendant(n, func(x *xhtml.Node) bool {
		return x.Data == "div" && hasClass(x, "usertext-body")
	}); div != nil {
		body = strings.TrimSpace(innerHTML(div))
	}

	if img := imageURL(dest); img != "" {
		it.ImageURL = img
		it.Summary = imageSummary(img)
		return it, true
	}

	var parts []string
	if body != "" {
		parts = append(parts, body)
	}
	if dest != "" {
		parts = append(parts, `<p><a href="`+htmlstd.EscapeString(dest)+`">[link]</a></p>`)
	}
	it.Summary = strings.Join(parts, "\n")
	return it, true
}

// listingPublishedAt reads a post's time from the listing tagline's <time>
// element, falling back to data-timestamp. Old reddit has used both seconds and
// milliseconds, so the magnitude decides.
func listingPublishedAt(n *xhtml.Node) string {
	if t := firstDescendant(n, func(x *xhtml.Node) bool { return x.Data == "time" }); t != nil {
		if d := strings.TrimSpace(attrValue(t, "datetime")); d != "" {
			if pt, err := time.Parse(time.RFC3339, d); err == nil {
				return pt.UTC().Format(hostTimeFormat)
			}
		}
	}
	raw := strings.TrimSpace(attrValue(n, "data-timestamp"))
	if raw == "" {
		return ""
	}
	sec, err := parseInt64(raw)
	if err != nil || sec <= 0 {
		return ""
	}
	if sec > 1_000_000_000_000 {
		return time.UnixMilli(sec).UTC().Format(hostTimeFormat)
	}
	return time.Unix(sec, 0).UTC().Format(hostTimeFormat)
}

// listingNextPage extracts the "next" pagination link from a listing page.
func listingNextPage(body []byte) string {
	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	var next *xhtml.Node
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if next != nil {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "span" && hasClass(n, "next-button") {
			next = firstDescendant(n, func(x *xhtml.Node) bool { return x.Data == "a" })
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if next == nil {
		return ""
	}
	href := strings.TrimSpace(htmlstd.UnescapeString(attrValue(next, "href")))
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		base, _ := url.Parse(searchBase)
		u = base.ResolveReference(u)
	}
	if !isRedditHost(u.Hostname()) {
		return ""
	}
	return u.String()
}

// --- node helpers ---

func hasClass(n *xhtml.Node, want string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "class") {
			for _, c := range strings.Fields(a.Val) {
				if c == want {
					return true
				}
			}
		}
	}
	return false
}

func attrValue(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func firstDescendant(n *xhtml.Node, pred func(*xhtml.Node) bool) *xhtml.Node {
	if pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := firstDescendant(c, pred); got != nil {
			return got
		}
	}
	return nil
}

func nodeText(n *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(x *xhtml.Node) {
		if x.Type == xhtml.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func innerHTML(n *xhtml.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = xhtml.Render(&b, c)
	}
	return b.String()
}

// --- small helpers ---

func parseInt64(raw string) (int64, error) {
	var n int64
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}

func addQueryParam(raw, key, val string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get(key) == "" {
		q.Set(key, val)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func isRateLimitError(err error) bool {
	var rl *pluginapi.RateLimit
	return errors.As(err, &rl)
}

func isAuthError(err error) bool {
	var se *pluginapi.StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden
}

func rssPublishedAt(it *gofeed.Item) string {
	t := it.PublishedParsed
	if t == nil {
		t = it.UpdatedParsed
	}
	if t == nil {
		return ""
	}
	return t.UTC().Format(hostTimeFormat)
}

func rssAuthorName(it *gofeed.Item) string {
	if it.Author == nil {
		return ""
	}
	name := strings.TrimSpace(it.Author.Name)
	name = strings.TrimPrefix(name, "/u/")
	name = strings.TrimPrefix(name, "u/")
	return name
}

func rssImage(it *gofeed.Item) string {
	if it.Image != nil && it.Image.URL != "" {
		return it.Image.URL
	}
	// Reddit puts the preview thumbnail in media:thumbnail, which gofeed keeps
	// in the generic extension map rather than Item.Image.
	if media, ok := it.Extensions["media"]; ok {
		for _, e := range media["thumbnail"] {
			if u := strings.TrimSpace(e.Attrs["url"]); u != "" {
				return u
			}
		}
	}
	for _, e := range it.Enclosures {
		if e != nil && strings.HasPrefix(strings.ToLower(e.Type), "image/") {
			return e.URL
		}
	}
	return ""
}
