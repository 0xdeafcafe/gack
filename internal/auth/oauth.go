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
	location := "NEEDS ATTENTION"
	panelTitle := "CONNECTION INTERRUPTED"
	badge := "[ ERROR ]"
	glyph := "!"
	prompt := "$ gack login"
	closingNote := "Return to gack to try again. You can close this tab."
	if success {
		className = "is-success"
		location = "COMPLETE"
		panelTitle = "CONNECTION ESTABLISHED"
		badge = "[ OK ]"
		glyph = "✓"
		prompt = "$ gack"
		closingNote = "You can close this tab. No credentials are shown here."
	}

	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<title>` + html.EscapeString(title) + ` · gack</title>
<style>
:root{color-scheme:dark;--canvas:#100e15;--surface:#191620;--surface-2:#211d28;--line:#51485d;--muted:#a69cac;--text:#f8f3fa;--purple:#734181;--accent:#c792ea;--shadow:#08070a}
*{box-sizing:border-box}
html,body{min-height:100%}
body{margin:0;background:var(--canvas);color:var(--text);font:500 15px/1.55 ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono",monospace;display:grid;place-items:center;padding:24px}
body.is-error{--accent:#ff7b72;--purple:#70383f}
body::before{content:"";position:fixed;inset:0;pointer-events:none;background:linear-gradient(rgba(255,255,255,.018) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.014) 1px,transparent 1px);background-size:24px 24px;mask-image:linear-gradient(to bottom,black,transparent 85%)}
.terminal{position:relative;width:min(880px,100%);overflow:hidden;border:1px solid var(--line);border-radius:14px;background:var(--surface);box-shadow:10px 12px 0 var(--shadow),0 28px 90px rgba(0,0,0,.48);animation:arrive .35s ease-out both}
.chrome{height:42px;padding:0 16px;display:flex;align-items:center;gap:8px;background:#151219;border-bottom:1px solid #332d39;color:var(--muted);font-size:12px;letter-spacing:.03em}
.dot{width:10px;height:10px;border:1px solid #665d6d;border-radius:50%;background:#2a2530}.dot:first-child{background:#f06c75;border-color:#f06c75}.dot:nth-child(2){background:#e7ba58;border-color:#e7ba58}.dot:nth-child(3){background:#70c06c;border-color:#70c06c}
.chrome-title{margin:auto}.secure{color:#8d8394}
.appbar{min-height:36px;padding:7px 14px;display:flex;align-items:center;justify-content:space-between;gap:16px;background:var(--purple);color:#fff;font-weight:800;letter-spacing:.05em}
.appbar small{font:700 11px/1 ui-monospace,SFMono-Regular,Menlo,monospace;opacity:.78;letter-spacing:.08em}
.breadcrumb{padding:7px 14px;border-bottom:1px solid #3c3543;background:var(--surface-2);color:var(--muted);font-size:12px;letter-spacing:.04em;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.breadcrumb b{padding:0 7px;color:#746a7c}.breadcrumb strong{color:var(--accent)}
.stage{min-height:410px;padding:clamp(28px,7vw,64px);display:grid;place-items:center}
.panel{width:min(680px,100%);border:1px solid color-mix(in srgb,var(--accent) 52%,var(--line));background:#151219;box-shadow:6px 6px 0 color-mix(in srgb,var(--accent) 14%,#09070b)}
.panel-head{padding:9px 12px;display:flex;justify-content:space-between;gap:16px;border-bottom:1px solid var(--line);background:var(--surface-2);font-size:12px;font-weight:800;letter-spacing:.06em}.badge{color:var(--accent);white-space:nowrap}
.content{padding:clamp(25px,5vw,44px);display:grid;grid-template-columns:56px 1fr;gap:22px;align-items:start}
.glyph{width:48px;height:48px;display:grid;place-items:center;border:1px solid var(--accent);color:var(--accent);font-size:26px;font-weight:900;box-shadow:3px 3px 0 color-mix(in srgb,var(--accent) 22%,transparent)}
h1{margin:-5px 0 10px;color:var(--accent);font-size:clamp(24px,4vw,38px);line-height:1.15;letter-spacing:-.035em}
p{margin:0;color:#d9d1dc;overflow-wrap:anywhere}.prompt{margin-top:24px;padding:10px 12px;border-left:2px solid var(--accent);background:#201b25;color:#f3edf5}.cursor{display:inline-block;width:8px;height:1.1em;margin-left:5px;vertical-align:-.18em;background:var(--accent);animation:blink 1s steps(1,end) infinite}
.foot{padding:10px 14px;border-top:1px solid #37303d;color:var(--muted);font-size:11px;letter-spacing:.025em}.foot span{color:var(--accent)}
@keyframes blink{50%{opacity:0}}@keyframes arrive{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
@media(max-width:560px){body{padding:12px}.terminal{border-radius:9px}.secure{display:none}.stage{min-height:360px;padding:24px 16px}.content{grid-template-columns:1fr;gap:18px}.glyph{width:42px;height:42px}.panel-head{font-size:10px}.foot{font-size:10px}}
@media(prefers-reduced-motion:reduce){.terminal,.cursor{animation:none}}
</style>
</head>
<body class="` + className + `">
<main class="terminal" aria-labelledby="result-title">
  <div class="chrome" aria-hidden="true"><i class="dot"></i><i class="dot"></i><i class="dot"></i><span class="chrome-title">gack — localhost</span><span class="secure">LOCAL CALLBACK</span></div>
  <header class="appbar"><span>GACK / SLACK AUTH</span><small>SECURE HANDOFF</small></header>
  <nav class="breadcrumb" aria-label="Sign-in progress">YOU ARE HERE <b>›</b> SIGN IN <b>›</b> <strong>` + location + `</strong></nav>
  <section class="stage">
    <div class="panel" role="status" aria-live="polite">
      <div class="panel-head"><span>` + panelTitle + `</span><span class="badge">` + badge + `</span></div>
      <div class="content">
        <div class="glyph" aria-hidden="true">` + glyph + `</div>
        <div>
          <h1 id="result-title">` + html.EscapeString(title) + `</h1>
          <p>` + html.EscapeString(message) + `</p>
          <div class="prompt" aria-hidden="true">` + prompt + `<i class="cursor"></i></div>
        </div>
      </div>
      <div class="foot"><span>●</span> ` + closingNote + `</div>
    </div>
  </section>
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
