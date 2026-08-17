package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPKCEChallengeRFC7636Vector(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := PKCEChallenge(verifier); got != want {
		t.Fatalf("PKCEChallenge() = %q, want %q", got, want)
	}
}

func TestRandomURLToken(t *testing.T) {
	first, err := randomURLToken(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomURLToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two random tokens unexpectedly matched")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("token is not unpadded base64url: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("decoded token is %d bytes, want 32", len(decoded))
	}
}

func TestAuthorizationURL(t *testing.T) {
	oauth := OAuth{
		ClientID:     "123.456",
		RedirectURI:  "http://localhost:17645/oauth/callback",
		AuthorizeURL: "https://slack.example/authorize?existing=value",
	}
	result, err := oauth.authorizationURL("state value", "challenge/value")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	wants := map[string]string{
		"existing":              "value",
		"client_id":             oauth.ClientID,
		"redirect_uri":          oauth.RedirectURI,
		"state":                 "state value",
		"code_challenge":        "challenge/value",
		"code_challenge_method": "S256",
		"user_scope":            strings.Join(UserScopes, ","),
	}
	for key, want := range wants {
		if got := query.Get(key); got != want {
			t.Errorf("query parameter %q = %q, want %q", key, got, want)
		}
	}
}

func TestLoginCompletesPKCECallbackAndExchange(t *testing.T) {
	formReceived := make(chan url.Values, 1)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		formReceived <- request.PostForm
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"ok":true,"team":{"id":"T1","name":"Acme"},"authed_user":{"id":"U1","scope":"chat:write","access_token":"xoxp-new","refresh_token":"xoxe-new","expires_in":3600}}`)
	}))
	t.Cleanup(tokenServer.Close)

	redirectURI := unusedLocalhostRedirect(t)
	authorizationReceived := make(chan url.Values, 1)
	oauth := OAuth{
		ClientID:    "client-id",
		RedirectURI: redirectURI,
		TokenURL:    tokenServer.URL,
		HTTPClient:  tokenServer.Client(),
		PresentURL: func(target string) error {
			parsed, err := url.Parse(target)
			if err != nil {
				return err
			}
			authorizationReceived <- parsed.Query()
			callback, err := url.Parse(redirectURI)
			if err != nil {
				return err
			}
			query := callback.Query()
			query.Set("state", parsed.Query().Get("state"))
			query.Set("code", "temporary-code")
			callback.RawQuery = query.Encode()
			response, err := (&http.Client{Timeout: 2 * time.Second}).Get(callback.String())
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("callback returned %s", response.Status)
			}
			if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
				return fmt.Errorf("callback response is missing security headers")
			}
			return nil
		},
	}

	started := time.Now()
	credential, err := oauth.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "xoxp-new" || credential.RefreshToken != "xoxe-new" {
		t.Fatalf("unexpected credential tokens: %#v", credential)
	}
	if credential.ClientID != "client-id" || credential.TeamID != "T1" || credential.TeamName != "Acme" || credential.UserID != "U1" || credential.Scope != "chat:write" {
		t.Fatalf("unexpected credential metadata: %#v", credential)
	}
	if credential.ExpiresAt.Before(started.Add(59*time.Minute)) || credential.ExpiresAt.After(time.Now().Add(61*time.Minute)) {
		t.Fatalf("unexpected expiry: %s", credential.ExpiresAt)
	}

	authorization := <-authorizationReceived
	if authorization.Get("client_id") != "client-id" || authorization.Get("state") == "" {
		t.Fatalf("unexpected authorization parameters: %v", authorization)
	}
	if authorization.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q", authorization.Get("code_challenge_method"))
	}
	exchange := <-formReceived
	if exchange.Get("client_id") != "client-id" || exchange.Get("code") != "temporary-code" || exchange.Get("redirect_uri") != redirectURI {
		t.Fatalf("unexpected exchange parameters: %v", exchange)
	}
	verifier := exchange.Get("code_verifier")
	if verifier == "" || PKCEChallenge(verifier) != authorization.Get("code_challenge") {
		t.Fatal("exchange verifier does not match authorization challenge")
	}
}

func TestLoginRejectsInvalidCallback(t *testing.T) {
	tests := []struct {
		name      string
		query     func(state string) url.Values
		wantError string
	}{
		{
			name: "state mismatch",
			query: func(string) url.Values {
				return url.Values{"state": {"wrong"}, "code": {"code"}}
			},
			wantError: "state did not match",
		},
		{
			name: "provider error",
			query: func(state string) url.Values {
				return url.Values{"state": {state}, "error": {"access_denied"}}
			},
			wantError: "authorization: access_denied",
		},
		{
			name: "missing code",
			query: func(state string) url.Values {
				return url.Values{"state": {state}}
			},
			wantError: "returned no code",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redirectURI := unusedLocalhostRedirect(t)
			oauth := OAuth{
				ClientID:    "client-id",
				RedirectURI: redirectURI,
				PresentURL: func(target string) error {
					authorization, err := url.Parse(target)
					if err != nil {
						return err
					}
					callback, _ := url.Parse(redirectURI)
					callback.RawQuery = test.query(authorization.Query().Get("state")).Encode()
					response, err := (&http.Client{Timeout: 2 * time.Second}).Get(callback.String())
					if err != nil {
						return err
					}
					defer response.Body.Close()
					if response.StatusCode != http.StatusBadRequest {
						return fmt.Errorf("callback returned %s", response.Status)
					}
					return nil
				},
			}
			_, err := oauth.Login(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Login() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestLoginValidationAndCancellation(t *testing.T) {
	redirects := []string{
		"https://localhost:17645/oauth/callback",
		"http://127.0.0.1:17645/oauth/callback",
		"http://localhost/oauth/callback",
		"http://localhost:0/oauth/callback",
		"http://localhost:70000/oauth/callback",
		"http://localhost:17645",
		"http://user@localhost:17645/oauth/callback",
		"http://localhost:17645/oauth/callback#fragment",
	}
	for _, redirect := range redirects {
		t.Run(redirect, func(t *testing.T) {
			_, err := (OAuth{ClientID: "client", RedirectURI: redirect}).Login(context.Background())
			if err == nil || !strings.Contains(err.Error(), "desktop OAuth redirect") {
				t.Fatalf("Login() error = %v", err)
			}
		})
	}

	_, err := (OAuth{RedirectURI: unusedLocalhostRedirect(t)}).Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "client ID is required") {
		t.Fatalf("missing client ID error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (OAuth{
		ClientID:    "client",
		RedirectURI: unusedLocalhostRedirect(t),
		PresentURL:  func(string) error { return nil },
	}).Login(ctx)
	if err != context.Canceled {
		t.Fatalf("canceled Login() error = %v, want context.Canceled", err)
	}
}

func TestLoginIgnoresNonGETCallback(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"ok":true,"authed_user":{"access_token":"token"}}`)
	}))
	defer tokenServer.Close()
	redirectURI := unusedLocalhostRedirect(t)
	oauth := OAuth{
		ClientID:    "client",
		RedirectURI: redirectURI,
		TokenURL:    tokenServer.URL,
		HTTPClient:  tokenServer.Client(),
		PresentURL: func(target string) error {
			authorization, _ := url.Parse(target)
			callback, _ := url.Parse(redirectURI)
			query := callback.Query()
			query.Set("state", authorization.Query().Get("state"))
			query.Set("code", "code")
			callback.RawQuery = query.Encode()

			request, err := http.NewRequest(http.MethodPost, callback.String(), nil)
			if err != nil {
				return err
			}
			response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
			if err != nil {
				return err
			}
			response.Body.Close()
			if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodGet {
				return fmt.Errorf("POST callback returned %s with Allow %q", response.Status, response.Header.Get("Allow"))
			}
			response, err = (&http.Client{Timeout: 2 * time.Second}).Get(callback.String())
			if err != nil {
				return err
			}
			response.Body.Close()
			return nil
		},
	}
	credential, err := oauth.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "token" {
		t.Fatalf("AccessToken = %q", credential.AccessToken)
	}
}

func TestExchangeRejectsBadTokenResponses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{"http status", http.StatusBadGateway, `down`, "HTTP 502"},
		{"invalid JSON", http.StatusOK, `{`, "decode Slack OAuth response"},
		{"Slack error", http.StatusOK, `{"ok":false,"error":"invalid_code"}`, "Slack OAuth: invalid_code"},
		{"missing token", http.StatusOK, `{"ok":true}`, "no user access token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.statusCode)
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()
			oauth := OAuth{ClientID: "client", RedirectURI: "http://localhost:1234/callback", TokenURL: server.URL, HTTPClient: server.Client()}
			_, err := oauth.exchange(context.Background(), "code", "verifier")
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("exchange() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestRefreshRotatesTokensAndPreservesMetadata(t *testing.T) {
	formReceived := make(chan url.Values, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		formReceived <- request.PostForm
		fmt.Fprint(writer, `{"ok":true,"authed_user":{"access_token":"new-access","refresh_token":"new-refresh","expires_in":7200}}`)
	}))
	defer server.Close()

	original := Credential{
		ClientID: "saved-client", AccessToken: "old-access", RefreshToken: "old-refresh",
		TeamID: "T1", TeamName: "Acme", UserID: "U1", Scope: "chat:write,search:read",
	}
	started := time.Now()
	refreshed, err := (OAuth{ClientID: "configured-client", TokenURL: server.URL, HTTPClient: server.Client()}).Refresh(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ClientID != "configured-client" || refreshed.AccessToken != "new-access" || refreshed.RefreshToken != "new-refresh" {
		t.Fatalf("unexpected refreshed tokens: %#v", refreshed)
	}
	if refreshed.TeamID != original.TeamID || refreshed.TeamName != original.TeamName || refreshed.UserID != original.UserID || refreshed.Scope != original.Scope {
		t.Fatalf("refresh lost credential metadata: %#v", refreshed)
	}
	if refreshed.ExpiresAt.Before(started.Add(119*time.Minute)) || refreshed.ExpiresAt.After(time.Now().Add(121*time.Minute)) {
		t.Fatalf("unexpected refresh expiry: %s", refreshed.ExpiresAt)
	}
	form := <-formReceived
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "old-refresh" || form.Get("client_id") != "configured-client" {
		t.Fatalf("unexpected refresh form: %v", form)
	}
}

func TestRefreshValidation(t *testing.T) {
	_, err := (OAuth{}).Refresh(context.Background(), Credential{})
	if err == nil || !strings.Contains(err.Error(), "no refresh token") {
		t.Fatalf("missing refresh token error = %v", err)
	}
	_, err = (OAuth{}).Refresh(context.Background(), Credential{RefreshToken: "refresh"})
	if err == nil || !strings.Contains(err.Error(), "client ID is required") {
		t.Fatalf("missing client ID error = %v", err)
	}

	tests := []struct {
		name      string
		response  string
		wantError string
	}{
		{"missing replacement refresh token", `{"ok":true,"access_token":"access","expires_in":3600}`, "no replacement refresh token"},
		{"missing access token expiry", `{"ok":true,"access_token":"access","refresh_token":"refresh"}`, "no access token expiry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(writer, test.response)
			}))
			defer server.Close()
			_, err := (OAuth{TokenURL: server.URL, HTTPClient: server.Client()}).Refresh(context.Background(), Credential{ClientID: "client", RefreshToken: "old-refresh"})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Refresh() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestCallbackPageEscapesContent(t *testing.T) {
	page := callbackPage(`<script>alert(1)</script>`, `<img src=x onerror=alert(1)>`, true)
	if strings.Contains(page, "<script>") || strings.Contains(page, "<img") {
		t.Fatalf("callback page contains unescaped content: %s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") || !strings.Contains(page, "&lt;img") {
		t.Fatalf("callback page did not preserve escaped content: %s", page)
	}
}

func TestCallbackPageRendersTerminalStates(t *testing.T) {
	success := callbackPage("Authorization received", "Return to gack.", true)
	for _, want := range []string{"GACK / SLACK AUTH", "CONNECTION ESTABLISHED", "[ OK ]", "COMPLETE", "You can close this tab", `role="status"`} {
		if !strings.Contains(success, want) {
			t.Errorf("success page missing %q", want)
		}
	}

	failure := callbackPage("Couldn’t sign in", "state did not match", false)
	for _, want := range []string{"CONNECTION INTERRUPTED", "[ ERROR ]", "NEEDS ATTENTION", "gack login", "state did not match"} {
		if !strings.Contains(failure, want) {
			t.Errorf("failure page missing %q", want)
		}
	}
}

func TestParsePort(t *testing.T) {
	got, err := ParsePort("http://localhost:17645/oauth/callback")
	if err != nil || got != 17645 {
		t.Fatalf("ParsePort() = %d, %v", got, err)
	}
	if _, err := ParsePort("http://localhost/oauth/callback"); err == nil {
		t.Fatal("ParsePort() accepted a URL without a port")
	}
}

func unusedLocalhostRedirect(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("http://localhost:%d/oauth/callback", port)
}
