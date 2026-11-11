# reddit

Read subreddits, user profiles, and searches as feeds. Reddit's pages have no
RSS/Atom feed and require a logged-in session, so this plugin reads
`old.reddit.com`'s HTML with your account and turns the results into feed items.

This plugin now handles subreddit and user posts feeds as a beta feature. The
built-in reddit plugin is legacy: until its attribution and media support is
moved here, keep it installed so items still render their `r/sub by u/user` line,
card kind, and modal media.

## Subreddit and user feeds (beta)

Turn on **fetch subreddit and user feeds (beta)** in the admin plugins page and
set a session below. Both are required: with the flag off or no session, feeds
keep the old `.rss` behavior. The beta path reads each feed's `/new` listing:

```
https://www.reddit.com/r/golang/.rss
https://www.reddit.com/user/someone/submitted.rss
```

The stored feed URL can stay in its old `.rss` shape. The plugin maps it to
`old.reddit.com/r/<sub>/new/` or `old.reddit.com/user/<name>/submitted/`, so the
feed keeps its identity and items across the switch.

While Reddit still serves `.rss`, a feed falls back to it when the session is
missing, expired, or refused. That keeps an existing install working while you
set up the cookie. Once Reddit removes `.rss`, the session is required.

Comment feeds (`/comments.rss`) are not migrated.

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

Search requests cost the same reddit account and host as every other reddit
feed. The plugin declares a **one-hour minimum poll interval**; nanoflux uses the
larger of that and its own configured minimum, so a search feed is polled at most
hourly even if the global default is lowered. A rarer search benefits from a
longer interval in the feed editor.

## Notes

- A result's image is taken from the post's external link (e.g. `i.redd.it`),
  not reddit's signed preview thumbnail, so it does not expire; image posts show
  their image on the card and in the modal.
- A post id (`t3_<id>`) is the item identity, so a post found by search, through
  a subreddit feed, or through its author's feed is stored once with shared
  read/favorite/list state. The beta feed path sets the same cross-feed key
  directly.
- Results are ordered newest-first, so the feed gains new matching posts as they
  appear. Avoid very broad queries that match a huge volume of posts; use
  "load older" to page further back.
- In the beta feed path, link and gallery cards may render as text cards until
  the legacy reddit plugin's media support moves here. Their modal content still
  opens normally.
