package redditsearch

import (
	"strings"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Config carries the reddit account used to read search results. It is set on
// the plugin by the host through Configure (the admin edits it in the app), not
// read from a file or environment.
//
// Reddit's old.reddit login now redirects to a reCAPTCHA-protected page, so a
// username/password login from a headless process is best-effort. A pasted
// reddit_session cookie is the reliable path and takes precedence when set.
type Config struct {
	// Username and Password are the account credentials used to attempt a
	// session login. Ignored when Session is set.
	Username string
	Password string
	// Session is the value of the reddit_session cookie from a logged-in
	// session. When set it is used directly and no login is attempted.
	Session string
	// IncludeNSFW requests NSFW results (reddit's include_over_18=on).
	IncludeNSFW bool
}

// Complete reports whether any usable credential is present.
func (c Config) Complete() bool {
	if strings.TrimSpace(c.Session) != "" {
		return true
	}
	return strings.TrimSpace(c.Username) != "" && strings.TrimSpace(c.Password) != ""
}

// Settings declares the account and options the plugin needs. The secret fields
// are write-only so the app never renders the stored value back.
func (*Plugin) Settings() []pluginapi.SettingField {
	return []pluginapi.SettingField{
		{
			Name:        "username",
			Label:       "reddit username",
			Kind:        "text",
			Placeholder: "your reddit username",
			Help:        "Used to log in. Ignored when a session cookie is set.",
		},
		{
			Name:  "password",
			Label: "reddit password",
			Kind:  "password",
			Help:  "Used to log in. Ignored when a session cookie is set.",
		},
		{
			Name:  "session",
			Label: "reddit_session cookie",
			Kind:  "password",
			Help:  "Recommended. From a logged-in old.reddit.com session: DevTools → Application → Cookies → reddit_session. Skips login and is more reliable than a password.",
		},
		{
			Name:        "include_nsfw",
			Label:       "include NSFW results",
			Kind:        "bool",
			Placeholder: "include NSFW results",
			Help:        "Adds reddit's include_over_18=on. A feed URL that already sets include_over_18 always keeps it on.",
		},
	}
}

// Configure caches the account the host pushed. It drops any cached login
// session, since the credentials may have changed.
func (p *Plugin) Configure(values map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = Config{
		Username:    strings.TrimSpace(values["username"]),
		Password:    strings.TrimSpace(values["password"]),
		Session:     strings.TrimSpace(values["session"]),
		IncludeNSFW: settingBool(values["include_nsfw"]),
	}
	p.session = ""
}

// settingBool reads a bool setting stored as "1"/"0" (older values may be
// "on"/"true"/"yes").
func settingBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}
