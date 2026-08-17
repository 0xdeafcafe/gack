package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
	"github.com/0xdeafcafe/gack/internal/slack"
	"github.com/0xdeafcafe/gack/internal/ui"
)

var version = "dev"

func main() {
	demoMode := flag.Bool("demo", false, "run with built-in data even when SLACK_TOKEN is set")
	liveMode := flag.Bool("live", false, "require a live Slack token")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("gack " + buildVersion())
		return
	}

	preferences, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not load preferences:", err)
	}
	var backend gack.Backend
	token := firstNonEmpty(os.Getenv("SLACK_TOKEN"), os.Getenv("SLACK_USER_TOKEN"), os.Getenv("SLACK_BOT_TOKEN"))
	if *demoMode || (token == "" && !*liveMode) {
		backend = demo.New()
	} else {
		if token == "" {
			fmt.Fprintln(os.Stderr, "gack: --live needs SLACK_TOKEN (prefer a user token for search and full conversation access)")
			os.Exit(2)
		}
		var bridge slack.InteractionBridge
		if endpoint := os.Getenv("GACK_INTERACTION_URL"); endpoint != "" {
			bridge = &slack.HTTPBridge{URL: endpoint, Token: os.Getenv("GACK_INTERACTION_TOKEN")}
		}
		messageLimit := envInt("GACK_MESSAGE_LIMIT", 15)
		client, err := slack.New(slack.Config{Token: token, Bridge: bridge, MessageLimit: messageLimit})
		if err != nil {
			fmt.Fprintln(os.Stderr, "gack:", err)
			os.Exit(2)
		}
		backend = client
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
