package redditsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// accountURL returns the signed-in account for the configured session. A var so
// tests can point it at a mock host.
var accountURL = "https://old.reddit.com/api/me.json"

// sessionHelp describes the session setting and, when known, the account it is
// signed in as. Settings is read live when the admin page renders its plugin
// settings, so the name appears without a restart.
func (p *Plugin) sessionHelp() string {
	const base = "Recommended. From a logged-in old.reddit.com session: DevTools → Application → Cookies → reddit_session. Skips login and is more reliable than a password."
	p.mu.Lock()
	account := p.account
	username := p.cfg.Username
	p.mu.Unlock()
	if account != "" {
		return "Signed in as u/" + account + ". " + base
	}
	if username != "" {
		return "Logging in as u/" + username + ". " + base
	}
	return base
}

// maybeFetchAccount starts one background lookup of the signed-in account when a
// session is configured and the name is not cached. Configure calls it at
// startup and after a settings save, and Settings calls it again so a failed
// lookup retries on the next admin page render without blocking the page.
func (p *Plugin) maybeFetchAccount() {
	p.mu.Lock()
	if p.account != "" || p.cfg.Session == "" || p.accountFetching {
		p.mu.Unlock()
		return
	}
	p.accountFetching = true
	session := p.cfg.Session
	p.mu.Unlock()

	go func() {
		name, err := fetchAccountName(session)
		p.mu.Lock()
		p.accountFetching = false
		if err == nil && name != "" && p.cfg.Session == session {
			p.account = name
		}
		p.mu.Unlock()
	}()
}

// fetchAccountName asks reddit which account the session belongs to.
func fetchAccountName(session string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", "reddit_session="+session)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("account lookup: status %d", resp.StatusCode)
	}
	var doc struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc); err != nil {
		return "", err
	}
	return strings.TrimSpace(doc.Data.Name), nil
}
