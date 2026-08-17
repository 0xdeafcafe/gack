# Architecture

gack uses Bubble Tea as a single serialized event loop. Its application core follows one-way data flow:

```text
terminal input / lifecycle event
              │
              ▼
        intent handling
              │
              ▼
           reactor ──────► Slack / keychain / clipboard / update service
              ▲                                  │
              │                                  ▼
              └──────── immutable event ◄────────┘
                              │
                              ▼
                           reducer
                              │
                              ▼
                         model → view
```

The boundaries live in `internal/ui`:

- `events.go` is the data-only event vocabulary.
- `flow.go` owns the bounded effect-to-event adapter.
- `reactors.go` is the only UI-core layer that performs backend or external I/O.
- `reducer.go` is the only layer that applies application events to model state.
- `model.go` translates terminal input into intents and effects.
- `view.go` renders current state without performing I/O.

There is deliberately no general broadcast event bus. Effects run through Bubble Tea commands and return exactly one event to its existing queue. That avoids subscriber registries, unbounded fan-out, and a goroutine per component. Targeted events carry channel/thread identity, so the reducer can discard a late response after the user has navigated elsewhere.

## Adding a feature

1. Add a data-only event to `events.go`.
2. Add a reactor that performs the external work and returns that event.
3. Handle the event in `reduce` and describe any next effect.
4. Render the resulting state.
5. Test the reactor and reducer separately, including stale/out-of-order delivery.

A future Socket Mode or Events API transport should normalize incoming Slack changes into the same event vocabulary. The reducer and views should not need to know whether an event came from a request response, a real-time stream, the demo backend, or an agent action.
