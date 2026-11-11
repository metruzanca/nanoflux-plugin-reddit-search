package redditsearch

import (
	"context"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/pluginapi"
)

func TestDiscoverFeed(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc", "fetch_feeds": "1"})
	cases := []struct {
		page       string
		feedURL    string
		homeURL    string
		title      string
		authorName string
		ok         bool
	}{
		{"https://www.reddit.com/r/golang/", "https://old.reddit.com/r/golang/new/", "https://www.reddit.com/r/golang", "r/golang", "r/golang", true},
		{"https://old.reddit.com/r/golang/top/?t=week", "https://old.reddit.com/r/golang/new/", "https://www.reddit.com/r/golang", "r/golang", "r/golang", true},
		{"https://www.reddit.com/r/golang/.rss", "https://old.reddit.com/r/golang/new/", "https://www.reddit.com/r/golang", "r/golang", "r/golang", true},
		{"https://www.reddit.com/user/spez", "https://old.reddit.com/user/spez/submitted/?sort=new", "https://www.reddit.com/user/spez", "u/spez", "spez", true},
		{"https://www.reddit.com/u/spez/", "https://old.reddit.com/user/spez/submitted/?sort=new", "https://www.reddit.com/user/spez", "u/spez", "spez", true},
		{"https://old.reddit.com/u/spez.rss", "https://old.reddit.com/user/spez/submitted/?sort=new", "https://www.reddit.com/user/spez", "u/spez", "spez", true},
		{"https://www.reddit.com/r/golang/comments/1abc/x/", "", "", "", "", false},
		{"https://www.reddit.com/user/spez/comments", "", "", "", "", false},
		{"https://www.reddit.com/", "", "", "", "", false},
		{"https://example.com/r/golang/", "", "", "", "", false},
	}
	for _, c := range cases {
		cs, err := p.Discover(context.Background(), c.page, &fakeHost{})
		got := pluginapi.Candidate{}
		if len(cs) == 1 {
			got = cs[0]
		}
		ok := err == nil && len(cs) == 1
		if ok != c.ok {
			t.Errorf("Discover(%q) ok = %v, want %v", c.page, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.FeedURL != c.feedURL || got.HomeURL != c.homeURL || got.Title != c.title || got.AuthorName != c.authorName || !got.Derived {
			t.Errorf("Discover(%q) = %+v", c.page, got)
		}
	}
}

func TestCanonicalizeFeedURL(t *testing.T) {
	p := &Plugin{}
	p.Configure(map[string]string{"session": "abc", "fetch_feeds": "1"})
	cases := []struct{ in, want string }{
		{"https://www.reddit.com/r/golang/.rss", "https://old.reddit.com/r/golang/new/"},
		{"https://www.reddit.com/r/golang.rss", "https://old.reddit.com/r/golang/new/"},
		{"https://m.reddit.com/r/golang/top/.rss", "https://old.reddit.com/r/golang/new/"},
		{"https://www.reddit.com/user/foo/submitted.rss", "https://old.reddit.com/user/foo/submitted/?sort=new"},
		{"https://old.reddit.com/u/foo.rss", "https://old.reddit.com/user/foo/submitted/?sort=new"},
		{"https://old.reddit.com/search?q=x", "https://old.reddit.com/search?q=x&sort=new&t=all"},
		{"https://example.com/feed.xml", "https://example.com/feed.xml"},
		{"not a url", "not a url"},
	}
	for _, c := range cases {
		if got := p.CanonicalizeFeedURL(c.in); got != c.want {
			t.Errorf("CanonicalizeFeedURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeedToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://www.reddit.com/r/cats.rss", "r/cats"},
		{"https://www.reddit.com/r/cats/.rss", "r/cats"},
		{"https://old.reddit.com/r/GoLang.rss", "r/golang"},
		{"https://www.reddit.com/user/sam/submitted.rss", "u/sam"},
		{"https://www.reddit.com/user/Sam.rss", "u/sam"},
		{"https://old.reddit.com/user/sam/submitted/?sort=new", "u/sam"},
		{"https://old.reddit.com/search?q=x", ""},
		{"https://www.reddit.com/r/cats/comments/1abc/", ""},
		{"https://example.com/r/cats.rss", ""},
		{"", ""},
	}
	p := &Plugin{}
	for _, c := range cases {
		if got := p.FeedToken(c.in); got != c.want {
			t.Errorf("FeedToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSharedKeys(t *testing.T) {
	p := &Plugin{}
	req := pluginapi.SharedKeyRequest{Items: []pluginapi.Item{
		{GUID: "t3_1abcde"},
		{GUID: "t1_pb71vsb"},
		{GUID: "t2_someone"},
		{GUID: "t3_9Z"},
		{GUID: "t3_"},
	}}
	got, err := p.SharedKeys(context.Background(), req, &fakeHost{})
	if err != nil {
		t.Fatalf("SharedKeys: %v", err)
	}
	want := map[int]string{0: "reddit:t3_1abcde", 3: "reddit:t3_9Z"}
	if len(got) != len(want) {
		t.Fatalf("shared keys = %+v, want %d", got, len(want))
	}
	for _, e := range got {
		if want[e.Index] != e.SharedKey {
			t.Errorf("item %d shared key = %q, want %q", e.Index, e.SharedKey, want[e.Index])
		}
	}
}

func TestCategorize(t *testing.T) {
	p := &Plugin{}
	req := pluginapi.CategorizeRequest{Items: []pluginapi.Item{
		{Summary: `<a href="https://i.imgur.com/x.jpeg">[link]</a>`},
		{Summary: `<p><a href="https://example.com/article" class="external">https://example.com/article</a></p>`},
		{Summary: `<a href="https://www.reddit.com/r/cats/comments/1a/x/">[link]</a>`},
		{Summary: `<p>self text</p>`},
	}}
	got, err := p.Categorize(context.Background(), req, &fakeHost{})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	want := map[int]string{0: "imgur.com", 1: "example.com"}
	if len(got) != len(want) {
		t.Fatalf("categories = %+v, want %d", got, len(want))
	}
	for _, e := range got {
		if len(e.Categories) != 1 || e.Categories[0] != want[e.Index] {
			t.Errorf("item %d = %v, want %q", e.Index, e.Categories, want[e.Index])
		}
	}
}

func TestDecorate(t *testing.T) {
	p := &Plugin{}
	req := pluginapi.DecorateRequest{Items: []pluginapi.Item{
		{Categories: []string{"r/cats", "u/sam"}, Link: "https://www.reddit.com/r/cats/comments/1a/x/", Summary: `<a href="https://example.com/a">[link]</a>`},
		{Categories: []string{"r/pics", "u/bob"}, Summary: `<a href="https://www.reddit.com/gallery/abc">[link]</a>`},
		{Categories: []string{"r/pics", "u/bob"}, Title: "Pic", ImageURL: "https://i.redd.it/x.jpeg", Summary: `<a href="https://i.redd.it/x.jpeg"><img src="https://i.redd.it/x.jpeg" alt=""></a>`},
		{Categories: []string{"r/self", "u/carol"}, Summary: `<p>body</p><a href="https://www.reddit.com/r/self/comments/1b/">[link]</a>`},
		{Categories: []string{"reblog"}},
	}}
	got, err := p.Decorate(context.Background(), req)
	if err != nil {
		t.Fatalf("Decorate: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("decorations = %d", len(got))
	}
	if got[0].Kind != pluginapi.KindLink || got[0].DedupeKey == "" || len(got[0].Attribution) == 0 {
		t.Errorf("link item = %+v", got[0])
	}
	if got[1].Kind != pluginapi.KindGallery {
		t.Errorf("gallery kind = %v", got[1].Kind)
	}
	if got[2].Kind != pluginapi.KindImage {
		t.Errorf("image kind = %v", got[2].Kind)
	}
	if got[3].Kind != pluginapi.KindText {
		t.Errorf("self kind = %v", got[3].Kind)
	}
	if len(got[4].Attribution) != 0 {
		t.Errorf("non-reddit item should have no attribution: %+v", got[4])
	}
	if !strings.Contains(got[0].Attribution[0].Token, "r/cats") {
		t.Errorf("attribution = %+v", got[0].Attribution)
	}
}

func TestRenderImage(t *testing.T) {
	p := &Plugin{}
	m, err := p.Render(context.Background(), pluginapi.RenderRequest{
		Link:     "https://www.reddit.com/r/pics/comments/1a/pic/",
		ImageURL: "https://i.redd.it/x.jpeg",
		Summary:  `<a href="https://i.redd.it/x.jpeg"><img src="https://i.redd.it/x.jpeg" alt=""></a>`,
	}, &fakeHost{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(m.Gallery) != 1 || m.Gallery[0] != "https://i.redd.it/x.jpeg" {
		t.Fatalf("media = %+v", m)
	}
}

func TestRenderGallery(t *testing.T) {
	old := EmbedBaseURL
	EmbedBaseURL = "https://embed.test"
	t.Cleanup(func() { EmbedBaseURL = old })

	h := &fakeHost{do: func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<img src="https://preview.redd.it/abc123def.jpg?width=1080">`)}, nil
	}}
	p := &Plugin{}
	m, err := p.Render(context.Background(), pluginapi.RenderRequest{
		Link:    "https://www.reddit.com/r/pics/comments/1a/pic/",
		Summary: `<a href="https://www.reddit.com/gallery/abc">[link]</a>`,
	}, h)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(m.Gallery) != 1 || m.Gallery[0] != "https://i.redd.it/abc123def.jpg" {
		t.Fatalf("media = %+v", m)
	}
}

func TestBypassProxy(t *testing.T) {
	p := &Plugin{}
	if !p.Match(mustURL(t, "https://i.redd.it/x.jpeg"), pluginapi.CapBypassProxy) {
		t.Error("reddit media host should bypass the proxy")
	}
	if p.Match(mustURL(t, "https://www.reddit.com/r/cats/"), pluginapi.CapBypassProxy) {
		t.Error("reddit page host should not bypass the proxy")
	}
}
