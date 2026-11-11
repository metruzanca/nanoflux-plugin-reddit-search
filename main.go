// Command nanoflux-plugin-reddit-search serves nanoflux's reddit reader over
// gRPC so nanoflux can load it as an external plugin. Reddit's pages have no
// feed and require a logged-in session, so this plugin logs in with the
// configured account (or a pasted session cookie) and scrapes old.reddit.com
// search results and, as a beta path, subreddit and user /new listings.
//
// The built-in reddit plugin remains installed during the beta for its
// attribution, dedupe and media capabilities. The beta feed path is opt-in
// because it replaces the .rss endpoints Reddit is retiring.
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
