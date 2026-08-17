# Interaction bridge

The bridge is the adapter between `gack` and an interactive workflow that would normally receive a Slack-generated Block Kit payload.

This is needed because Slack does not publish a Web API method for a third-party client to click another app’s button or submit its modal. The bridge is appropriate when you own the production workflow and can expose an authenticated adapter for it. It is not a way to bypass an unrelated Slack app’s security boundary.

## Request

`gack` sends an HTTP `POST` with `Content-Type: application/json`. When `GACK_INTERACTION_TOKEN` is set, it also sends `Authorization: Bearer <token>`.

A message button looks like this:

```json
{
  "type": "block_actions",
  "user_id": "U123",
  "channel_id": "C123",
  "message_ts": "1786924800.000100",
  "thread_ts": "",
  "block_id": "release_actions",
  "action_id": "deploy_start",
  "action_type": "button",
  "value": "2026.08.17-rc3"
}
```

A modal submission includes all state, grouped by `block_id` and then `action_id`:

```json
{
  "type": "view_submission",
  "user_id": "U123",
  "channel_id": "C123",
  "view_id": "deploy-configure",
  "callback_id": "deploy_configure",
  "private_metadata": "opaque-workflow-state",
  "state": {
    "release": { "version": "2026.08.17-rc3" },
    "target": { "environment": "production" },
    "approvals": { "checked": ["release", "change"] }
  }
}
```

Single-value controls use strings. Checkboxes and multi-selects use arrays of strings. Hidden adapter values are returned as strings.

## Response

An empty `2xx` response simply closes the interaction. A JSON response can contain one of the following.

Show a short notice and close:

```json
{ "notice": "Deployment started" }
```

Return field errors. Keys are Block Kit `block_id` values:

```json
{
  "errors": {
    "change": "A change ticket is required"
  }
}
```

Open a modal:

```json
{
  "view": {
    "id": "deploy-configure",
    "callback_id": "deploy_configure",
    "private_metadata": "opaque-workflow-state",
    "title": "Start deployment",
    "submit": "Review",
    "close": "Cancel",
    "blocks": [
      {
        "type": "input",
        "block_id": "release",
        "label": "Release",
        "elements": [
          {
            "type": "plain_text_input",
            "action_id": "version",
            "initial_value": "2026.08.17-rc3"
          }
        ]
      },
      {
        "type": "input",
        "block_id": "target",
        "label": "Environment",
        "elements": [
          {
            "type": "static_select",
            "action_id": "environment",
            "initial_option": "staging",
            "options": [
              { "text": "Staging", "value": "staging" },
              { "text": "Production", "value": "production" }
            ]
          }
        ]
      }
    ]
  }
}
```

Use `replace` instead of `view` to move an open modal to its next step. `Esc` then returns to the previous step:

```json
{
  "replace": {
    "id": "deploy-confirm",
    "callback_id": "deploy_confirm",
    "title": "Confirm deployment",
    "submit": "Deploy",
    "close": "Back",
    "blocks": []
  }
}
```

The bridge response uses `gack`’s normalized block shape, not Slack’s nested text objects. This keeps adapters small and makes the same response useful in tests and non-Slack workflow services.

## Failure behavior

- Non-`2xx` responses become visible errors in the footer.
- Bodies are limited to 2 MiB.
- Calls time out after 15 seconds.
- The bearer token is optional at the protocol level, but a production bridge should always authenticate and authorize both the user and requested action.
