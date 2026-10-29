package redditsearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// fakeHost is an in-process pluginapi.Host for tests.
type fakeHost struct {
	do      func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)
	lastReq pluginapi.HTTPRequest
}

func (h *fakeHost) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	h.lastReq = req
	if h.do != nil {
		return h.do(context.Background(), req)
	}
	return pluginapi.HTTPResponse{Status: 200}, nil
}
func (h *fakeHost) Now() time.Time      { return time.Unix(0, 0).UTC() }
func (h *fakeHost) Logf(string, ...any) {}

// searchFixture mirrors the current old.reddit.com search result markup: the
// post identity is data-fullname only, and title/link/author/subreddit/time/
// destination live in anchors and a <time> element (no data-timestamp etc.).
const searchFixture = `<!doctype html><html><body>
<div class=" search-result search-result-link has-thumbnail has-linkflair " data-fullname="t3_abc123">
  <a href="/r/golang/comments/abc123/some_title/" class="may-blank thumbnail "><img src="//preview.redd.it/abc123.jpeg?width=140&amp;height=140&amp;crop=1:1,smart&amp;auto=webp&amp;s=sig" width="70" height="70" alt=""></a>
  <div>
    <header class="search-result-header"><a href="https://old.reddit.com/r/golang/comments/abc123/some_title/" class="search-title may-blank">Some &amp; Title</a></header>
    <div class="search-result-meta"><span class="search-time">submitted <time title="x" datetime="2023-11-14T22:13:20+00:00">then</time></span> <span class="search-author">by <a href="https://old.reddit.com/user/LeftArmOfExodia" class="author may-blank id-t2_x">LeftArmOfExodia</a><span class="userattrs"></span></span> <span>to <a href="https://www.reddit.com/r/golang/" class="search-subreddit-link may-blank"><span>r/<span class="highlightwords">golang</span></span></a></span></div>
    <div class="search-result-footer"><span class="search-result-icon search-result-icon-external"></span><a href="https://i.redd.it/abc123.jpeg" class="search-link may-blank">https://i.redd.it/abc123.jpeg</a></div>
  </div>
</div>
<div class=" search-result search-result-link " data-fullname="t3_def456">
  <a href="/r/rust/comments/def456/other/" class="may-blank thumbnail "><img src="//preview.redd.it/def456.jpg?s=x" width="70" height="70" alt=""></a>
  <div>
    <header class="search-result-header"><a href="https://www.reddit.com/r/rust/comments/def456/other/" class="search-title may-blank">Other</a></header>
    <div class="search-result-meta"><span class="search-time">submitted <time datetime="2023-11-14T22:15:00+00:00">then</time></span> <span class="search-author">by <a href="/user/someone" class="author">someone</a></span> <span>to <a href="/r/rust/" class="search-subreddit-link">r/rust</a></span></div>
    <div class="search-result-footer"><span class="search-result-icon"></span><a href="https://example.com/article" class="search-link may-blank">https://example.com/article</a></div>
  </div>
</div>
<span class="next-button"><a href="https://old.reddit.com/search?q=foo&amp;sort=new&amp;t=all&amp;after=t3_def456&amp;count=25" rel="nofollow next">next</a></span>
</body></html>`

// oldFixture is the older markup, where the fields are data-* attributes and the
// image is only reddit's preview thumbnail.
const oldFixture = `<div class="search-result search-result-link" data-fullname="t3_old1" data-author="alice" data-subreddit="cats" data-permalink="/r/cats/comments/old1/hello/" data-timestamp="1700000000000"><a class="search-title may-blank" href="/r/cats/comments/old1/hello/">Hello</a></div>`

func TestCanonicalSearchURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "global search forces sort and time",
			in:   "https://old.reddit.com/search?q=author%3ALeftArmOfExodia+&include_over_18=on&sort=hot&t=week",
			want: "https://old.reddit.com/search?include_over_18=on&q=author%3ALeftArmOfExodia&sort=new&t=all",
		},
		{
			name: "subreddit search folds into a subreddit term",
			in:   "https://old.reddit.com/r/golang/search?q=generics&restrict_sr=on",
			want: "https://old.reddit.com/search?q=subreddit%3Agolang+generics&sort=new&t=all",
		},
		{
			name: "bare subreddit search",
			in:   "https://www.reddit.com/r/golang/search",
			want: "https://old.reddit.com/search?q=subreddit%3Agolang&sort=new&t=all",
		},
		{
			name: "existing subreddit token is not duplicated",
			in:   "https://old.reddit.com/r/golang/search?q=subreddit%3Agolang+generics",
			want: "https://old.reddit.com/search?q=subreddit%3Agolang+generics&sort=new&t=all",
		},
		{
			name: "any reddit host normalizes to old",
			in:   "https://www.reddit.com/search?q=foo",
			want: "https://old.reddit.com/search?q=foo&sort=new&t=all",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalSearchURL(tc.in)
			if got != tc.want {
				t.Fatalf("canonicalSearchURL(%q) =\n  %q\nwant\n  %q", tc.in, got, tc.want)
			}
			if again := canonicalSearchURL(got); again != got {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
		})
	}

	for _, in := range []string{
		"https://old.reddit.com/r/golang",
		"https://old.reddit.com/r/golang/comments/abc/x/",
		"https://example.com/search?q=foo",
		"https://old.reddit.com/search", // no query
	} {
		if got := canonicalSearchURL(in); got != "" {
			t.Errorf("canonicalSearchURL(%q) = %q, want empty", in, got)
		}
	}
}

func TestApplyNSFW(t *testing.T) {
	base := "https://old.reddit.com/search?q=foo&sort=new&t=all"
	if got := applyNSFW(base, Config{IncludeNSFW: true}); !strings.Contains(got, "include_over_18=on") {
		t.Fatalf("toggle on: %q", got)
	}
	if got := applyNSFW(base, Config{}); strings.Contains(got, "include_over_18") {
		t.Fatalf("toggle off should not add the param: %q", got)
	}
	// A URL that already carries it keeps it.
	with18 := base + "&include_over_18=on"
	if got := applyNSFW(with18, Config{}); !strings.Contains(got, "include_over_18=on") {
		t.Fatalf("URL param should win: %q", got)
	}
}

func TestParseSearchResults(t *testing.T) {
	items := parseSearchResults([]byte(searchFixture))
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first := items[0]
	if first.GUID != "t3_abc123" {
		t.Errorf("GUID = %q", first.GUID)
	}
	if first.Title != "Some & Title" {
		t.Errorf("Title = %q", first.Title)
	}
	if first.Link != "https://www.reddit.com/r/golang/comments/abc123/some_title/" {
		t.Errorf("Link = %q", first.Link)
	}
	// The stable external image is used, not the signed preview thumbnail.
	if first.ImageURL != "https://i.redd.it/abc123.jpeg" {
		t.Errorf("ImageURL = %q", first.ImageURL)
	}
	if !strings.Contains(first.Summary, `<img src="https://i.redd.it/abc123.jpeg"`) {
		t.Errorf("image post summary should embed the image: %q", first.Summary)
	}
	if len(first.Categories) != 2 || first.Categories[0] != "r/golang" || first.Categories[1] != "u/LeftArmOfExodia" {
		t.Errorf("Categories = %v", first.Categories)
	}
	if first.PublishedAt != "2023-11-14 22:13:20" {
		t.Errorf("PublishedAt = %q", first.PublishedAt)
	}

	second := items[1]
	if second.PublishedAt != "2023-11-14 22:15:00" {
		t.Errorf("second PublishedAt = %q", second.PublishedAt)
	}
	if second.ImageURL != "" {
		t.Errorf("a link post should have no image, got %q", second.ImageURL)
	}
	if !strings.Contains(second.Summary, `href="https://example.com/article"`) {
		t.Errorf("link post summary should carry the destination: %q", second.Summary)
	}
	if second.Categories[0] != "r/rust" || second.Categories[1] != "u/someone" {
		t.Errorf("second Categories = %v", second.Categories)
	}
}

// The older markup (data-* attributes, no footer) still parses title, link,
// author, subreddit and the millisecond timestamp.
func TestParseSearchResultsOldMarkup(t *testing.T) {
	items := parseSearchResults([]byte(oldFixture))
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	it := items[0]
	if it.Title != "Hello" || it.Link != "https://www.reddit.com/r/cats/comments/old1/hello/" {
		t.Errorf("item = %+v", it)
	}
	if it.Categories[0] != "r/cats" || it.Categories[1] != "u/alice" {
		t.Errorf("Categories = %v", it.Categories)
	}
	if it.PublishedAt != "2023-11-14 22:13:20" {
		t.Errorf("PublishedAt = %q", it.PublishedAt)
	}
}

func TestNextPageURL(t *testing.T) {
	got := nextPageURL([]byte(searchFixture))
	want := "https://old.reddit.com/search?after=t3_def456&count=25&q=foo&sort=new&t=all"
	if got != want {
		t.Fatalf("nextPageURL = %q, want %q", got, want)
	}
	if nextPageURL([]byte("<html></html>")) != "" {
		t.Fatal("expected empty next page url")
	}
}

func TestDiscover(t *testing.T) {
	p := &Plugin{}
	cs, err := p.Discover(context.Background(), "https://old.reddit.com/r/golang/search?q=generics", &fakeHost{})
	if err != nil || len(cs) != 1 {
		t.Fatalf("Discover = %v, %v", cs, err)
	}
	c := cs[0]
	if !c.Derived {
		t.Error("candidate should be Derived")
	}
	if c.FeedURL != "https://old.reddit.com/search?q=subreddit%3Agolang+generics&sort=new&t=all" {
		t.Errorf("FeedURL = %q", c.FeedURL)
	}
	if c.Title != "search: subreddit:golang generics" {
		t.Errorf("Title = %q", c.Title)
	}
	if _, err := p.Discover(context.Background(), "https://example.com/", &fakeHost{}); !errors.Is(err, pluginapi.ErrUnsupportedCapability) {
		t.Errorf("non-search Discover err = %v", err)
	}
}

func TestMatch(t *testing.T) {
	p := &Plugin{}
	must := func(raw string) *url.URL { u, _ := url.Parse(raw); return u }
	if !p.Match(must("https://old.reddit.com/search?q=x"), pluginapi.CapFetch) {
		t.Error("search URL should match fetch")
	}
	if !p.Match(must("https://old.reddit.com/r/golang/search"), pluginapi.CapDiscover) {
		t.Error("subreddit search URL should match discover")
	}
	if !p.Match(must("https://old.reddit.com/search?q=x"), pluginapi.CapMinPollInterval) {
		t.Error("search URL should claim the poll floor")
	}
	if p.Match(must("https://old.reddit.com/r/golang"), pluginapi.CapMinPollInterval) {
		t.Error("a subreddit feed is not this plugin's feed")
	}
	if got := p.Meta().MinPollInterval; got != time.Hour {
		t.Errorf("MinPollInterval = %v, want 1h", got)
	}
	if p.Match(must("https://old.reddit.com/r/golang"), pluginapi.CapFetch) {
		t.Error("a subreddit feed is not a search URL")
	}
	if p.Match(must("https://example.com/search"), pluginapi.CapFetch) {
		t.Error("non-reddit host should not match")
	}
}

func TestFetch(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc", "include_nsfw": "1"})
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(searchFixture)}, nil
	}}
	res, err := p.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://old.reddit.com/r/golang/search?q=generics",
	}, h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d", len(res.Items))
	}
	if res.Items[0].ImageURL != "https://i.redd.it/abc123.jpeg" {
		t.Errorf("ImageURL = %q", res.Items[0].ImageURL)
	}
	if res.NextPageURL == "" {
		t.Error("expected a next page url")
	}
	if !strings.Contains(h.lastReq.URL, "include_over_18=on") {
		t.Errorf("NSFW toggle not applied: %q", h.lastReq.URL)
	}
	if h.lastReq.Headers["Cookie"] != "reddit_session=abc" {
		t.Errorf("Cookie = %q", h.lastReq.Headers["Cookie"])
	}
	if !strings.Contains(h.lastReq.URL, "q=subreddit%3Agolang+generics") {
		t.Errorf("fetch URL not canonical: %q", h.lastReq.URL)
	}
}

func TestFetchRateLimited(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc"})
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 429, RateLimited: true, RetryAfter: time.Minute}, nil
	}}
	_, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://old.reddit.com/search?q=x"}, h)
	var rl *pluginapi.RateLimit
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want *RateLimit", err)
	}
}

func TestFetchUnconfigured(t *testing.T) {
	p := &Plugin{}
	_, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://old.reddit.com/search?q=x"}, &fakeHost{})
	if err == nil {
		t.Fatal("expected an error when unconfigured")
	}
}

func TestLogin(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		http.SetCookie(w, &http.Cookie{Name: "reddit_session", Value: "sess123"})
		http.SetCookie(w, &http.Cookie{Name: "loid", Value: "abc"})
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"json":{"errors":[],"data":{}}}`))
	}))
	defer srv.Close()

	old := loginURL
	loginURL = srv.URL
	defer func() { loginURL = old }()

	p := &Plugin{}
	cookie, err := p.login(context.Background(), Config{Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(cookie, "reddit_session=sess123") {
		t.Fatalf("cookie = %q", cookie)
	}
	if gotForm.Get("user") != "user" || gotForm.Get("passwd") != "pass" {
		t.Fatalf("login form = %v", gotForm)
	}
}

func TestLoginWithoutCookieFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"json":{"errors":[["WRONG_PASSWORD",{}]]}}`))
	}))
	defer srv.Close()
	old := loginURL
	loginURL = srv.URL
	defer func() { loginURL = old }()

	p := &Plugin{}
	if _, err := p.login(context.Background(), Config{Username: "u", Password: "p"}); err == nil {
		t.Fatal("expected login to fail without a session cookie")
	}
}

// A subreddit name split across highlight spans must still resolve to the whole
// name (read from the anchor href, not the space-joined text).
func TestSubredditFromBlockSplitSpans(t *testing.T) {
	block := []byte(`<a href="https://www.reddit.com/r/keyholdercaptions/" class="search-subreddit-link may-blank"><span>r/<span style="background: x" class="highlightwords">keyholder</span>captions</span></a>`)
	if got := subredditFromBlock(block); got != "keyholdercaptions" {
		t.Fatalf("subreddit = %q, want keyholdercaptions", got)
	}
}
