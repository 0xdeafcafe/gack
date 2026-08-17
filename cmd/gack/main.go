package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/agentcli"
	"github.com/0xdeafcafe/gack/internal/auth"
	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
	"github.com/0xdeafcafe/gack/internal/slack"
	"github.com/0xdeafcafe/gack/internal/ui"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "logout":
			runLogout()
			return
		case "api":
			runAgentAPI(os.Args[2:])
			return
		}
	}

	preferences, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not load preferences:", err)
	}
	if len(os.Args) > 1 && os.Args[1] == "login" {
		runLogin(os.Args[2:], &preferences)
		return
	}

	demoMode := flag.Bool("demo", false, "run with built-in data even when SLACK_TOKEN is set")
	liveMode := flag.Bool("live", false, "require a live Slack token")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), "Usage: gack [--demo|--live]\n       gack login [--client-id ID] [--no-browser]\n       gack logout\n       gack api [--demo] <command> [arguments]\n\n")
		fmt.Fprint(flag.CommandLine.Output(), "Open the terminal client, sign into a Slack workspace, or use the JSON agent API.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("gack " + buildVersion())
		return
	}

	backend, err := newBackend(*demoMode, *liveMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gack:", err)
		os.Exit(2)
	}

	model := ui.New(backend, preferences.ChannelOrder, func(order []string) error {
		preferences.ChannelOrder = order
		return config.Save(preferences)
	})
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "gack:", err)
		os.Exit(1)
	}
}

func runAgentAPI(arguments []string) {
	demoMode := envBool("GACK_DEMO")
	if len(arguments) > 0 && arguments[0] == "--demo" {
		demoMode = true
		arguments = arguments[1:]
	}
	if len(arguments) == 0 || arguments[0] == "help" || arguments[0] == "--help" || arguments[0] == "-h" {
		demoMode = true // Help never needs credentials or a network connection.
	}
	backend, err := newBackend(demoMode, !demoMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "{\"ok\":false,\"command\":\"\",\"error\":{\"code\":\"backend_unavailable\",\"message\":%q}}\n", err.Error())
		os.Exit(agentcli.ExitError)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	status := agentcli.Run(ctx, backend, arguments, os.Stdout, os.Stderr, agentcli.Options{})
	cancel()
	os.Exit(status)
}

func newBackend(demoMode, requireLive bool) (gack.Backend, error) {
	if demoMode {
		return demo.New(), nil
	}
	token := firstNonEmpty(os.Getenv("SLACK_TOKEN"), os.Getenv("SLACK_USER_TOKEN"), os.Getenv("SLACK_BOT_TOKEN"))
	if token == "" {
		store := auth.DefaultStore()
		credential, err := store.Load()
		if err == nil && credential.NeedsRefresh(time.Now()) {
			oauthClient := auth.OAuth{ClientID: credential.ClientID}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			credential, err = oauthClient.Refresh(ctx, credential)
			cancel()
			if err == nil {
				err = store.Save(credential)
			}
		}
		if err == nil {
			token = credential.AccessToken
		} else if !errors.Is(err, auth.ErrNotFound) {
			return nil, fmt.Errorf("load saved Slack login: %w", err)
		}
	}
	if token == "" {
		if requireLive {
			return nil, errors.New("no Slack login; run `gack login --client-id YOUR_SLACK_CLIENT_ID`")
		}
		return demo.New(), nil
	}
	var bridge slack.InteractionBridge
	if endpoint := os.Getenv("GACK_INTERACTION_URL"); endpoint != "" {
		bridge = &slack.HTTPBridge{URL: endpoint, Token: os.Getenv("GACK_INTERACTION_TOKEN")}
	}
	return slack.New(slack.Config{Token: token, Bridge: bridge, MessageLimit: envInt("GACK_MESSAGE_LIMIT", 15)})
}

func runLogin(arguments []string, preferences *config.Preferences) {
	flags := flag.NewFlagSet("gack login", flag.ExitOnError)
	clientID := flags.String("client-id", firstNonEmpty(os.Getenv("GACK_SLACK_CLIENT_ID"), preferences.SlackClientID), "Slack app client ID")
	noBrowser := flags.Bool("no-browser", false, "print the authorization URL without opening it")
	redirectURI := flags.String("redirect-uri", firstNonEmpty(os.Getenv("GACK_SLACK_REDIRECT_URI"), auth.DefaultRedirectURI), "registered localhost OAuth redirect URI")
	flags.Parse(arguments)
	if *clientID == "" {
		fmt.Fprintln(os.Stderr, "gack login: pass --client-id from your Slack app’s Basic Information page")
		fmt.Fprintln(os.Stderr, "The included slack-manifest.example.yaml configures PKCE and the localhost callback.")
		os.Exit(2)
	}
	present := func(target string) error {
		fmt.Println("Open this URL to sign in to Slack:")
		fmt.Println(target)
		if *noBrowser {
			return nil
		}
		return auth.OpenBrowser(target)
	}
	client := auth.OAuth{ClientID: *clientID, RedirectURI: *redirectURI, PresentURL: present}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	credential, err := client.Login(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gack login:", err)
		os.Exit(1)
	}
	if err := auth.DefaultStore().Save(credential); err != nil {
		fmt.Fprintln(os.Stderr, "gack login: could not save credential:", err)
		os.Exit(1)
	}
	preferences.SlackClientID = *clientID
	if err := config.Save(*preferences); err != nil {
		fmt.Fprintln(os.Stderr, "warning: signed in, but could not remember the client ID:", err)
	}
	workspace := firstNonEmpty(credential.TeamName, credential.TeamID, "Slack")
	fmt.Println("Signed in to " + workspace + ". Run `gack` to open it.")
}

func runLogout() {
	if err := auth.DefaultStore().Delete(); err != nil {
		fmt.Fprintln(os.Stderr, "gack logout:", err)
		os.Exit(1)
	}
	fmt.Println("Signed out. Your channel order is still tucked away for next time.")
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envBool(name string) bool {
	value := os.Getenv(name)
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}
