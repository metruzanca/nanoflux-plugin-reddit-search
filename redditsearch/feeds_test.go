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

const (
	bannedListing = `<html><head><title>programmerhumour: banned</title></head><body><p>This community has been banned</p></body></html>`
	deletedUser   = `<html><head><title>someuser: deleted</title></head><body></body></html>`
	suspendedUser = `<html><head><title>someuser: suspended</title></head><body></body></html>`
	notFoundPage  = `<html><head><title>reddit.com: page not found</title></head><body><h1>page not found</h1></body></html>`
)

func TestListingTarget(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		kind   string
		name   string
		scrape string
		title  string
		home   string
	}{
		{"https://www.reddit.com/r/golang/.rss", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://www.reddit.com/r/golang.rss", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://old.reddit.com/r/golang/new/", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://old.reddit.com/r/golang/new/.rss", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://www.reddit.com/r/golang/top/.rss", true, "r", "golang", "https://old.reddit.com/r/golang/new/", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://old.reddit.com/r/golang/new/?count=25&after=t3_x", true, "r", "golang", "https://old.reddit.com/r/golang/new/?after=t3_x&count=25", "r/golang", "https://www.reddit.com/r/golang"},
		{"https://www.reddit.com/user/sam.rss", true, "u", "sam", "https://old.reddit.com/user/sam/submitted/?sort=new", "u/sam", "https://www.reddit.com/user/sam"},
		{"https://www.reddit.com/u/sam/submitted.rss", true, "u", "sam", "https://old.reddit.com/user/sam/submitted/?sort=new", "u/sam", "https://www.reddit.com/user/sam"},
		{"https://old.reddit.com/user/sam/submitted/?sort=new", true, "u", "sam", "https://old.reddit.com/user/sam/submitted/?sort=new", "u/sam", "https://www.reddit.com/user/sam"},
		{"https://www.reddit.com/r/golang/comments/1abc/x/", false, "", "", "", "", ""},
		{"https://www.reddit.com/r/golang/search?q=x", false, "", "", "", "", ""},
		{"https://example.com/r/golang/.rss", false, "", "", "", "", ""},
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
		if got.kind != tc.kind || got.name != tc.name || got.scrapeURL != tc.scrape || got.title != tc.title || got.homeURL != tc.home {
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

func TestMatchFeedFetch(t *testing.T) {
	p := &Plugin{}
	feed := mustURL(t, "https://www.reddit.com/r/golang/.rss")
	if !p.Match(feed, pluginapi.CapFetch) {
		t.Error("feed fetch should always match")
	}
	if !p.Match(feed, pluginapi.CapDiscover) {
		t.Error("feed discovery should be available in this plugin")
	}
	if p.Match(mustURL(t, "https://www.reddit.com/r/golang/comments/1abc/x/"), pluginapi.CapFetch) {
		t.Error("post permalink should not match feed fetch")
	}
	if !p.Match(mustURL(t, "https://old.reddit.com/search?q=x"), pluginapi.CapFetch) {
		t.Error("search should match fetch")
	}
}

func TestFetchListing(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc", "include_nsfw": "1"})
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

func TestFetchListingUnconfigured(t *testing.T) {
	p := &Plugin{}
	calls := 0
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		calls++
		return pluginapi.HTTPResponse{Status: 200}, nil
	}}
	_, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/golang/.rss"}, h)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v, want a configuration error", err)
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want 0", calls)
	}
}

func TestFetchListingRateLimited(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc"})
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
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestFetchListingLoginWallErrors(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "expired"})
	calls := 0
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		calls++
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<html><div id="login-form">login</div></html>`)}, nil
	}}
	_, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/golang/.rss"}, h)
	if err == nil || !strings.Contains(err.Error(), "login or block page") {
		t.Fatalf("err = %v, want a login-wall error", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if p.loadConfig().Session == "" {
		t.Error("config session should remain; only the cached login session is cleared")
	}
}

func TestFetchListingBlockPageErrors(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc"})
	calls := 0
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		calls++
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<html><head><title>Blocked</title></head><body>nope</body></html>`)}, nil
	}}
	_, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/golang/.rss"}, h)
	if err == nil || !strings.Contains(err.Error(), "login or block page") {
		t.Fatalf("err = %v, want a block-page error", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestPermanentFailure(t *testing.T) {
	cases := []struct {
		name   string
		target feedTarget
		status int
		body   string
		want   string
		ok     bool
	}{
		{"banned community", feedTarget{kind: "r", name: "programmerhumour"}, 404, bannedListing, "reddit community was banned", true},
		{"deleted community", feedTarget{kind: "r", name: "gone"}, 404, `<html><head><title>gone: deleted</title></head></html>`, "reddit community was deleted", true},
		{"deleted profile", feedTarget{kind: "u", name: "gone"}, 404, deletedUser, "reddit profile was deleted", true},
		{"suspended profile", feedTarget{kind: "u", name: "gone"}, 404, suspendedUser, "reddit profile was suspended", true},
		{"generic 404", feedTarget{kind: "u", name: "typo"}, 404, notFoundPage, "", false},
		{"generic 404 community", feedTarget{kind: "r", name: "typo"}, 404, notFoundPage, "", false},
		{"not eligible status", feedTarget{kind: "r", name: "x"}, 500, bannedListing, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := permanentFailure(tc.target, tc.status, []byte(tc.body))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got.Reason != tc.want || got.Code != tc.status {
				t.Fatalf("permanent = %+v, want reason %q code %d", got, tc.want, tc.status)
			}
		})
	}
}

func TestFetchBannedCommunity(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc"})
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 404, Body: []byte(bannedListing)}, nil
	}}
	_, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/r/programmerhumour/.rss"}, h)
	var pe *pluginapi.PermanentError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *PermanentError", err)
	}
	if pe.Reason != "reddit community was banned" || pe.Code != 404 {
		t.Fatalf("permanent = %+v", pe)
	}
}

func TestFetchGenericNotFoundIsTransient(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc"})
	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 404, Body: []byte(notFoundPage)}, nil
	}}
	_, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.reddit.com/user/typo/submitted.rss"}, h)
	var se *pluginapi.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	var pe *pluginapi.PermanentError
	if errors.As(err, &pe) {
		t.Fatalf("generic 404 should not be permanent: %+v", pe)
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
