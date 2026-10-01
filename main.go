// Command nanoflux-plugin-reddit-search serves nanoflux's reddit search watcher
// over gRPC so nanoflux can load it as an external plugin. Reddit's search
// pages have no feed and require a logged-in session, so this plugin logs in
// with the configured account (or a pasted session cookie) and scrapes
// old.reddit.com search results into a feed.
//
// It is deliberately separate from nanoflux's built-in reddit handling, which
// reads reddit's public .rss feeds and stays within what reddit's terms allow:
// watching arbitrary search queries is a different, login-gated surface, so it
// lives in its own opt-in plugin.
//
// Build it and drop the binary into the host's plugins directory
// (NF_PLUGINS_DIR, ./plugins by default):
//
//	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ../nanoflux-plugin-reddit-search .
//
// See the plugin's docs modal (or redditsearch/readme.md) for the supported
// search parameters and the session setup.
package main

import (
	goplugin "github.com/hashicorp/go-plugin"

	redditsearch "nanoflux-plugin-reddit-search/redditsearch"

	"github.com/metruzanca/nanoflux/pluginapi"
)

func main() {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: pluginapi.Handshake,
		Plugins:         pluginapi.PluginSet(&redditsearch.Plugin{}),
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}
