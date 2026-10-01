# reddit search

Watch a reddit search as a feed. Reddit's search pages have no RSS/Atom feed and
require a logged-in session, so this plugin reads `old.reddit.com`'s search HTML
with your account and turns the results into feed items.

This plugin is separate from nanoflux's built-in reddit support, which reads
reddit's public `.rss` feeds. Watching an arbitrary search query is a login-gated
scrape, so it lives in its own opt-in plugin.

## Add a feed

Paste a search URL into the add-feed box:

```
https://old.reddit.com/search?q=author%3ALeftArmOfExodia&sort=new&t=all
https://old.reddit.com/r/golang/search?q=generics
```

The plugin normalizes it to `old.reddit.com`, forces `sort=new` and
`t=all`, and folds an `/r/<sub>/search` path into a `subreddit:<sub>` term. So
`https://old.reddit.com/r/golang/search?q=generics` becomes a global search for
`subreddit:golang generics`.

## Search parameters

Any of reddit's advanced search parameters work in `q`:

| parameter | finds |
| --- | --- |
| `subreddit:subreddit` | submissions in "subreddit" |
| `author:username` | submissions by "username" |
| `site:example.com` | submissions from "example.com" |
| `url:text` | "text" in the url |
| `selftext:text` | "text" in self post contents |
| `self:yes` (or `self:no`) | include (or exclude) self posts |
| `nsfw:yes` (or `nsfw:no`) | include (or exclude) results marked NSFW |

For example, `subreddit:aww site:imgur.com dog`.

## Session

Reddit's search is login-gated. Two ways to sign in, set on the admin plugins
page:

- **`reddit_session` cookie (recommended).** Log in to `old.reddit.com` in a
  browser, open DevTools → Application → Cookies, and paste the `reddit_session`
  value. This is what the built-in X plugin does, and it is the reliable path.
- **Username and password.** The plugin attempts reddit's form login. Reddit now
  protects login with a captcha and 2FA, so this is best-effort; if the account
  needs either, use the cookie instead.

### Include NSFW results

The "include NSFW results" toggle adds reddit's `include_over_18=on`. A feed URL
that already sets `include_over_18=on` always keeps it on.

## Notes

- A result's image is taken from the post's external link (e.g. `i.redd.it`),
  not reddit's signed preview thumbnail, so it does not expire; image posts show
  their image on the card and in the modal.
- A result's post id (`t3_<id>`) is its identity, so a post found by search and
  also present in a subscribed subreddit `.rss` feed is stored once with shared
  read/favorite/list state, and renders reddit's usual `r/sub by u/user` line.
- Results are ordered newest-first, so the feed gains new matching posts as they
  appear. Avoid very broad queries that match a huge volume of posts; use
  "load older" to page further back.
