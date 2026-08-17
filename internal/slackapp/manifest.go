// Package slackapp owns the portable Slack app definition used by gack's
// browser-based setup flow.
package slackapp

import (
	"net/url"
	"strings"

	"github.com/0xdeafcafe/gack/internal/auth"
)

const createAppURL = "https://api.slack.com/apps"

// Manifest returns the YAML manifest for a user-token-only desktop Slack app.
// The scopes come from the OAuth client so setup and authorization cannot drift.
func Manifest() string {
	var manifest strings.Builder
	manifest.WriteString(`display_information:
  name: gack
  description: A small terminal client for Slack
  background_color: "#3f1d4d"

oauth_config:
  pkce_enabled: true
  redirect_urls:
    - http://localhost:17645/oauth/callback
  scopes:
    user:
`)
	for _, scope := range auth.UserScopes {
		manifest.WriteString("      - ")
		manifest.WriteString(scope)
		manifest.WriteByte('\n')
	}
	manifest.WriteString(`
settings:
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
`)
	return manifest.String()
}

// CreationURL opens Slack's app creator with the manifest already filled in.
func CreationURL() string {
	query := url.Values{
		"new_app":       {"1"},
		"manifest_yaml": {Manifest()},
	}
	return createAppURL + "?" + query.Encode()
}
