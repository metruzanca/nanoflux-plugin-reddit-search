# reddit

Read subreddits, user profiles, and searches as feeds. Reddit's pages have no
RSS/Atom feed and require a logged-in session, so this plugin reads
`old.reddit.com`'s HTML with your account and turns the results into feed items.

The plugin is self-contained: it owns fetching, URL policy, discovery,
cross-feed identity, attribution, card kinds, and modal media handling. It
replaces the legacy reddit plugin.

## Subreddit and user feeds

The plugin reads each feed's `/new` listing:

```
https://www.reddit.com/r/golang/.rss
https://www.reddit.com/user/someone/submitted.rss
```

Existing `.rss` URLs are still accepted and canonicalized to the listing shape,
so a stored subscription keeps its identity and items. A reddit session is
required. There is no `.rss` fetch path.

A deleted or banned community or profile pauses the feed with a reason instead
of retrying it forever.

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

Reddit's search and listing pages are login-gated. Two ways to sign in, set on
the admin plugins page:

- **`reddit_session` cookie (recommended).** Log in to `old.reddit.com` in a
  browser, open DevTools → Application → Cookies, and paste the `reddit_session`
  value. This is what the built-in X plugin does, and it is the reliable path.
- **Username and password.** The plugin attempts reddit's form login. Reddit now
  protects login with a captcha and 2FA, so this is best-effort; if the account
  needs either, use the cookie instead.

### Include NSFW results

The "include NSFW results" toggle adds reddit's `include_over_18=on` and, for
subreddit listings, sends the `over18=1` cookie. A feed URL that already sets
`include_over_18=on` always keeps it on.

## Poll interval

Reddit requests cost the same account and host for every feed. The plugin
declares a **one-hour minimum poll interval**; nanoflux uses the larger of that
and its own configured minimum, so a feed is polled at most hourly even if the
global default is lowered. A rarer feed benefits from a longer interval in the
feed editor.

## Notes

- A result's image is taken from the post's external link (e.g. `i.redd.it`),
  not reddit's signed preview thumbnail, so it does not expire; image posts show
  their image on the card and in the modal.
- A post id (`t3_<id>`) is the item identity, so a post found by search, through
  a subreddit feed, or through its author's feed is stored once with shared
  read/favorite/list state. The listing path sets the same cross-feed key.
- Results are ordered newest-first, so the feed gains new matching posts as they
  appear. Avoid very broad queries that match a huge volume of posts; use
  "load older" to page further back.
- Card kinds and modal media are resolved from the post's destination, so link
  and gallery posts classify correctly without reddit's preview thumbnails.
