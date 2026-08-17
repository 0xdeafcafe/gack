# gack

Slack, without the tab sprawl.

`gack` is a small, keyboard-first Slack client for the terminal. It keeps the useful bits close—channels, messages, threads, activity, search, reactions, and Block Kit workflows—and leaves the rest for another day.

It is early, but it is real. The included demo needs no Slack account and contains a two-step production deployment dialogue, so you can try the whole interaction model before wiring up a workspace.

## Take it for a spin

You need Go 1.25 or newer.

```sh
go install github.com/0xdeafcafe/gack/cmd/gack@latest
gack --demo
```

From a checkout:

```sh
make run
```

In the demo, open `#platform`, select the Shipyard release message, and press `1`. That walks through configure → validate → review → deploy without touching anything real.

## The useful keys

| Key | What it does |
| --- | --- |
| `Ctrl+K` | Jump to a channel or search workspace messages |
| `Ctrl+F` | Find within the open conversation or thread |
| `j` / `k` | Move through channels, messages, and choices |
| `Enter` / `t` | Open a thread |
| `c` | Compose a message or thread reply |
| `r` | Add or remove an emoji reaction |
| `i` or `1`–`9` | Use Block Kit controls on the selected message |
| `a` / `n` | Open activity / unread notifications |
| `Tab` | Move between the sidebar, conversation, and thread |
| `Shift+J` / `Shift+K` | Move a channel down / up |
| mouse drag | Reorder channels |
| `R` | Refresh the current view |
| `?` | Show the in-app key guide |

Terminals generally do not send the macOS Command modifier to terminal programs. If you want a literal `Cmd+K`, map that chord to `Ctrl+K` in your terminal profile; `gack` will do the rest.

Channel order is saved to `~/.config/gack/config.json` (or the platform-equivalent user config directory).

## Connect a Slack workspace

The cleanest setup is a Slack app with a user token. User tokens let `gack` see the same joined conversations you can see and are required by Slack’s `search.messages` API.

1. Create a Slack app from [`slack-manifest.example.yaml`](slack-manifest.example.yaml).
2. Install it to your workspace.
3. Copy the **User OAuth Token** (`xoxp-…`) from OAuth & Permissions.
4. Run:

```sh
export SLACK_TOKEN='xoxp-your-token'
gack --live
```

Treat that token like a password. A password manager, `direnv` backed by a secret store, or your OS keychain is a much better home than shell history.

A bot token also works, but it only sees conversations the bot has joined and legacy workspace-wide search needs a user token. Slack currently applies stricter history rate limits to some non-Marketplace apps, so the default live window is deliberately 15 messages. You can change it—within the app’s hard cap of 100—with:

```sh
GACK_MESSAGE_LIMIT=50 gack --live
```

## Block Kit and interactive dialogues

Messages are not flattened into a blob. `gack` parses and renders sections, fields, context, rich text, headers, dividers, images, action rows, buttons, static selects, inputs, checkboxes, radio buttons, date/time fields, and multi-step modal replacement. Unknown blocks stay visible as an explicit fallback instead of vanishing.

There is one important Slack boundary: Slack’s supported APIs do not offer a custom client an endpoint to “click” a control owned by another Slack app. In Slack’s own clients, Slack creates the interaction payload and delivers it to the app’s configured request URL or Socket Mode connection. A third-party client cannot forge that hand-off through the Web API.

For live workflows, `gack` uses a tiny interaction bridge:

```sh
export GACK_INTERACTION_URL='https://workflow.example.com/gack/interactions'
export GACK_INTERACTION_TOKEN='your-bridge-token'
gack --live
```

The bridge receives normalized Block Kit actions and view submissions and can return a modal, field errors, a replacement step, or a notice. The contract is documented in [docs/interaction-bridge.md](docs/interaction-bridge.md). This is intentionally honest plumbing: the demo proves the TUI interaction, and the bridge is the one workspace-specific seam needed for production workflows.

## Kept lean on purpose

- Message history is bounded: 15 live messages by default, 100 maximum.
- There is no per-channel history cache; switching channels releases the previous window.
- Search results and locally posted messages are bounded.
- Conversation and message panes only format rows that can be seen around the cursor.
- Channel lists are windowed too, including mouse hit-testing.
- Network calls have deadlines, response size limits, and one rate-limit-aware retry.

If you want a hard runtime ceiling as well, Go understands `GOMEMLIMIT`:

```sh
GOMEMLIMIT=64MiB gack --live
```

## What is here today

- Live channels, DMs, conversation history, posting, threads, search, and reactions
- Activity and notification views (mentions are derived from search; press `R` to refresh)
- Slack mrkdwn mentions, channels, links, common emoji, and Block Kit rendering
- Interactive demo workflows and a live bridge protocol
- Responsive narrow-terminal layout, mouse wheel navigation, and persistent channel ordering
- Deterministic tests for the Slack transport, Block Kit parser, interaction workflow, and viewport dimensions

Still intentionally missing: files, message editing/deletion, huddles, canvases, custom emoji downloads, and a Socket Mode event stream. The live client refreshes on navigation or `R`; real-time events are the next obvious transport layer.

## Build it

```sh
make check   # format check, vet, and tests
make build   # ./bin/gack
make install # installs to GOBIN
```

The project is split around a small backend interface. The TUI knows nothing Slack-specific, which keeps the demo deterministic and makes future Socket Mode or alternative transports pleasantly boring to add.

## A small naming note

It is pronounced however you said it the first time.
