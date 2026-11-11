package redditsearch

import (
	"context"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/pluginapi"
	"nanoflux-plugin-reddit-search/oembed"
)

// EmbedBaseURL hosts reddit's embed pages, which render a post's full media
// (including every gallery image) and are served without a login. A var so
// tests can point it at a mock host.
var EmbedBaseURL = "https://embed.reddit.com"

// Render resolves a reddit post's view-time media: an embeddable player for a
// link post, the full-res images of a gallery, or an image post's original.
func (p *Plugin) Render(ctx context.Context, req pluginapi.RenderRequest, h pluginapi.Host) (pluginapi.Media, error) {
	dest, ok := destination(req)
	if !ok {
		return pluginapi.Media{}, nil
	}
	if gid, ok := galleryID(dest); ok {
		sub, _, _ := postID(req.Link)
		return pluginapi.Media{Gallery: p.galleryImages(ctx, sub, gid, h)}, nil
	}
	if isImageURL(dest) {
		if full := fullImage(dest); full != "" {
			dest = full
		}
		return pluginapi.Media{Gallery: []string{dest}}, nil
	}
	if e, err := oembed.DiscoverWith(ctx, hGetter(h), dest); err == nil && e.Src != "" {
		return pluginapi.Media{SourceURL: dest, EmbedSrc: e.Src}, nil
	}
	return pluginapi.Media{SourceURL: dest}, nil
}

// destination finds a post's real content URL. A reddit "[link]" anchor is
// preferred; search results and other feed shapes use the summary's first
// anchor instead, and an image post falls back to its stored image. It performs
// no network I/O, so it also works after reddit's .rss endpoints are gone.
func destination(req pluginapi.RenderRequest) (string, bool) {
	if u, ok := extractLinkAnchor(req.Summary); ok {
		return u, true
	}
	if u, ok := firstSummaryLink(req.Summary); ok {
		return u, true
	}
	if req.ImageURL != "" && isImageURL(req.ImageURL) {
		return req.ImageURL, true
	}
	return "", false
}

// hGetter adapts a pluginapi.Host to an oembed.Getter so oEmbed requests are
// mediated by the host (User-Agent, timeouts, per-host pacing).
func hGetter(h pluginapi.Host) oembed.Getter {
	return func(ctx context.Context, rawurl string) (int, []byte, error) {
		resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: rawurl})
		if err != nil {
			return 0, nil, err
		}
		return resp.Status, resp.Body, nil
	}
}

// galleryImages returns the full-res image URLs of a gallery post, scraped from
// its embed page. Results are cached per post id.
func (p *Plugin) galleryImages(ctx context.Context, sub, id string, h pluginapi.Host) []string {
	if id == "" {
		return nil
	}
	if imgs, ok := p.galleryGet(id); ok {
		return imgs
	}
	var imgs []string
	if sub != "" {
		pageURL := EmbedBaseURL + "/r/" + sub + "/comments/" + id
		if resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: pageURL}); err == nil && resp.Status < 400 {
			imgs = galleryImagesFromHTML(resp.Body)
		}
	}
	p.gallerySet(id, imgs)
	return imgs
}

type galEntry struct {
	imgs []string
	at   time.Time
}

const (
	galleryTTL = 15 * time.Minute
	cacheCap   = 1000
)

func (p *Plugin) galleryGet(id string) ([]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.gallery[id]
	if !ok || time.Since(e.at) > galleryTTL {
		return nil, false
	}
	return e.imgs, true
}

func (p *Plugin) gallerySet(id string, imgs []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gallery == nil {
		p.gallery = make(map[string]galEntry)
	}
	if len(p.gallery) >= cacheCap {
		for k := range p.gallery {
			delete(p.gallery, k)
			break
		}
	}
	p.gallery[id] = galEntry{imgs: imgs, at: time.Now()}
}

// redditMediaHosts are the CDNs reddit serves images from. They hotlink freely
// (a browser loads them directly) but refuse the host's server-side image proxy,
// so reddit's images are loaded directly instead.
var redditMediaHosts = []string{"redd.it", "redditmedia.com", "redditstatic.com"}

// isRedditMediaHost reports whether host is one of reddit's image CDNs.
func isRedditMediaHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range redditMediaHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// galleryID returns the gallery id when dest is reddit's own gallery page
// (https://www.reddit.com/gallery/{id}), which the [link] anchor of a gallery
// post points at.
func galleryID(dest string) (string, bool) {
	u, err := url.Parse(dest)
	if err != nil || !isRedditHost(u.Hostname()) {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "gallery" && parts[1] != "" {
		return parts[1], true
	}
	return "", false
}

// postID extracts the subreddit and post id from a reddit post URL like
// /r/{sub}/comments/{id}/slug or /comments/{id}/slug.
func postID(link string) (sub, id string, ok bool) {
	u, err := url.Parse(link)
	if err != nil || !isRedditHost(u.Hostname()) {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "r" && parts[2] == "comments" && parts[3] != "" {
		return parts[1], parts[3], true
	}
	if len(parts) >= 2 && parts[0] == "comments" && parts[1] != "" {
		return "", parts[1], true
	}
	return "", "", false
}

// isImageURL reports whether raw points directly at an image, by URL path
// extension. The parsed path is used so signed URLs (photo.jpg?e=...&t=...) match.
func isImageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".bmp":
		return true
	}
	return false
}

// fullImage rewrites a preview.redd.it/{id}.{ext} URL to the full-res
// i.redd.it/{id}.{ext} original. Reddit signs preview URLs per-size, but the
// i.redd.it original needs no signature and serves unauthenticated.
func fullImage(previewURL string) string {
	u, err := url.Parse(previewURL)
	if err != nil || u.Hostname() != "preview.redd.it" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 {
		return ""
	}
	id, ext, ok := strings.Cut(parts[0], ".")
	if !ok || id == "" || ext == "" {
		return ""
	}
	if ext == "jpeg" {
		ext = "jpg"
	}
	return "https://i.redd.it/" + id + "." + ext
}

// galleryImgRe matches a preview.redd.it URL and captures the file id and
// extension (e.g. .../whiskers-v0-9z8x7c6v.jpg).
var galleryImgRe = regexp.MustCompile(`preview\.redd\.it/[^"'\s]*?([A-Za-z0-9_]{6,})\.(jpe?g|png|gif)`)

// galleryImagesFromHTML extracts the gallery's image file ids from an embed
// page and rewrites them to full-res i.redd.it URLs.
func galleryImagesFromHTML(body []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range galleryImgRe.FindAllSubmatch(body, -1) {
		u := "https://i.redd.it/" + string(m[1]) + "." + string(m[2])
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
		if len(out) >= 25 {
			break
		}
	}
	return out
}

// extractLinkAnchor finds the anchor whose text is exactly "[link]", reddit's
// marker for a post's destination, and returns its href.
func extractLinkAnchor(htmlContent string) (string, bool) {
	if htmlContent == "" {
		return "", false
	}
	doc, err := xhtml.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", false
	}
	var find func(*xhtml.Node) (string, bool)
	find = func(n *xhtml.Node) (string, bool) {
		if n.Type == xhtml.ElementNode && n.Data == "a" && strings.TrimSpace(nodeText(n)) == "[link]" {
			for _, a := range n.Attr {
				if strings.ToLower(a.Key) != "href" {
					continue
				}
				u, err := url.Parse(a.Val)
				if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
					return u.String(), true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if u, ok := find(c); ok {
				return u, true
			}
		}
		return "", false
	}
	return find(doc)
}

// firstSummaryLink returns the first http(s) anchor href in a summary. It is
// the fallback for feed items whose summary carries a plain destination link
// rather than reddit's "[link]" marker.
func firstSummaryLink(htmlContent string) (string, bool) {
	if htmlContent == "" {
		return "", false
	}
	doc, err := xhtml.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", false
	}
	var find func(*xhtml.Node) (string, bool)
	find = func(n *xhtml.Node) (string, bool) {
		if n.Type == xhtml.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if strings.ToLower(a.Key) != "href" {
					continue
				}
				u, err := url.Parse(a.Val)
				if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
					return u.String(), true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if u, ok := find(c); ok {
				return u, true
			}
		}
		return "", false
	}
	return find(doc)
}
