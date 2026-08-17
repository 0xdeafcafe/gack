package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAuthorizeURL = "https://slack.com/oauth/v2/authorize"
	DefaultTokenURL     = "https://slack.com/api/oauth.v2.access"
	DefaultRedirectURI  = "http://localhost:17645/oauth/callback"
)

var UserScopes = []string{
	"channels:history", "channels:read", "chat:write", "groups:history", "groups:read",
	"im:history", "im:read", "mpim:history", "mpim:read", "reactions:read",
	"reactions:write", "search:read", "users:read",
}

type OAuth struct {
	ClientID     string
	RedirectURI  string
	AuthorizeURL string
	TokenURL     string
	HTTPClient   *http.Client
	PresentURL   func(string) error
}

type callbackResult struct {
	code string
	err  error
}

func (o OAuth) Login(ctx context.Context) (Credential, error) {
	o = o.withDefaults()
	if strings.TrimSpace(o.ClientID) == "" {
		return Credential{}, errors.New("Slack client ID is required")
	}
	redirect, err := url.Parse(o.RedirectURI)
	if err != nil {
		return Credential{}, fmt.Errorf("parse OAuth redirect URI: %w", err)
	}
	port, portErr := strconv.Atoi(redirect.Port())
	if redirect.Scheme != "http" || redirect.Hostname() != "localhost" || redirect.User != nil || portErr != nil || port < 1 || port > 65535 || redirect.Path == "" || redirect.Fragment != "" {
		return Credential{}, fmt.Errorf("desktop OAuth redirect must be http://localhost:<port>/path")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", redirect.Port()))
	if err != nil {
		return Credential{}, fmt.Errorf("listen for Slack OAuth callback on %s: %w", redirect.Host, err)
	}
	defer listener.Close()

	verifier, err := randomURLToken(64)
	if err != nil {
		return Credential{}, err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return Credential{}, err
	}
	challenge := PKCEChallenge(verifier)
	callback := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(redirect.Path, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := request.URL.Query()
		result := callbackResult{}
		switch {
		case query.Get("state") != state:
			result.err = errors.New("Slack OAuth state did not match")
		case query.Get("error") != "":
			result.err = fmt.Errorf("Slack authorization: %s", query.Get("error"))
		case query.Get("code") == "":
			result.err = errors.New("Slack authorization returned no code")
		default:
			result.code = query.Get("code")
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if result.err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(writer, callbackPage("Couldn’t sign in", result.err.Error(), false))
		} else {
			fmt.Fprint(writer, callbackPage("Authorization received", "Slack handed the session back safely. Gack will continue in your terminal.", true))
		}
		select {
		case callback <- result:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	authorizationURL, err := o.authorizationURL(state, challenge)
	if err != nil {
		return Credential{}, err
	}
	if err := o.PresentURL(authorizationURL); err != nil {
		return Credential{}, fmt.Errorf("open Slack authorization: %w", err)
	}
	select {
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	case result := <-callback:
		if result.err != nil {
			return Credential{}, result.err
		}
		return o.exchange(ctx, result.code, verifier)
	}
}

func (o OAuth) Refresh(ctx context.Context, credential Credential) (Credential, error) {
	o = o.withDefaults()
	if credential.RefreshToken == "" {
		return Credential{}, errors.New("saved Slack login has no refresh token")
	}
	clientID := firstNonEmpty(o.ClientID, credential.ClientID)
	if strings.TrimSpace(clientID) == "" {
		return Credential{}, errors.New("Slack client ID is required to refresh the saved login")
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {credential.RefreshToken},
		"client_id":     {clientID},
	}
	response, err := o.requestToken(ctx, values)
	if err != nil {
		return Credential{}, err
	}
	refreshed := credentialFromResponse(response, clientID)
	if refreshed.RefreshToken == "" {
		return Credential{}, errors.New("Slack OAuth refresh returned no replacement refresh token")
	}
	if refreshed.ExpiresAt.IsZero() {
		return Credential{}, errors.New("Slack OAuth refresh returned no access token expiry")
	}
	if refreshed.TeamID == "" {
		refreshed.TeamID = credential.TeamID
	}
	if refreshed.TeamName == "" {
		refreshed.TeamName = credential.TeamName
	}
	if refreshed.UserID == "" {
		refreshed.UserID = credential.UserID
	}
	if refreshed.Scope == "" {
		refreshed.Scope = credential.Scope
	}
	return refreshed, nil
}

func (o OAuth) authorizationURL(state, challenge string) (string, error) {
	parsed, err := url.Parse(o.AuthorizeURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("client_id", o.ClientID)
	query.Set("user_scope", strings.Join(UserScopes, ","))
	query.Set("redirect_uri", o.RedirectURI)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (o OAuth) exchange(ctx context.Context, code, verifier string) (Credential, error) {
	response, err := o.requestToken(ctx, url.Values{
		"client_id":     {o.ClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {o.RedirectURI},
	})
	if err != nil {
		return Credential{}, err
	}
	return credentialFromResponse(response, o.ClientID), nil
}

type oauthResponse struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Team         struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	AuthedUser struct {
		ID           string `json:"id"`
		Scope        string `json:"scope"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	} `json:"authed_user"`
}

func (o OAuth) requestToken(ctx context.Context, values url.Values) (oauthResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := o.HTTPClient.Do(request)
	if err != nil {
		return oauthResponse{}, fmt.Errorf("Slack OAuth token exchange: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return oauthResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return oauthResponse{}, fmt.Errorf("Slack OAuth token exchange: HTTP %s", response.Status)
	}
	var decoded oauthResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return oauthResponse{}, fmt.Errorf("decode Slack OAuth response: %w", err)
	}
	if !decoded.OK {
		return oauthResponse{}, fmt.Errorf("Slack OAuth: %s", decoded.Error)
	}
	if decoded.AccessToken == "" && decoded.AuthedUser.AccessToken == "" {
		return oauthResponse{}, errors.New("Slack OAuth returned no user access token")
	}
	return decoded, nil
}

func credentialFromResponse(response oauthResponse, clientID string) Credential {
	access, refresh, expires, scope := response.AuthedUser.AccessToken, response.AuthedUser.RefreshToken, response.AuthedUser.ExpiresIn, response.AuthedUser.Scope
	if access == "" {
		access, refresh, expires, scope = response.AccessToken, response.RefreshToken, response.ExpiresIn, response.Scope
	}
	credential := Credential{
		ClientID: clientID, AccessToken: access, RefreshToken: refresh, Scope: scope,
		TeamID: response.Team.ID, TeamName: response.Team.Name, UserID: response.AuthedUser.ID,
	}
	if expires > 0 {
		credential.ExpiresAt = time.Now().Add(time.Duration(expires) * time.Second)
	}
	return credential
}

func (o OAuth) withDefaults() OAuth {
	if o.RedirectURI == "" {
		o.RedirectURI = DefaultRedirectURI
	}
	if o.AuthorizeURL == "" {
		o.AuthorizeURL = DefaultAuthorizeURL
	}
	if o.TokenURL == "" {
		o.TokenURL = DefaultTokenURL
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if o.PresentURL == nil {
		o.PresentURL = OpenBrowser
	}
	return o
}

func PKCEChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func randomURLToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func OpenBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func callbackPage(title, message string, success bool) string {
	className := "is-error"
	status := "ACTION REQUIRED"
	statusLabel := "Status: action required"
	role := "alert"
	glyph := "!"
	nextTitle := "Return to your terminal and try again"
	nextDetail := "Run <code>gack login</code> when you’re ready."
	if success {
		className = "is-success"
		status = "COMPLETE"
		statusLabel = "Status: complete"
		role = "status"
		glyph = "✓"
		nextTitle = "Return to your terminal"
		nextDetail = "Gack is finishing sign-in there."
	}

	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<meta name="theme-color" content="#100e15">
<title>` + html.EscapeString(title) + ` · gack</title>
<style>
:root{color-scheme:dark;--canvas:#100e15;--line:#4f4658;--muted:#aaa1b0;--text:#fbf7fc;--accent:#d49af2;--accent-soft:#2a1f31;--glow:rgba(185,105,224,.16)}
*{box-sizing:border-box}
html,body{min-height:100%}
body{margin:0;background:var(--canvas);color:var(--text);font:500 16px/1.6 ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono",monospace}
body.is-error{--accent:#ff8b82;--accent-soft:#321f25;--glow:rgba(255,91,82,.13)}
body::before{content:"";position:fixed;inset:0;pointer-events:none;background:radial-gradient(circle at 50% 38%,var(--glow),transparent 42%),linear-gradient(rgba(255,255,255,.018) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.014) 1px,transparent 1px);background-size:auto,32px 32px,32px 32px;mask-image:linear-gradient(to bottom,black,transparent 92%)}
.page{position:relative;width:min(780px,100%);min-height:100vh;margin:auto;padding:clamp(24px,5vw,56px);display:grid;grid-template-rows:auto 1fr auto}
.masthead{padding-bottom:18px;border-bottom:1px solid var(--line);display:flex;align-items:center;justify-content:space-between;gap:24px}
.brand{font-size:14px;font-weight:900;letter-spacing:.09em}
.status-badge{display:inline-flex;align-items:center;gap:8px;padding:5px 9px;border:1px solid var(--accent);background:var(--accent-soft);color:var(--accent);font-size:11px;font-weight:900;line-height:1;letter-spacing:.08em;white-space:nowrap}
.status-badge::before{content:"";width:7px;height:7px;border-radius:50%;background:currentColor;box-shadow:0 0 12px currentColor}
.result{align-self:center;padding:clamp(64px,12vh,120px) 0 clamp(52px,10vh,96px);animation:arrive .35s ease-out both}
.glyph{margin:0 0 24px;color:var(--accent);font-size:clamp(46px,10vw,72px);font-weight:900;line-height:1}
.eyebrow{margin:0 0 12px;color:var(--accent);font-size:12px;font-weight:900;letter-spacing:.16em}
h1{max-width:680px;margin:0;color:var(--text);font-size:clamp(34px,7vw,64px);line-height:1.03;letter-spacing:-.055em;overflow-wrap:anywhere}
.message{max-width:650px;margin:24px 0 0;color:#d7cfdc;font-size:clamp(16px,2.5vw,19px);overflow-wrap:anywhere}
.next{margin-top:clamp(48px,9vh,80px);padding:20px 0;border-top:1px solid var(--line);border-bottom:1px solid var(--line);display:grid;grid-template-columns:88px 1fr;gap:18px}
.next-label{margin:2px 0 0;color:var(--accent);font-size:11px;font-weight:900;letter-spacing:.14em}
.next-title{margin:0;color:var(--text);font-size:clamp(17px,3vw,22px);font-weight:800;line-height:1.3}.next-detail{margin:5px 0 0;color:var(--muted);font-size:14px}
code{padding:.08em .35em;border:1px solid var(--line);background:var(--accent-soft);color:var(--text);font:inherit}
.foot{padding-top:18px;color:var(--muted);font-size:11px;letter-spacing:.03em}.foot strong{color:var(--accent)}
@keyframes arrive{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
@media(max-width:560px){.page{padding:22px}.masthead{align-items:flex-start;flex-direction:column;gap:12px}.result{padding:56px 0 44px}.next{grid-template-columns:1fr;gap:8px}.next-label{margin:0}}
@media(prefers-reduced-motion:reduce){.result{animation:none}}
@media(forced-colors:active){.status-badge{forced-color-adjust:none;background:Canvas;color:CanvasText}.status-badge::before{box-shadow:none}}
</style>
</head>
<body class="` + className + `">
<main class="page" aria-labelledby="result-title">
  <header class="masthead">
    <div class="brand">GACK / SLACK AUTH</div>
    <div class="status-badge" aria-label="` + statusLabel + `">` + status + `</div>
  </header>
  <section class="result" role="` + role + `" aria-live="polite" aria-atomic="true">
    <div class="glyph" aria-hidden="true">` + glyph + `</div>
    <p class="eyebrow">SLACK HANDOFF</p>
    <h1 id="result-title">` + html.EscapeString(title) + `</h1>
    <p class="message">` + html.EscapeString(message) + `</p>
    <div class="next">
      <p class="next-label">NEXT</p>
      <div>
        <p class="next-title">` + nextTitle + `</p>
        <p class="next-detail">` + nextDetail + `</p>
      </div>
    </div>
  </section>
  <footer class="foot"><strong>●</strong> Safe to close this tab · No credentials are displayed</footer>
</main>
</body>
</html>`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func ParsePort(redirectURI string) (int, error) {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(parsed.Port())
}
