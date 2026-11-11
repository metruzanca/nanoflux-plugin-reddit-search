package redditsearch

import (
	"bytes"
	"context"
	"errors"
	htmlstd "html"
	"net/http"
	"net/url"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// feedTarget is a subreddit or user posts feed resolved to its /new listing.
type feedTarget struct {
	kind      string // "r" or "u"
	name      string // subreddit or username
	scrapeURL string // old.reddit /new listing
	title     string // "r/sub" or "u/name"
	homeURL   string // human page on www
}

// listingTarget resolves a reddit subreddit or user posts URL to a feedTarget.
// It accepts the legacy .rss shapes (existing subscriptions) and the listing
// shapes, but deliberately rejects search URLs and non-post paths: search is
// handled by the search path, and only posts are in scope.
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
			title:     "u/" + name,
			homeURL:   home,
		}, true
	}
	return feedTarget{}, false
}

// subredditRestOK reports whether the segments after /r/{sub} are a posts
// listing shape. Empty, .rss, new and new/.rss are accepted. Anything else
// (search, wiki, an explicit post permalink) is not a feed.
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
// accepted. Other profile tabs are rejected.
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

// fetchListing scrapes a subreddit or user /new listing. There is no .rss
// fallback: the listing is the only fetch path, so a missing or expired session
// is an error the feed card surfaces until the operator fixes it.
func (p *Plugin) fetchListing(ctx context.Context, target feedTarget, h pluginapi.Host) (pluginapi.Result, error) {
	cfg := p.loadConfig()
	if !cfg.Complete() {
		return pluginapi.Result{}, errors.New("reddit is not configured: set a reddit_session cookie, or a username and password, in the admin plugins page")
	}
	cookie, err := p.cookie(ctx)
	if err != nil {
		return pluginapi.Result{}, err
	}
	if cfg.IncludeNSFW {
		cookie += "; over18=1"
	}
	endpoint := target.scrapeURL
	if cfg.IncludeNSFW && target.kind == "u" {
		endpoint = addQueryParam(endpoint, "include_over_18", "on")
	}

	resp, err := p.getPage(ctx, endpoint, cookie, h)
	if err != nil {
		return pluginapi.Result{}, err
	}
	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		p.clearSession()
		return pluginapi.Result{}, &pluginapi.StatusError{Code: resp.Status, URL: endpoint}
	case resp.Status == http.StatusNotFound || resp.Status == http.StatusGone:
		if pe, ok := permanentFailure(target, resp.Status, resp.Body); ok {
			return pluginapi.Result{}, pe
		}
		return pluginapi.Result{}, &pluginapi.StatusError{Code: resp.Status, URL: endpoint}
	case resp.Status >= 400:
		return pluginapi.Result{}, &pluginapi.StatusError{Code: resp.Status, URL: endpoint}
	}
	if isLoginWall(resp.Body) || !isListingPage(resp.Body) {
		p.clearSession()
		return pluginapi.Result{}, errors.New("reddit returned a login or block page: the session is missing or expired")
	}
	items := parseListing(resp.Body)
	if len(items) == 0 {
		h.Logf("reddit listing %s parsed no posts", target.scrapeURL)
	}
	return pluginapi.Result{
		Feed:        pluginapi.Feed{Title: target.title, HomeURL: target.homeURL},
		Items:       items,
		NextPageURL: listingNextPage(resp.Body),
	}, nil
}

// getPage performs one host-mediated request and returns the full response so
// the caller can inspect an error body. A rate-limited host becomes a typed
// RateLimit, as it does for search.
func (p *Plugin) getPage(ctx context.Context, endpoint, cookie string, h pluginapi.Host) (pluginapi.HTTPResponse, error) {
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    endpoint,
		Headers: map[string]string{
			"Cookie": cookie,
			"Accept": "text/html,application/xhtml+xml",
		},
	})
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	if resp.RateLimited {
		return resp, &pluginapi.RateLimit{URL: endpoint, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	return resp, nil
}

// permanentFailure recognizes a community or profile that reddit says is
// banned, deleted or suspended. A generic 404 stays transient, matching the
// legacy plugin: an ordinary missing page or a typo is not a reason to pause.
func permanentFailure(target feedTarget, status int, body []byte) (*pluginapi.PermanentError, bool) {
	if status != http.StatusNotFound && status != http.StatusGone {
		return nil, false
	}
	title := strings.ToLower(pageTitle(body))
	text := strings.ToLower(string(body))
	for _, state := range []string{"banned", "deleted", "suspended"} {
		if strings.HasSuffix(title, ": "+state) || mentionState(text, state, target.kind) {
			kind := "feed"
			switch target.kind {
			case "r":
				kind = "community"
			case "u":
				kind = "profile"
			}
			return &pluginapi.PermanentError{
				Code:   status,
				Reason: "reddit " + kind + " was " + state,
			}, true
		}
	}
	return nil, false
}

// mentionState matches the body phrases reddit uses for a gone community or
// profile. It avoids a bare "banned" check: reddit error pages mention the word
// in unrelated navigation text. The phrase set is scoped to the feed kind so a
// user 404 cannot pause because some community link says it was banned.
func mentionState(text, state, kind string) bool {
	switch kind {
	case "r":
		switch state {
		case "banned":
			return strings.Contains(text, "community has been banned") ||
				strings.Contains(text, "this community has been banned")
		case "deleted":
			return strings.Contains(text, "community has been deleted")
		case "suspended":
			return strings.Contains(text, "community has been suspended")
		}
	case "u":
		switch state {
		case "banned":
			return strings.Contains(text, "user has been banned") ||
				strings.Contains(text, "account has been banned")
		case "deleted":
			return strings.Contains(text, "user has been deleted") ||
				strings.Contains(text, "account has been deleted")
		case "suspended":
			return strings.Contains(text, "user has been suspended") ||
				strings.Contains(text, "account has been suspended")
		}
	}
	return false
}

// pageTitle returns a page's <title> text, or "".
func pageTitle(body []byte) string {
	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	if t := firstDescendant(doc, func(n *xhtml.Node) bool {
		return n.Type == xhtml.ElementNode && n.Data == "title"
	}); t != nil {
		return cleanText(nodeText(t))
	}
	return ""
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

	// A non-self post can carry reddit's preview thumbnail for its destination.
	// Keep it as the row image so a link, gallery or video card shows a preview
	// without opening the item. Self posts have no destination preview.
	isSelf := strings.EqualFold(attrValue(n, "data-type"), "self")
	if !isSelf && dest != "" && dest != link {
		it.ImageURL = listingThumbnail(n)
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

// listingThumbnail returns reddit's preview image for a listing post, or "".
// It only accepts reddit's preview hosts: the listing also carries generic
// icons and default thumbnails that should not be shown as content previews.
func listingThumbnail(n *xhtml.Node) string {
	var raw string
	if a := firstDescendant(n, func(x *xhtml.Node) bool {
		return x.Type == xhtml.ElementNode && x.Data == "a" && hasClass(x, "thumbnail")
	}); a != nil {
		if img := firstDescendant(a, func(x *xhtml.Node) bool {
			return x.Type == xhtml.ElementNode && x.Data == "img"
		}); img != nil {
			raw = attrValue(img, "src")
		}
	}
	if raw == "" {
		raw = attrValue(n, "data-thumbnail")
	}
	raw = strings.TrimSpace(htmlstd.UnescapeString(raw))
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
	switch strings.ToLower(u.Hostname()) {
	case "external-preview.redd.it", "preview.redd.it":
		return u.String()
	}
	return ""
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
