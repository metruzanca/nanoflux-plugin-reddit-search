# nanoflux-plugin-reddit-search

A [nanoflux](https://github.com/metruzanca/nanoflux)↗ external plugin that reads
subreddits, user profiles, and searches as feeds, using a logged-in session to
read `old.reddit.com`'s pages. It also carries reddit attribution, cross-feed
identity, card kinds, and modal media handling, so it replaces the legacy
`reddit` plugin.

## Install

Build the binary into nanoflux's `plugins/` directory (`NF_PLUGINS_DIR`,
`/plugins` in the container):

```bash
cd nanoflux/plugins/reddit-search
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ../nanoflux-plugin-reddit-search .
```

The binary lands in `nanoflux/plugins/`. Restart nanoflux; the log should show
`registered external plugin reddit-search`.

Adjust `GOARCH` if your host is not `amd64` (use `arm64` for ARM).

## Usage

Add a feed with a reddit search URL, a subreddit, or a user profile:

```
https://old.reddit.com/search?q=author%3ALeftArmOfExodia&sort=new&t=all
https://old.reddit.com/r/golang/search?q=generics
https://www.reddit.com/r/golang/.rss
https://www.reddit.com/user/someone/submitted.rss
```

Then set the account on nanoflux's admin plugins page: paste a `reddit_session`
cookie (recommended), or a username and password. See the plugin's docs in the
app for the supported search parameters and feed behavior.

A session is required. There is no `.rss` fetch path.
