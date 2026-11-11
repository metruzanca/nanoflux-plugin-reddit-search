package redditsearch

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// listingFixture mirrors old.reddit.com's /new listing markup: each post is a
// div.thing with data-* attributes, a title anchor, a tagline time, and an
// optional usertext-body for self posts.
const listingFixture = `<!doctype html><html><body>
<div class=" thing id-t3_abc123 odd link " data-fullname="t3_abc123" data-author="alice" data-subreddit="golang" data-permalink="/r/golang/comments/abc123/some_title/" data-url="https://example.com/article" data-timestamp="1700000000">
  <div class="entry unvoted">
    <div class="top-matter">
      <p class="title"><a class="title may-blank" href="https://example.com/article">Some &amp; Title</a> <span class="domain">(<span>example.com</span>)</span></p>
      <p class="tagline">submitted <time datetime="2023-11-14T22:13:20+00:00">then</time> by <a class="author" href="/user/alice">alice</a> to <a class="subreddit" href="/r/golang/">r/golang</a></p>
    </div>
  </div>
</div>
<div class=" thing id-t3_self1 even link self" data-fullname="t3_self1" data-author="bob" data-subreddit="lounge" data-permalink="/r/lounge/comments/self1/hello/" data-url="https://www.reddit.com/r/lounge/comments/self1/hello/" data-timestamp="1700000001000">
  <div class="entry">
    <p class="title"><a class="title" href="https://www.reddit.com/r/lounge/comments/self1/hello/">Hello</a></p>
    <div class="expando"><div class="usertext-body may-blank-within md-container"><div class="md"><p>Self text</p></div></div></div>
  </div>
</div>
<div class=" thing id-t3_img1 odd link" data-fullname="t3_img1" data-author="carol" data-subreddit="pics" data-permalink="/r/pics/comments/img1/pic/" data-url="https://i.redd.it/img1.jpeg" data-timestamp="1700000002000">
  <div class="entry"><p class="title"><a class="title" href="https://i.redd.it/img1.jpeg">Pic</a></p></div>
</div>
<span class="next-button"><a href="/r/golang/new/?count=25&amp;after=t3_abc123">next</a></span>
</body></html>`

const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>r/golang</title>
  <entry>
    <id>t3_rss1</id>
    <title>RSS Post</title>
    <link href="https://www.reddit.com/r/golang/comments/rss1/post/" />
    <published>2023-11-14T22:13:20+00:00</published>
    <author><name>/u/alice</name></author>
    <category term="golang" label="r/golang"/>
    <content type="html">&lt;p&gt;hello&lt;/p&gt;</content>
  </entry>
</feed>`

func TestListingTarget(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		kind   string
		name   string
		scrape string
		rss    string
		title  string
		home   string
	}{
		{"https://www.reddit.com/r/golang/.rss", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "https://www.reddit.com/r/golang/.rss", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://www.reddit.com/r/golang.rss", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "https://www.reddit.com/r/golang/.rss", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://old.reddit.com/r/golang/new/", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "https://www.reddit.com/r/golang/.rss", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://old.reddit.com/r/golang/new/.rss", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "https://www.reddit.com/r/golang/.rss", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://www.reddit.com/r/golang/top/.rss", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "https://www.reddit.com/r/golang/.rss", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://old.reddit.com/r/golang/new/?count=25&after=t3_x", true, "r", "golang", "https://old.reddit.com/r/golang/new/?after=t3_x&count=25", "https://www.reddit.com/r/golang/.rss", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://www.reddit.com/user/sam.rss", true, "u", "sam", "https://old.reddit.com/user/sam/submitted/?sort=new", "https://www.reddit.com/user/sam/submitted.rss", "u/sam", "https://www.reddit.com/user/sam"},
		{"https://www.reddit.com/u/sam/submitted.rss", true, "u", "sam", "https://old.reddit.com/user/sam/submitted/?sort=new", "https://www.reddit.com/user/sam/submitted.rss", "u/sam", "https://www.reddit.com/user/sam"},
		{"https://old.reddit.com/user/sam/submitted/?sort=new", true, "u", "sam", "https://old.reddit.com/user/sam/submitted/?sort=new", "https://www.reddit.com/user/sam/submitted.rss", "u/sam", "https://www.reddit.com/user/sam"},
		{"https://www.reddit.com/r/golang/comments/1abc/x/", false, "", "", "", "", "", ""},
		{"https://www.reddit.com/r/golang/comments.rss", false, "", "", "", "", "", ""},
		{"https://www.reddit.com/user/sam/comments.rss", false, "", "", "", "", "", ""},
		{"https://www.reddit.com/r/golang/search?q=x", false, "", "", "", "", "", ""},
		{"https://example.com/r/golang/.rss", false, "", "", "", "", "", ""},
	}
	for _, tc := range cases {
		got, ok := listingTarget(tc.in)
		if ok != tc.ok {
			t.Errorf("listingTarget(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.kind != tc.kind || got.name != tc.name || got.scrapeURL != tc.scrape || got.rssURL != tc.rss || got.title != tc.title || got.homeURL != tc.home {
			t.Errorf("listingTarget(%q) = %+v", tc.in, got)
		}
	}
}

func TestParseListing(t *testing.T) {
	items := parseListing([]byte(listingFixture))
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}

	first := items[0]
	if first.GUID != "t3_abc123" || first.Identity != "t3_abc123" || first.SharedKey != "reddit:t3_abc123" {
		t.Errorf("identity = %q/%q/%q", first.GUID, first.Identity, first.SharedKey)
	}
	if first.Title != "Some & Title" {
		t.Errorf("Title = %q", first.Title)
	}
	if first.Link != "https://www.reddit.com/r/golang/comments/abc123/some_title/" {
		t.Errorf("Link = %q", first.Link)
	}
	if !strings.Contains(first.Summary, `href="https://example.com/article"`) || !strings.Contains(first.Summary, "[link]") {
		t.Errorf("link summary = %q", first.Summary)
	}
	if len(first.Categories) != 2 || first.Categories[0] != "r/golang" || first.Categories[1] != "u/alice" {
		t.Errorf("Categories = %v", first.Categories)
	}
	if first.PublishedAt != "2023-11-14 22:13:20" {
		t.Errorf("PublishedAt = %q", first.PublishedAt)
	}

	self := items[1]
	if !strings.Contains(self.Summary, "Self text") || !strings.Contains(self.Summary, "[link]") {
		t.Errorf("self summary = %q", self.Summary)
	}
	if self.Categories[0] != "r/lounge" || self.Categories[1] != "u/bob" {
		t.Errorf("self Categories = %v", self.Categories)
	}

	img := items[2]
	if img.ImageURL != "https://i.redd.it/img1.jpeg" {
		t.Errorf("ImageURL = %q", img.ImageURL)
	}
	if !strings.Contains(img.Summary, `<img src="https://i.redd.it/img1.jpeg"`) {
		t.Errorf("image summary = %q", img.Summary)
	}
}

func TestListingNextPage(t *testing.T) {
	got := listingNextPage([]byte(listingFixture))
	if got != "https://old.reddit.com/r/golang/new/?count=25&after=t3_abc123" {
		t.Fatalf("next = %q", got)
	}
	if listingNextPage([]byte("<html></html>")) != "" {
		t.Fatal("expected empty next page")
	}
}

func TestMatchFeedFetchFlag(t *testing.T) {
	p := &Plugin{}
	feed, _ := url.Parse("https://www.reddit.com/r/golang/.rss")
	if p.Match(feed, pluginapi.CapFetch) {
		t.Error("feed fetch should be off by default")
	}

	p.Configure(map[string]string{"fetch_feeds": "1"})
	if p.Match(feed, pluginapi.CapFetch) {
		t.Error("feed fetch should wait for a session")
	}

	p.Configure(map[string]string{"fetch_feeds": "1", "session": "abc"})
	if !p.Match(feed, pluginapi.CapFetch) {
		t.Error("feed fetch should match when enabled and configured")
	}
	if p.Match(feed, pluginapi.CapDiscover) {
		t.Error("feed discovery should stay with the reddit plugin for now")
	}
	if p.Match(mustURL(t, "https://www.reddit.com/r/golang/comments/1abc/x/"), pluginapi.CapFetch) {
		t.Error("post permalink should not match feed fetch")
	}
	if p.Match(mustURL(t, "https://www.reddit.com/user/sam/comments.rss"), pluginapi.CapFetch) {
		t.Error("comment feed should not match feed fetch")
	}
	if !p.Match(mustURL(t, "https://old.reddit.com/search?q=x"), pluginapi.CapFetch) {
		t.Error("search should still match regardless of the flag")
	}
}

func TestFetchListing(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc", "fetch_feeds": "1", "include_nsfw": "1"})
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(listingFixture)}, nil
	}}
	res, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/golang/.rss"}, h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d", len(res.Items))
	}
	if res.Feed.Title != "r/golang" || res.Feed.HomeURL != "https://www.reddit.com/r/golang" {
		t.Errorf("feed = %+v", res.Feed)
	}
	if !strings.Contains(h.lastReq.URL, "old.reddit.com/r/golang/new/") {
		t.Errorf("scrape URL = %q", h.lastReq.URL)
	}
	if !strings.Contains(h.lastReq.Headers["Cookie"], "reddit_session=abc") || !strings.Contains(h.lastReq.Headers["Cookie"], "over18=1") {
		t.Errorf("Cookie = %q", h.lastReq.Headers["Cookie"])
	}
	if res.NextPageURL == "" {
		t.Error("expected next page URL")
	}
}

func TestFetchListingFallsBackToRSS(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"fetch_feeds": "1"})
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(rssFixture)}, nil
	}}
	res, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/golang/.rss"}, h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].GUID != "t3_rss1" {
		t.Fatalf("items = %+v", res.Items)
	}
	if res.Items[0].SharedKey != "reddit:t3_rss1" {
		t.Errorf("SharedKey = %q", res.Items[0].SharedKey)
	}
	if h.lastReq.URL != "https://www.reddit.com/r/golang/.rss" {
		t.Errorf("fallback URL = %q", h.lastReq.URL)
	}
}

func TestFetchListingRateLimited(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc", "fetch_feeds": "1"})
	calls := 0
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		calls++
		return pluginapi.HTTPResponse{Status: 429, RateLimited: true}, nil
	}}
	_, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/golang/.rss"}, h)
	var rl *pluginapi.RateLimit
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want *RateLimit", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no fallback on rate limit)", calls)
	}
}

func TestFetchListingLoginWallFallsBack(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "expired", "fetch_feeds": "1"})
	calls := 0
	h := &fakeHost{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		calls++
		if strings.Contains(req.URL, "old.reddit.com") {
			return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<html><div id="login-form">login</div></html>`)}, nil
		}
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(rssFixture)}, nil
	}}
	res, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/golang/.rss"}, h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(res.Items) != 1 || res.Items[0].GUID != "t3_rss1" {
		t.Fatalf("items = %+v", res.Items)
	}
	if p.loadConfig().Session == "" {
		t.Error("config session should remain; only the cached login session is cleared")
	}
}

func TestFetchListingBlockPageFallsBack(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc", "fetch_feeds": "1"})
	calls := 0
	h := &fakeHost{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		calls++
		if strings.Contains(req.URL, "old.reddit.com") {
			return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<html><head><title>Blocked</title></head><body>nope</body></html>`)}, nil
		}
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(rssFixture)}, nil
	}}
	res, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/golang/.rss"}, h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(res.Items) != 1 || res.Items[0].GUID != "t3_rss1" {
		t.Fatalf("items = %+v", res.Items)
	}
}

func TestParseRSSMediaThumbnail(t *testing.T) {
	const body = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">
  <entry>
    <id>t3_thumb1</id>
    <title>Thumb</title>
    <link href="https://www.reddit.com/r/golang/comments/thumb1/post/" />
    <published>2023-11-14T22:13:20+00:00</published>
    <author><name>/u/alice</name></author>
    <category term="golang" label="r/golang"/>
    <media:thumbnail url="https://external-preview.redd.it/thumb.jpeg" />
  </entry>
</feed>`
	items, err := parseRSS([]byte(body), feedTarget{kind: "r", name: "golang"})
	if err != nil {
		t.Fatalf("parseRSS: %v", err)
	}
	if len(items) != 1 || items[0].ImageURL != "https://external-preview.redd.it/thumb.jpeg" {
		t.Fatalf("items = %+v", items)
	}
}

func TestIsListingPage(t *testing.T) {
	if !isListingPage([]byte(`<div id="siteTable"></div>`)) {
		t.Error("site table should count as a listing")
	}
	if !isListingPage([]byte(`<div class="thing link"></div>`)) {
		t.Error("thing block should count as a listing")
	}
	if isListingPage([]byte(`<html><div id="login-form"></div></html>`)) {
		t.Error("login form is not a listing")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}
