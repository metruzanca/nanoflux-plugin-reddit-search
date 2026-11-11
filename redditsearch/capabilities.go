package redditsearch

import (
	"context"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// SharedKeys gives every reddit post a cross-feed SharedKey from its GUID, the
// post fullname "t3_<id>" that reddit sets identically in every feed that
// carries the post.
func (*Plugin) SharedKeys(_ context.Context, req pluginapi.SharedKeyRequest, _ pluginapi.Host) ([]pluginapi.ItemSharedKey, error) {
	var out []pluginapi.ItemSharedKey
	for i, it := range req.Items {
		if key := sharedKey(it.GUID); key != "" {
			out = append(out, pluginapi.ItemSharedKey{Index: i, SharedKey: key})
		}
	}
	return out, nil
}

// sharedKey returns the cross-feed identity for a reddit GUID, or "" when the
// GUID is not a post fullname. "t3_" then a base36 id; t1_ (comments) and t2_
// (accounts) are not post identities.
func sharedKey(guid string) string {
	const prefix = "t3_"
	if !strings.HasPrefix(guid, prefix) {
		return ""
	}
	id := guid[len(prefix):]
	if id == "" {
		return ""
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return ""
		}
	}
	return "reddit:" + guid
}

// Categorize tags a link post with the registrable domain of its destination.
// It reads the destination from reddit's "[link]" marker, or from the first
// anchor in a summary that carries a plain link (search results), so the label
// reaches the tag filter without a network lookup on every poll. Reddit's own
// destinations are not tagged.
func (*Plugin) Categorize(_ context.Context, req pluginapi.CategorizeRequest, _ pluginapi.Host) ([]pluginapi.ItemCategories, error) {
	var out []pluginapi.ItemCategories
	for i, it := range req.Items {
		dest, ok := summaryDestination(it.Summary)
		if !ok {
			continue
		}
		if domain := destinationDomain(dest); domain != "" {
			out = append(out, pluginapi.ItemCategories{Index: i, Categories: []string{domain}})
		}
	}
	return out, nil
}

// destinationDomain returns the registrable domain of a link post's external
// destination (i.imgur.com -> imgur.com), or "" when the destination is reddit
// itself or is not an HTTP(S) URL.
func destinationDomain(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		host = domain
	}
	if isRedditHost(host) || isRedditMediaHost(host) {
		return ""
	}
	return host
}

// Decorate builds the "r/cats by u/sam" source line for each reddit item from
// its categories, plus the card kind.
func (*Plugin) Decorate(_ context.Context, req pluginapi.DecorateRequest) ([]pluginapi.Decorated, error) {
	out := make([]pluginapi.Decorated, 0, len(req.Items))
	for i, it := range req.Items {
		kind := itemKind(it)
		d := pluginapi.Decorated{Index: i, Kind: kind, ThumbURL: kindThumb(kind, it.ImageURL)}
		if sub, user := categoryPair(it.Categories); sub != "" && user != "" {
			d.Attribution = []pluginapi.SourcePart{
				{Text: sub, Token: sub, URL: "https://www.reddit.com/" + sub + "/"},
				{Text: "by"},
				{Text: user, Token: user, URL: "https://www.reddit.com/user/" + strings.TrimPrefix(user, "u/") + "/"},
			}
			if dest, ok := summaryDestination(it.Summary); ok {
				d.DedupeKey = linkDedupeKey(dest, user)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// summaryDestination returns a post's content URL from its summary, preferring
// reddit's "[link]" marker and falling back to the first anchor.
func summaryDestination(summary string) (string, bool) {
	if u, ok := extractLinkAnchor(summary); ok {
		return u, true
	}
	return firstSummaryLink(summary)
}

// categoryPair extracts the r/<sub> and u/<user> category tokens from an item's
// categories. Either may be empty.
func categoryPair(categories []string) (sub, user string) {
	for _, c := range categories {
		lc := strings.ToLower(c)
		switch {
		case strings.HasPrefix(lc, "r/"):
			if sub == "" {
				sub = c
			}
		case strings.HasPrefix(lc, "u/"):
			if user == "" {
				user = c
			}
		}
	}
	return sub, user
}

// linkDedupeKey builds the view-time cross-feed dedupe identity for a reddit
// link post: the poster plus the post's external destination, normalized so the
// same URL crossposted by the same user to several subreddits yields one key.
func linkDedupeKey(dest, author string) string {
	if dest == "" || author == "" {
		return ""
	}
	u, err := url.Parse(dest)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			if strings.HasPrefix(strings.ToLower(k), "utm_") {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.ToLower(author) + "|" + u.String()
}

// itemKind classifies a reddit item from its summary destination first, then
// its stored thumbnail. A preview thumbnail alone can belong to a link, a
// gallery or a video, so the destination is the stronger signal.
func itemKind(it pluginapi.Item) pluginapi.ItemKind {
	if dest, ok := summaryDestination(it.Summary); ok {
		if _, ok := galleryID(dest); ok {
			return pluginapi.KindGallery
		}
		if isImageURL(dest) {
			return pluginapi.KindImage
		}
		host := hostOf(dest)
		if host == "v.redd.it" {
			return pluginapi.KindVideo
		}
		if !isRedditHost(host) && !isRedditMediaHost(host) {
			return pluginapi.KindLink
		}
	}
	if isLinkThumb(it.ImageURL) {
		return pluginapi.KindLink
	}
	if isGalleryThumb(it.ImageURL) {
		return pluginapi.KindGallery
	}
	if isImagePost(it.Summary, it.ImageURL, it.Title) {
		return pluginapi.KindImage
	}
	if it.ImageURL != "" && isImageURL(it.ImageURL) {
		return pluginapi.KindImage
	}
	return pluginapi.KindText
}
