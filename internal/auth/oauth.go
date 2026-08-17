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
			fmt.Fprint(writer, callbackPage("Couldn’t sign in", result.err.Error()))
		} else {
			fmt.Fprint(writer, callbackPage("Authorization received", "Return to gack to finish signing in."))
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

func callbackPage(title, message string) string {
	return `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>` + html.EscapeString(title) + ` · gack</title><style>body{margin:0;background:#17141f;color:#f7f3f8;font:18px system-ui;display:grid;place-items:center;min-height:100vh}.card{max-width:32rem;padding:3rem;border:1px solid #6e3a78;border-radius:1rem;background:#26232b}h1{color:#c792ea}</style><main class="card"><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(message) + `</p></main>`
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
