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

Installed builds check the tagged Go module release in the background at most once every six hours. When a newer version exists, the header shows `u update`; pressing `u` replaces the executable you launched and reopens gack. The same flow is available explicitly:

```sh
gack update --check
gack update
```

The update is built through your installed Go toolchain, verified before replacement, and written atomically beside the current executable. Set `GACK_NO_UPDATE_CHECK=1` to disable the quiet background check.

In the demo, open `#platform`, select the Shipyard release message, and press `1`. That walks through configure → validate → review → deploy without touching anything real.

## The useful keys

| Key | What it does |
| --- | --- |
| `Ctrl+K` | Jump to a channel or search workspace messages |
| `Ctrl+F` | Find within the open conversation or thread |
| `j` / `k` | Move through channels, messages, and choices |
| `Enter` / `t` | Open a thread |
| `c` | Open the multiline composer or thread reply |
| `r` | Add or remove an emoji reaction |
| `y` | Copy the selected message |
| `e` / `Ctrl+Up` | Edit the selected / latest message you wrote |
| `i` or `1`–`9` | Use Block Kit controls on the selected message |
| `a` / `n` | Open activity / unread notifications |
| `Tab` | Move between the sidebar, conversation, and thread |
| `s` (sidebar) | Cycle Manual, Alphabetical, and Attention sorting |
| `Shift+J` / `Shift+K` | Move a channel down / up |
| mouse drag | Reorder channels |
| `R` | Refresh the current view |
| `?` | Show the in-app key guide |

Terminals generally do not send the macOS Command modifier to terminal programs. If you want a literal `Cmd+K`, add a terminal key mapping that sends `Ctrl+K` (hex `0x0b`); `gack` will open the same floating palette. The same idea maps `Cmd+Up` to `Ctrl+Up` for edit-last-message.

Channel order is saved to `~/.config/gack/config.json` (or the platform-equivalent user config directory).

### Writing without fighting the editor

The composer is multiline and preserves bracketed paste, so a whole code block can go straight in without each newline trying to send it. `Enter` adds a line; `Ctrl+S` or `Option+Enter` sends.

The usual terminal editing vocabulary works inside it:

- `Option+Left/Right` moves by word and `Option+Backspace` removes a word.
- `Ctrl+A/E` moves to the start/end of a line.
- `Ctrl+U` removes back to the start of a line—the reliable terminal equivalent of `Cmd+Delete`.
- `Ctrl+V` and normal bracketed terminal paste preserve multiple lines.
- `Ctrl+Up` with an empty composer edits your latest message; `e` edits your selected message.

The terminal owns graphical text selection and `Cmd+C`. Hold `Shift` while dragging when your terminal reserves plain mouse events for `gack`, then copy normally.

## Connect a Slack workspace

The cleanest setup is a Slack app with a user token. User tokens let `gack` see the same joined conversations you can see and are required by Slack’s `search.messages` API.

Run the guided setup:

```sh
gack login
```

The terminal walks through three explicit steps: open Slack’s pre-filled app creator, paste the numeric Client ID from **Basic Information**, then approve the workspace in your browser. It shows which control is focused, waits visibly for browser approval, and lets you retry or edit the Client ID without exposing a token. Successful login continues straight into the workspace in the same process.

If you already have the Client ID, skip straight to confirmation:

```sh
gack login --client-id '123456789.123456789'
```

During sign-in, gack opens Slack in your browser, waits on a localhost callback, and exchanges the result with PKCE—there is no client secret in the binary. On macOS, the resulting user token lives in your login Keychain. Run `gack logout` to remove it. The Client ID is not secret and is remembered in the normal preferences file for future logins. Starting plain `gack` while signed out opens this wizard automatically and then continues into Slack.

`gack manifest` prints the same reusable YAML, and `gack manifest --open` opens Slack’s pre-filled creator directly when you would rather handle the steps yourself. The checked-in copy remains available as [`slack-manifest.example.yaml`](slack-manifest.example.yaml).

If a browser cannot be opened automatically, use `gack login --client-id ID --no-browser`; it prints the same authorization URL. `SLACK_TOKEN` remains available for automation and temporary sessions, but it is no longer the normal setup path.

A bot token also works, but it only sees conversations the bot has joined and legacy workspace-wide search needs a user token. Slack currently applies stricter history rate limits to some non-Marketplace apps, so the default live window is deliberately 15 messages. You can change it—within the app’s hard cap of 100—with:

```sh
GACK_MESSAGE_LIMIT=50 gack --live
```

## Let an agent use gack

`gack api` is a non-interactive, JSON-only surface over the same bounded backend and saved workspace login. That gives an agent something predictable to call without scraping terminal paint:

```sh
gack api channels --unread
gack api messages alerts
gack api thread incidents 1786924800.000100
gack api search 'checkout latency'
gack api activity --unread
```

Reading and writing are deliberately separate. An agent can only speak when it invokes an explicit mutation:

```sh
gack api send platform 'Canary is healthy.'
gack api send incidents 'Looking now.' --thread 1786924800.000100
gack api edit platform 1786924800.000200 'Canary is healthy in both regions.'
gack api react incidents 1786924800.000100 eyes
gack api react incidents 1786924800.000100 eyes --remove
```

For an observer, lock the whole process read-only:

```sh
GACK_READ_ONLY=1 gack api messages alerts
```

With that guard, `send`, `edit`, and `react` return a structured `read_only` error. The credential still carries your Slack permissions, so only expose the command to an agent you trust; use a separately scoped Slack app/token when you want a harder workspace-side boundary. Run `gack api help` for the machine-readable command catalog. Add `--demo` immediately after `api` to exercise every command without a workspace.

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
- Sidebar sort modes retain one manual order and only derive alternate views; they do not duplicate channel data.
- Workspace bootstrap asks Slack only for conversations you joined, avoiding workspace-wide channel scans.
- Network calls have deadlines, response size limits, and one rate-limit-aware retry.

If you want a hard runtime ceiling as well, Go understands `GOMEMLIMIT`:

```sh
GOMEMLIMIT=64MiB gack --live
```

## What is here today

- Live channels, DMs, conversation history, posting/editing, threads, search, and reactions
- Activity and notification views (mentions are derived from search; press `R` to refresh)
- Slack mrkdwn mentions, channels, links, common emoji, and Block Kit rendering
- Interactive demo workflows and a live bridge protocol
- Responsive narrow-terminal layout, actionable connection recovery, mouse wheel navigation, and persistent channel ordering
- Deterministic tests for the Slack transport, Block Kit parser, interaction workflow, and viewport dimensions

Still intentionally missing: files, message deletion, huddles, canvases, custom emoji downloads, and a Socket Mode event stream. The live client refreshes on navigation or `R`; real-time events are the next obvious transport layer.

## Build it

The UI core uses a single-queue, event → reducer → effect architecture; see [the architecture note](docs/architecture.md) for the boundaries and extension pattern.

```sh
make check   # format check, vet, and tests
make build   # ./bin/gack
make install # installs to GOBIN
```

The project is split around a small backend interface. The TUI knows nothing Slack-specific, which keeps the demo deterministic and makes future Socket Mode or alternative transports pleasantly boring to add.

## A small naming note

It is pronounced however you said it the first time.
