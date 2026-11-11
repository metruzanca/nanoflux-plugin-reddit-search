package redditsearch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchAccountName(t *testing.T) {
	var gotCookie, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"kind":"t2","data":{"name":"ExampleUser"}}`))
	}))
	defer srv.Close()

	old := accountURL
	accountURL = srv.URL
	t.Cleanup(func() { accountURL = old })

	name, err := fetchAccountName("abc")
	if err != nil {
		t.Fatalf("fetchAccountName: %v", err)
	}
	if name != "ExampleUser" {
		t.Fatalf("name = %q", name)
	}
	if gotCookie != "reddit_session=abc" {
		t.Errorf("Cookie = %q", gotCookie)
	}
	if gotUA != userAgent {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

func TestFetchAccountNameError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	old := accountURL
	accountURL = srv.URL
	t.Cleanup(func() { accountURL = old })

	if _, err := fetchAccountName("expired"); err == nil {
		t.Fatal("expected an error for a rejected session")
	}
}

func TestSessionHelpShowsAccount(t *testing.T) {
	p := &Plugin{account: "ExampleUser"}
	help := p.sessionHelp()
	if !strings.Contains(help, "Signed in as u/ExampleUser") {
		t.Fatalf("help = %q", help)
	}
}

func TestSessionHelpShowsUsername(t *testing.T) {
	p := &Plugin{cfg: Config{Username: "ExampleUser"}}
	help := p.sessionHelp()
	if !strings.Contains(help, "Logging in as u/ExampleUser") {
		t.Fatalf("help = %q", help)
	}
}

func TestSettingsShowsSignedInAccount(t *testing.T) {
	p := &Plugin{account: "ExampleUser"}
	var sessionHelp string
	for _, f := range p.Settings() {
		if f.Name == "session" {
			sessionHelp = f.Help
		}
	}
	if !strings.Contains(sessionHelp, "Signed in as u/ExampleUser") {
		t.Fatalf("session help = %q", sessionHelp)
	}
}
