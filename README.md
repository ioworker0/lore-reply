# lore-reply

`lore-reply` is a local single-user web tool for replying to a patch mail from `lore.kernel.org` or composing a brand-new plain-text patch mail without manually rebuilding the draft each time.

It uses `b4` to fetch one target message when you are replying, or starts an empty draft when you are composing a fresh mail, then saves that draft under `/tmp/lore-reply/drafts/` and either shows a ready-to-run `git send-email` command or sends it directly from the page.

## Features

- Single Go binary
- Local-only web UI by default on `127.0.0.1:9110`
- `b4` integration with startup validation
- Browser opens automatically on startup
- Optional startup URL with automatic auto-load
- Plain-text editor with monospace fonts and a 72-column ruler
- Full-screen body editor for long replies
- `New Mail` flow for composing a fresh message without loading lore first
- Editable `From`, `To`, `Cc`, `Subject`, and `Body`
- `To` defaults to the original sender, with the remaining audience moved to `Cc`
- Draggable recipients between `To` and `Cc`
- Structured recipient add dialog with optional name and required email
- Stable draft path generated from `Message-ID` and subject
- Existing saved drafts are restored when you load the same lore URL again
- `Reload` discards the current saved draft state and fetches a fresh draft from lore
- `Load Another` returns to the URL input so you can switch to a different mail
- Send dialog with direct send, 30-second delayed send, and command copy actions
- Delayed-send progress bar with cancel support in both the main view and the full-screen body editor
- Direct-send result output from `git send-email` shown in the UI
- `From Name` and `From Email` are saved in local browser storage after a successful save
- Optional idle-exit mode driven by page heartbeat
- Automatic draft save every 5 seconds while there are unsaved changes
- Inbox view for syncing related lore threads from a mailing list
- Inbox filters for mailing list, your email addresses, time range, and match mode
- Thread-first inbox browser with per-message thread tree and jump back into the reply composer
- Collapsible inbox thread list for reading long message bodies

## Scope

Current scope is intentionally small:

- one composer draft at a time
- one local inbox cache snapshot at a time
- no server-side user accounts or shared state
- no historical inbox merge yet; each sync replaces the previous inbox cache snapshot

## Requirements

- Go `1.24` or newer
- `b4` available in `PATH`, or an explicit path passed with `--b4`
- `git send-email` installed if you want to send the saved draft or send directly from the web UI

## Build

```bash
make build
```

This produces:

```bash
./lore-reply
```

Run tests with:

```bash
make test
```

Run without building first:

```bash
make run
```

## Run

```bash
./lore-reply
```

Defaults:

- `--from-name=nobody`
- `--from-email=nobody@kernel.org`
- `--listen=127.0.0.1:9110`

Available flags:

```text
--b4
--from-name
--from-email
--listen
--url
--exit-on-idle
```

Example:

```bash
./lore-reply \
  --b4 /path/to/b4 \
  --from-name "Your Name" \
  --from-email "you@example.com" \
  --listen 127.0.0.1:9110 \
  --url "https://lore.kernel.org/linux-mm/..." \
  --exit-on-idle
```

Then open:

```text
http://127.0.0.1:9110
```

`lore-reply` opens the browser on startup. If `--url` is provided, it also automatically runs the page `Load` step for that URL.

If `--exit-on-idle` is also provided, the page sends a heartbeat every 5 seconds and the server shuts down automatically after the heartbeat disappears for 10 minutes.

## Direct Reply Workflow

1. Paste a `lore.kernel.org` message URL into the page.
2. Click `Load`.
3. `lore-reply` calls `b4 mbox --single-message` and builds an editable reply draft.
4. Edit `From`, recipients, subject, and body.
5. Dirty drafts are auto-saved every 5 seconds.
6. Click `Send Mail` to save the draft and open the send dialog.
7. Choose `Send in 30s`, `Send Now`, or `Copy Command`.
8. Delayed sends stay visible in the top progress bar and can be canceled before they fire.
9. The draft is written under `/tmp/lore-reply/drafts/` before sending.

With `--url`, steps `1` and `2` are performed automatically after startup.

If you load the same lore URL again later, `lore-reply` restores the saved draft from `/tmp/lore-reply/drafts/` instead of overwriting it.

Use `Reload` in the top-right corner if you want to discard the current saved draft state and fetch a fresh copy from lore.

## New Mail Workflow

1. Open the `Composer` view.
2. Click `New Mail`.
3. Fill in `To`, optional `Cc`, `Subject`, and `Body`.
4. Dirty drafts are auto-saved every 5 seconds after a subject is present.
5. Click `Send Mail` to save the draft and open the send dialog.
6. Choose `Send in 30s`, `Send Now`, or `Copy Command`.

Fresh mails are saved under `/tmp/lore-reply/drafts/` with a generated `compose-...` filename and sent without `--in-reply-to`.

## Inbox Workflow

1. Open the `Inbox` view.
2. Set the mailing list URL, your email addresses, range, and match mode.
3. Click `Sync`.
4. `lore-reply` searches lore/public-inbox for matching messages, expands them into full threads with `b4`, and stores a local inbox cache.
5. Browse the left-hand thread list, inspect the selected message at the top-right, and use the thread tree below it to move through replies.
6. Click `Reply In Composer` on any message to jump back into the existing direct-reply editor.

## Inbox Cache

Inbox sync state is stored locally at:

```text
/tmp/lore-reply/inbox-state.json
```

Current behavior is replace-on-sync, not merge-on-sync. A successful new sync overwrites the previous inbox cache snapshot.

## Draft Output

Saved drafts are plain-text mail files. The generated send command includes:

- `--from`
- `--in-reply-to` for reply drafts
- one `--to` per `To` recipient
- one `--cc` per `Cc` recipient
- the absolute draft path

The same loaded draft overwrites the same file on repeated saves.

Each saved draft also writes a small metadata sidecar so custom `To` and `Cc` changes can be restored together with the saved body.

Direct sends use `git send-email --confirm=never`, so they do not stop on the interactive `Send this email?` prompt. The command output is returned to the UI whether the send succeeds or fails.

## Notes

- The UI is designed for kernel-style plain-text mail replies.
- The quoted original mail is part of the editable body.
- `b4` errors are shown directly in the page without extra translation.
- The app binds to localhost by default and is meant to be used as a local tool.
- When a loaded draft is dirty, the page auto-saves it to disk every 5 seconds.
- Auto-save reduces accidental loss, but the most recent unsaved keystrokes can still be lost if the browser exits before the next save window.
- Direct sending is non-interactive. If your `git send-email` setup still requires terminal prompts for SMTP credentials or other input, the send will fail and the raw output is shown in the page.
- Inbox sync uses a local JSON cache today, not SQLite.
