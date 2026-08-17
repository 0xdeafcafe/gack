package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/agentcli"
	"github.com/0xdeafcafe/gack/internal/auth"
	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
	"github.com/0xdeafcafe/gack/internal/loginui"
	"github.com/0xdeafcafe/gack/internal/notify"
	"github.com/0xdeafcafe/gack/internal/selfupdate"
	"github.com/0xdeafcafe/gack/internal/slack"
	"github.com/0xdeafcafe/gack/internal/slackapp"
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
		case "manifest":
			err := runManifest(os.Args[2:], os.Stdout, os.Stderr, auth.OpenBrowser)
			if errors.Is(err, flag.ErrHelp) {
				return
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "gack manifest:", err)
				os.Exit(1)
			}
			return
		case "update":
			if err := runUpdate(os.Args[2:], os.Stdout, os.Stderr, selfupdate.DefaultChecker(), selfupdate.Installer{}); err != nil {
				fmt.Fprintln(os.Stderr, "gack update:", err)
				os.Exit(1)
			}
			return
		case "groups":
			if err := runGroups(os.Args[2:], os.Stdout, os.Stderr); err != nil {
				fmt.Fprintln(os.Stderr, "gack groups:", err)
				os.Exit(1)
			}
			return
		}
	}

	preferences, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not load preferences:", err)
	}
	if len(os.Args) > 1 && os.Args[1] == "login" {
		if !runLogin(os.Args[2:], &preferences) {
			return
		}
		// Successful interactive login continues into the workspace in this
		// process. Remove the subcommand before parsing the app flags below.
		os.Args = []string{os.Args[0]}
	}

	demoMode := flag.Bool("demo", false, "run with built-in data even when SLACK_TOKEN is set")
	liveMode := flag.Bool("live", false, "require a live Slack token")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), "Usage: gack [--demo|--live]\n       gack manifest [--open]\n       gack login [--client-id ID] [--no-browser]\n       gack update [--check]\n       gack groups <list|add|remove|clear>\n       gack logout\n       gack api [--demo] <command> [arguments]\n\n")
		fmt.Fprint(flag.CommandLine.Output(), "Open the terminal client, sign into a Slack workspace, or use the JSON agent API.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("gack " + buildVersion())
		return
	}
	if *demoMode && *liveMode {
		fmt.Fprintln(os.Stderr, "gack: --demo and --live cannot be used together")
		os.Exit(2)
	}

	backend, err := newBackend(*demoMode)
	if errors.Is(err, auth.ErrNotFound) && !*demoMode {
		err = loginInPlace(&preferences)
		if errors.Is(err, loginui.ErrCanceled) {
			return
		}
		if err == nil {
			backend, err = newBackend(false)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gack:", err)
		os.Exit(2)
	}

	for {
		action, runErr := runTUI(backend, &preferences)
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "gack:", runErr)
			os.Exit(1)
		}
		switch action {
		case ui.ExitLogin:
			if err := loginInPlace(&preferences); err != nil {
				if errors.Is(err, loginui.ErrCanceled) {
					return
				}
				fmt.Fprintln(os.Stderr, "gack login:", err)
				os.Exit(1)
			}
			backend, err = newBackend(false)
			if err != nil {
				fmt.Fprintln(os.Stderr, "gack:", err)
				os.Exit(1)
			}
		case ui.ExitUpdate:
			if err := runUpdate(nil, os.Stdout, os.Stderr, selfupdate.DefaultChecker(), selfupdate.Installer{}); err != nil {
				fmt.Fprintln(os.Stderr, "gack update:", err)
				os.Exit(1)
			}
			if err := restartSelf(os.Args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "gack: updated, but could not reopen:", err)
			}
			return
		default:
			return
		}
	}
}

func runGroups(arguments []string, stdout, stderr io.Writer) error {
	usage := func() {
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  gack groups list")
		fmt.Fprintln(stderr, "  gack groups add NAME REGEX")
		fmt.Fprintln(stderr, "  gack groups remove NAME")
		fmt.Fprintln(stderr, "  gack groups clear")
		fmt.Fprintln(stderr, "First matching rule wins; unmatched conversations appear under Other.")
	}
	if len(arguments) == 0 || arguments[0] == "help" || arguments[0] == "--help" || arguments[0] == "-h" {
		usage()
		return nil
	}
	preferences, err := config.Load()
	if err != nil {
		return fmt.Errorf("load preferences: %w", err)
	}
	switch arguments[0] {
	case "list":
		if len(arguments) != 1 {
			return errors.New("list takes no arguments")
		}
		if len(preferences.SidebarGroups) == 0 {
			fmt.Fprintln(stdout, "No sidebar groups. Add one with `gack groups add NAME REGEX`.")
			return nil
		}
		for index, group := range preferences.SidebarGroups {
			fmt.Fprintf(stdout, "%d. %s\t%s\n", index+1, group.Name, group.Pattern)
		}
		return nil
	case "add":
		if len(arguments) != 3 {
			usage()
			return errors.New("add needs a name and regex")
		}
		name, pattern := strings.TrimSpace(arguments[1]), strings.TrimSpace(arguments[2])
		if name == "" || pattern == "" {
			return errors.New("name and regex cannot be empty")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid regex: %w", err)
		}
		for _, group := range preferences.SidebarGroups {
			if strings.EqualFold(group.Name, name) {
				return fmt.Errorf("group %q already exists", name)
			}
		}
		preferences.SidebarGroups = append(preferences.SidebarGroups, config.SidebarGroup{Name: name, Pattern: pattern})
		if err := config.Save(preferences); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Added sidebar group %q. It will appear the next time gack opens.\n", name)
		return nil
	case "remove":
		if len(arguments) != 2 {
			return errors.New("remove needs a group name")
		}
		for index, group := range preferences.SidebarGroups {
			if !strings.EqualFold(group.Name, arguments[1]) {
				continue
			}
			preferences.SidebarGroups = append(preferences.SidebarGroups[:index], preferences.SidebarGroups[index+1:]...)
			if err := config.Save(preferences); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Removed sidebar group %q.\n", group.Name)
			return nil
		}
		return fmt.Errorf("group %q does not exist", arguments[1])
	case "clear":
		if len(arguments) != 1 {
			return errors.New("clear takes no arguments")
		}
		preferences.SidebarGroups = nil
		if err := config.Save(preferences); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Cleared sidebar groups.")
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func runTUI(backend gack.Backend, preferences *config.Preferences) (ui.ExitAction, error) {
	model := ui.NewWithSidebar(backend, config.SidebarPreferences{
		ChannelOrder: preferences.ChannelOrder,
		Sort:         preferences.SidebarSort,
		Groups:       preferences.SidebarGroups,
	}, func(sidebar config.SidebarPreferences) error {
		preferences.ChannelOrder = sidebar.ChannelOrder
		preferences.SidebarSort = sidebar.Sort
		preferences.SidebarGroups = sidebar.Groups
		return config.Save(*preferences)
	})
	currentVersion := buildVersion()
	if !envBool("GACK_NO_NOTIFICATIONS") {
		notifier := notify.Default()
		model.SetNotifier(notifier.Send)
	}
	if !developmentBuild(currentVersion) && !envBool("GACK_NO_UPDATE_CHECK") {
		checker := selfupdate.DefaultChecker()
		model.SetUpdateCheck(func(ctx context.Context) (string, error) {
			result, err := checker.Check(ctx, currentVersion, false)
			if err != nil || !result.UpdateAvailable {
				return "", err
			}
			return result.Latest, nil
		})
	}
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseAllMotion())
	final, err := program.Run()
	if err != nil {
		return ui.ExitNone, err
	}
	if result, ok := final.(*ui.Model); ok {
		return result.RequestedExit(), nil
	}
	return ui.ExitNone, nil
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
	backend, err := newBackend(demoMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "{\"ok\":false,\"command\":\"\",\"error\":{\"code\":\"backend_unavailable\",\"message\":%q}}\n", err.Error())
		os.Exit(agentcli.ExitError)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	status := agentcli.Run(ctx, backend, arguments, os.Stdout, os.Stderr, agentcli.Options{})
	cancel()
	os.Exit(status)
}

func runManifest(arguments []string, stdout, stderr io.Writer, openBrowser func(string) error) error {
	flags := flag.NewFlagSet("gack manifest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	open := flags.Bool("open", false, "open Slack's app creator with the manifest pre-filled")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gack manifest [--open]")
		fmt.Fprintln(stderr, "Print the Slack app manifest to standard output.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if _, err := io.WriteString(stdout, slackapp.Manifest()); err != nil {
		return fmt.Errorf("print manifest: %w", err)
	}
	if !*open {
		return nil
	}
	if openBrowser == nil {
		return errors.New("browser opener is unavailable")
	}
	fmt.Fprintln(stderr, "Opening Slack’s app creator with the manifest already filled in…")
	fmt.Fprintln(stderr, "After Slack creates the app, copy its Client ID and run `gack login --client-id ID`.")
	if err := openBrowser(slackapp.CreationURL()); err != nil {
		return fmt.Errorf("open Slack app creator: %w", err)
	}
	return nil
}

func newBackend(demoMode bool) (gack.Backend, error) {
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
		return nil, fmt.Errorf("no Slack login: %w", auth.ErrNotFound)
	}
	var bridge slack.InteractionBridge
	if endpoint := os.Getenv("GACK_INTERACTION_URL"); endpoint != "" {
		bridge = &slack.HTTPBridge{URL: endpoint, Token: os.Getenv("GACK_INTERACTION_TOKEN")}
	}
	return slack.New(slack.Config{Token: token, Bridge: bridge, MessageLimit: envInt("GACK_MESSAGE_LIMIT", 15)})
}

func runLogin(arguments []string, preferences *config.Preferences) bool {
	flags := flag.NewFlagSet("gack login", flag.ExitOnError)
	clientID := flags.String("client-id", firstNonEmpty(os.Getenv("GACK_SLACK_CLIENT_ID"), preferences.SlackClientID), "Slack app client ID")
	noBrowser := flags.Bool("no-browser", false, "print the authorization URL without opening it")
	redirectURI := flags.String("redirect-uri", firstNonEmpty(os.Getenv("GACK_SLACK_REDIRECT_URI"), auth.DefaultRedirectURI), "registered localhost OAuth redirect URI")
	flags.Parse(arguments)
	if *noBrowser && *clientID == "" {
		fmt.Fprintln(os.Stderr, "gack login: pass --client-id from your Slack app’s Basic Information page")
		fmt.Fprintln(os.Stderr, "Run `gack login` in a terminal for guided setup, or `gack manifest --open` to create the app.")
		os.Exit(2)
	}
	var credential auth.Credential
	var err error
	if *noBrowser {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		present := func(target string) error {
			fmt.Println("Open this URL to sign in to Slack:")
			fmt.Println(target)
			return nil
		}
		credential, err = (auth.OAuth{ClientID: *clientID, RedirectURI: *redirectURI, PresentURL: present}).Login(ctx)
	} else {
		wizardContext, cancel := context.WithCancel(context.Background())
		defer cancel()
		credential, err = loginui.Run(wizardContext, loginui.Config{
			ClientID: *clientID,
			Login: func(ctx context.Context, selectedClientID string) (auth.Credential, error) {
				authorizationContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				return (auth.OAuth{ClientID: selectedClientID, RedirectURI: *redirectURI}).Login(authorizationContext)
			},
		})
	}
	if err != nil {
		if errors.Is(err, loginui.ErrCanceled) {
			fmt.Println("Slack sign-in canceled. Nothing was saved.")
			return false
		}
		fmt.Fprintln(os.Stderr, "gack login:", err)
		os.Exit(1)
	}
	if err := auth.DefaultStore().Save(credential); err != nil {
		fmt.Fprintln(os.Stderr, "gack login: could not save credential:", err)
		os.Exit(1)
	}
	preferences.SlackClientID = credential.ClientID
	if err := config.Save(*preferences); err != nil {
		fmt.Fprintln(os.Stderr, "warning: signed in, but could not remember the client ID:", err)
	}
	workspace := firstNonEmpty(credential.TeamName, credential.TeamID, "Slack")
	fmt.Println("Signed in to " + workspace + ". Opening it now…")
	return true
}

func loginInPlace(preferences *config.Preferences) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	credential, err := loginui.Run(ctx, loginui.Config{
		ClientID: firstNonEmpty(os.Getenv("GACK_SLACK_CLIENT_ID"), preferences.SlackClientID),
		Login: func(ctx context.Context, clientID string) (auth.Credential, error) {
			authorizationContext, stop := context.WithTimeout(ctx, 5*time.Minute)
			defer stop()
			redirectURI := firstNonEmpty(os.Getenv("GACK_SLACK_REDIRECT_URI"), auth.DefaultRedirectURI)
			return (auth.OAuth{ClientID: clientID, RedirectURI: redirectURI}).Login(authorizationContext)
		},
	})
	if err != nil {
		return err
	}
	if err := auth.DefaultStore().Save(credential); err != nil {
		return fmt.Errorf("save Slack login: %w", err)
	}
	preferences.SlackClientID = credential.ClientID
	if err := config.Save(*preferences); err != nil {
		fmt.Fprintln(os.Stderr, "warning: signed in, but could not remember the client ID:", err)
	}
	return nil
}

func runUpdate(arguments []string, stdout, stderr io.Writer, checker selfupdate.Checker, installer selfupdate.Installer) error {
	flags := flag.NewFlagSet("gack update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	checkOnly := flags.Bool("check", false, "check the latest tagged version without installing it")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gack update [--check]")
		fmt.Fprintln(stderr, "Check for a tagged release and replace this gack executable in place.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	current := buildVersion()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	result, err := checker.Check(ctx, current, true)
	cancel()
	if err != nil {
		return err
	}
	if *checkOnly {
		if developmentBuild(current) {
			fmt.Fprintf(stdout, "This is a development build; the latest tagged release is %s.\n", result.Latest)
		} else if result.UpdateAvailable {
			fmt.Fprintf(stdout, "gack %s is available (you have %s). Run `gack update`.\n", result.Latest, current)
		} else {
			fmt.Fprintf(stdout, "gack %s is up to date.\n", current)
		}
		return nil
	}
	if developmentBuild(current) {
		return fmt.Errorf("development builds are not replaced automatically; install %s@%s", selfupdate.CommandPath, result.Latest)
	}
	if !result.UpdateAvailable {
		fmt.Fprintf(stdout, "gack %s is already up to date.\n", current)
		return nil
	}
	fmt.Fprintf(stdout, "Updating gack %s → %s…\n", current, result.Latest)
	installer.Stdout = stdout
	installer.Stderr = stderr
	updateContext, stopUpdate := context.WithTimeout(context.Background(), 10*time.Minute)
	err = installer.Install(updateContext, result.Latest)
	stopUpdate()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Updated gack in place to %s.\n", result.Latest)
	return nil
}

func restartSelf(arguments []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func developmentBuild(value string) bool {
	return value == "dev" || value == "(devel)" || strings.Contains(value, "+dirty")
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
