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
			fmt.Fprint(writer, callbackPage("That didn’t quite work", result.err.Error(), false))
		} else {
			fmt.Fprint(writer, callbackPage("You’re all set", "Slack and Gack are connected. Your workspace is waiting in the terminal.", true))
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
	status := "LET’S TRY AGAIN"
	statusLabel := "Status: sign-in needs attention"
	role := "alert"
	glyph := "!"
	kicker := "Almost there"
	nextTitle := "Back to your terminal"
	nextDetail := "Run <code>gack login</code> and we’ll have another go."
	if success {
		className = "is-success"
		status = "CONNECTED"
		statusLabel = "Status: connected"
		role = "status"
		glyph = "✓"
		kicker = "Nice, that worked"
		nextTitle = "Head back to your terminal"
		nextDetail = "Gack is finishing the last little bit for you."
	}

	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<meta name="theme-color" content="#f4ede4">
<title>` + html.EscapeString(title) + ` · gack</title>
<style>
:root{color-scheme:light;--cream:#f4ede4;--paper:#fffaf5;--ink:#1d1c1d;--muted:#625c62;--aubergine:#4a154b;--green:#2eb67d;--green-soft:#dff5e9;--blue:#36c5f0;--yellow:#ecb22e;--coral:#e01e5a;--shadow:rgba(74,21,75,.14)}
*{box-sizing:border-box}
html,body{min-height:100%}
body{margin:0;overflow-x:hidden;background:var(--cream);color:var(--ink);font:500 17px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}
body.is-error{--green:#e01e5a;--green-soft:#fde4ec}
.confetti{position:fixed;inset:0;overflow:hidden;pointer-events:none}
.shape{position:absolute;display:block;opacity:.92;filter:saturate(.9)}
.shape.one{width:170px;height:170px;left:-58px;top:-54px;border-radius:42% 58% 65% 35%;background:var(--yellow);transform:rotate(18deg)}
.shape.two{width:118px;height:38px;right:7%;top:10%;border-radius:999px;background:var(--blue);transform:rotate(-16deg)}
.shape.three{width:94px;height:94px;right:-24px;bottom:12%;border-radius:50%;background:var(--coral)}
.shape.four{width:26px;height:92px;left:9%;bottom:8%;border-radius:999px;background:var(--green);transform:rotate(34deg)}
.shape.five{width:22px;height:22px;right:18%;bottom:7%;border-radius:7px;background:var(--aubergine);transform:rotate(20deg)}
.page{position:relative;z-index:1;width:min(860px,100%);min-height:100vh;margin:auto;padding:clamp(28px,5vw,64px) 24px;display:grid;place-items:center}
.card{position:relative;width:100%;overflow:hidden;border:1px solid rgba(74,21,75,.08);border-radius:28px;background:var(--paper);box-shadow:0 26px 70px var(--shadow);animation:arrive .45s cubic-bezier(.2,.8,.2,1) both}
.colour-bar{height:10px;display:grid;grid-template-columns:1.05fr .9fr 1.1fr .95fr}.colour-bar i:nth-child(1){background:var(--coral)}.colour-bar i:nth-child(2){background:var(--yellow)}.colour-bar i:nth-child(3){background:var(--green)}.colour-bar i:nth-child(4){background:var(--blue)}
.masthead{padding:26px clamp(26px,5vw,48px) 0;display:flex;align-items:center;justify-content:space-between;gap:20px}
.brand{display:flex;align-items:center;gap:12px;color:var(--aubergine);font-size:16px;font-weight:850;letter-spacing:-.01em}
.brand-mark{display:grid;grid-template-columns:repeat(2,9px);gap:4px;transform:rotate(-8deg)}.brand-mark i{width:9px;height:9px;border-radius:3px}.brand-mark i:nth-child(1){background:var(--coral)}.brand-mark i:nth-child(2){background:var(--yellow)}.brand-mark i:nth-child(3){background:var(--green)}.brand-mark i:nth-child(4){background:var(--blue)}
.brand small{color:#8b6f89;font:700 12px/1 ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.04em}
.status-badge{display:inline-flex;align-items:center;gap:8px;padding:8px 12px;border-radius:999px;background:var(--green-soft);color:#176b4a;font:800 11px/1 ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.07em;white-space:nowrap}.is-error .status-badge{color:#a51646}
.status-badge::before{content:"";width:8px;height:8px;border-radius:50%;background:var(--green)}
.result{padding:clamp(46px,8vw,76px) clamp(28px,8vw,76px) clamp(38px,6vw,58px);display:grid;grid-template-columns:112px 1fr;gap:clamp(24px,5vw,48px);align-items:start}
.celebration{position:relative;width:96px;height:96px;display:grid;place-items:center;border-radius:30px 30px 30px 10px;background:var(--aubergine);box-shadow:10px 10px 0 rgba(236,178,46,.32);transform:rotate(-3deg)}
.glyph{color:#fff;font-size:48px;font-weight:900;line-height:1;transform:rotate(3deg)}
.celebration::before,.celebration::after{content:"";position:absolute;border-radius:999px}.celebration::before{width:18px;height:18px;right:-24px;top:-16px;background:var(--blue)}.celebration::after{width:12px;height:34px;left:-20px;bottom:-14px;background:var(--coral);transform:rotate(38deg)}
.kicker{margin:2px 0 8px;color:var(--aubergine);font:800 13px/1.2 ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.08em;text-transform:uppercase}
h1{max-width:580px;margin:0;color:var(--aubergine);font-size:clamp(36px,6vw,52px);line-height:1.02;letter-spacing:-.045em;overflow-wrap:anywhere}
.message{max-width:570px;margin:20px 0 0;color:var(--muted);font-size:clamp(17px,2.3vw,20px);overflow-wrap:anywhere}
.next{margin-top:32px;padding:20px 22px;border-radius:16px;background:#f0e7ef;display:flex;gap:16px;align-items:center}.next-arrow{flex:0 0 auto;color:var(--aubergine);font-size:30px;font-weight:900;line-height:1}.next-title{margin:0;color:var(--aubergine);font-size:18px;font-weight:800;line-height:1.25}.next-detail{margin:3px 0 0;color:var(--muted);font-size:14px}
code{padding:.08em .38em;border-radius:5px;background:#e4d6e4;color:var(--aubergine);font:700 .94em/1.2 ui-monospace,SFMono-Regular,Menlo,monospace}
.foot{padding:18px clamp(26px,5vw,48px) 22px;border-top:1px solid #eadfea;color:#7b6e79;font-size:13px}.foot strong{color:var(--green)}
@keyframes arrive{from{opacity:0;transform:translateY(12px) rotate(-.3deg)}to{opacity:1;transform:none}}
@media(max-width:640px){.page{padding:18px}.card{border-radius:22px}.masthead{padding:22px 22px 0;align-items:flex-start}.brand small{display:none}.result{padding:42px 24px 36px;grid-template-columns:1fr;gap:32px}.celebration{width:80px;height:80px;border-radius:25px 25px 25px 9px}.glyph{font-size:40px}.next{align-items:flex-start}.shape.two,.shape.five{display:none}.foot{padding:16px 22px 20px}}
@media(prefers-reduced-motion:reduce){.card{animation:none}}
@media(forced-colors:active){.card,.status-badge,.next,.celebration{border:1px solid CanvasText;box-shadow:none}.colour-bar,.confetti{display:none}.glyph,.status-badge,h1,.kicker,.next-title{color:CanvasText}}
</style>
</head>
<body class="` + className + `">
<div class="confetti" aria-hidden="true"><i class="shape one"></i><i class="shape two"></i><i class="shape three"></i><i class="shape four"></i><i class="shape five"></i></div>
<main class="page">
  <article class="card" aria-labelledby="result-title">
    <div class="colour-bar" aria-hidden="true"><i></i><i></i><i></i><i></i></div>
    <header class="masthead">
      <div class="brand"><span class="brand-mark" aria-hidden="true"><i></i><i></i><i></i><i></i></span><span>gack <small>/ SLACK AUTH</small></span></div>
      <div class="status-badge" aria-label="` + statusLabel + `">` + status + `</div>
    </header>
    <section class="result" role="` + role + `" aria-live="polite" aria-atomic="true">
      <div class="celebration" aria-hidden="true"><span class="glyph">` + glyph + `</span></div>
      <div>
        <p class="kicker">` + kicker + `</p>
        <h1 id="result-title">` + html.EscapeString(title) + `</h1>
        <p class="message">` + html.EscapeString(message) + `</p>
        <div class="next">
          <span class="next-arrow" aria-hidden="true">→</span>
          <div>
            <p class="next-title">` + nextTitle + `</p>
            <p class="next-detail">` + nextDetail + `</p>
          </div>
        </div>
      </div>
    </section>
    <footer class="foot"><strong>●</strong> You can close this tab — no credentials are shown here.</footer>
  </article>
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
