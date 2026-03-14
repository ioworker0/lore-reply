# lore-reply

`lore-reply` is a local single-user web tool for replying to a patch mail from `lore.kernel.org` without manually rebuilding the reply draft each time.

It uses `b4` to fetch one target message, turns it into an editable plain-text reply draft, saves that draft under `/tmp/lore-reply/drafts/`, and shows a ready-to-run `git send-email` command.

## Features

- Single Go binary
- Local-only web UI by default on `127.0.0.1:9110`
- `b4` integration with startup validation
- Optional startup URL with automatic browser open and auto-load
- Plain-text editor with monospace fonts and a 72-column ruler
- Full-screen body editor for long replies
- Editable `From`, `To`, `Cc`, `Subject`, and `Body`
- `To` defaults to the original sender, with the remaining audience moved to `Cc`
- Draggable recipients between `To` and `Cc`
- Structured recipient add dialog with optional name and required email
- Stable draft path generated from `Message-ID` and subject
- Existing saved drafts are restored when you load the same lore URL again
- `Reload` discards the current saved draft state and fetches a fresh draft from lore
- `Load Another` returns to the URL input so you can switch to a different mail
- Save result dialog with a copy button for the generated `git send-email` command
- `From Name` and `From Email` are saved in local browser storage after a successful save
- Optional idle-exit mode driven by page heartbeat
- Automatic draft save every 5 seconds while there are unsaved changes

## Scope

Current scope is intentionally small:

- one loaded draft at a time
- no direct mail sending from the web UI
- no draft list
- no thread management beyond replying to a single lore message

## Requirements

- Go `1.24` or newer
- `b4` available in `PATH`, or an explicit path passed with `--b4`
- `git send-email` installed if you want to send the saved draft

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

If `--url` is provided, `lore-reply` opens the browser on startup and automatically runs the page `Load` step for that URL.

If `--exit-on-idle` is also provided, the page sends a heartbeat every 5 seconds and the server shuts down automatically after the heartbeat disappears for a short idle window.

## Workflow

1. Paste a `lore.kernel.org` message URL into the page.
2. Click `Load`.
3. `lore-reply` calls `b4 mbox --single-message` and builds an editable reply draft.
4. Edit `From`, recipients, subject, and body.
5. Dirty drafts are auto-saved every 5 seconds.
6. Click `Save` at any time for an immediate save and a ready-to-run `git send-email` command.
7. The draft is written under `/tmp/lore-reply/drafts/`.
8. Copy the generated `git send-email` command and run it in your shell.

With `--url`, steps `1` and `2` are performed automatically after startup.

If you load the same lore URL again later, `lore-reply` restores the saved draft from `/tmp/lore-reply/drafts/` instead of overwriting it.

Use `Reload` in the top-right corner if you want to discard the current saved draft state and fetch a fresh copy from lore.

## Draft Output

Saved drafts are plain-text mail files. The generated send command includes:

- `--from`
- `--in-reply-to`
- one `--to` per `To` recipient
- one `--cc` per `Cc` recipient
- the absolute draft path

The same loaded draft overwrites the same file on repeated saves.

Each saved draft also writes a small metadata sidecar so custom `To` and `Cc` changes can be restored together with the saved body.

## Notes

- The UI is designed for kernel-style plain-text mail replies.
- The quoted original mail is part of the editable body.
- `b4` errors are shown directly in the page without extra translation.
- The app binds to localhost by default and is meant to be used as a local tool.
- When a loaded draft is dirty, the page auto-saves it to disk every 5 seconds.
- Auto-save reduces accidental loss, but the most recent unsaved keystrokes can still be lost if the browser exits before the next save window.
