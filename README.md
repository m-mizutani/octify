# octify

Triage GitHub notifications from the terminal.

octify lists your notifications, keeps them up to date in the background, and
lets you work through them with the keyboard: select rows, mark them read or
unread, and archive them in bulk.

## How read and unread work

**Read and unread are octify's own state, not GitHub's.** The Notifications API
has no "mark as unread" operation, so the only way to offer one is to keep the
decision locally.

Two consequences follow, and both are deliberate:

- **The notification badge in the GitHub web UI does not go down** when octify
  marks something read. octify never writes GitHub's read flag.
- **Deleting the read-state file resets everything** back to whatever GitHub
  says is unread. Nothing else is lost.

A notification without a local record follows GitHub's own unread flag, which is
why a first run agrees with the web UI. A record is dropped again when the
notification is updated (so a new comment brings the row back as unread), when
you archive it, and when it has been gone from the list for 30 days.

Archiving is the one operation that does reach GitHub: it marks the thread
**Done**, which removes it from the inbox on both sides.

## Setup

octify signs in with the OAuth device flow, which needs a client ID.

1. Register an OAuth app at <https://github.com/settings/developers>.
2. In the app settings, **enable device flow**. Without it GitHub answers every
   sign-in with `device_flow_disabled`.
3. Build with the client ID embedded, or pass it at runtime:

```
go build -ldflags "-X github.com/m-mizutani/octify/pkg/cli.DefaultClientID=<client id>" .
```

```
export OCTIFY_CLIENT_ID=<client id>
```

No client secret is involved: the device flow does not use one, and the client
ID is public information.

### Scopes

octify requests `repo` and `notifications`.

`notifications` alone is not enough: GitHub's documentation does not state that
it covers notifications from private repositories, and the review-request search
needs to see private pull requests to mark them.

## Usage

```
octify                # open the list
octify auth login     # sign in
octify auth logout    # delete the saved token (read state is kept)
octify auth status    # show whether a token is saved, and where
```

### Keys

| Key | Action |
| --- | --- |
| `j` / `↓`, `k` / `↑` | Move down, up |
| `g` `g`, `G` | Go to first, last |
| `ctrl+d`, `ctrl+u` | Half page down, up |
| `tab`, `shift+tab` | Next, previous tab |
| `1` … `5` | Go straight to a tab |
| `x` | Toggle the selection on this row |
| `*` `a`, `*` `n` | Select everything shown, clear the selection |
| `e` | Archive (Done) — reaches GitHub |
| `I` | Mark read — local only |
| `U` | Mark unread — local only |
| `a` | Show unread only, or everything |
| `enter` / `o` | Open in the browser |
| `r` | Poll now |
| `/` | Filter by repository or title |
| `esc` | Close help, then clear the filter, then stop archiving |
| `?` | Help |
| `q`, `ctrl+c` | Quit |

Selecting nothing and pressing `e`, `I` or `U` acts on the row under the cursor.

### Tabs

Notifications are split by what they point at: **PR**, **Issue**, **Actions**
(check suites and workflow runs) and **Other** (commits, releases, discussions
and anything new GitHub introduces). **All** holds everything.

A pull request you are currently asked to review is marked `R`. That is the
current state, not the reason the notification was created, so it disappears
once you have reviewed.

## Configuration

Every option is a flag with a matching environment variable. The flag wins.

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `--interval` | `OCTIFY_INTERVAL` | `60s` | Lower bound for polling |
| `--client-id` | `OCTIFY_CLIENT_ID` | build-time value | OAuth app client ID |
| `--api-base` | `OCTIFY_API_BASE` | `https://api.github.com` | REST root |
| `--web-base` | `OCTIFY_WEB_BASE` | `https://github.com` | Web root |
| `--credential-file` | `OCTIFY_CREDENTIAL_FILE` | see below | Token file, when no keychain |
| `--no-keyring` | `OCTIFY_NO_KEYRING` | `false` | Never use the OS keychain |
| `--state-file` | `OCTIFY_STATE_FILE` | see below | Read/unread records |
| `--state-ttl` | `OCTIFY_STATE_TTL` | `720h` | How long a record outlives its notification |
| `--all` | `OCTIFY_ALL` | `false` | Start with read notifications shown |
| `--max-pages` | `OCTIFY_MAX_PAGES` | `10` | Pages of 50 fetched per poll |
| `--archive-gap` | `OCTIFY_ARCHIVE_GAP` | `1s` | Wait between bulk archive requests |
| `--log-level` | `OCTIFY_LOG_LEVEL` | `warn` | `debug`, `info`, `warn`, `error` |
| `--log-file` | `OCTIFY_LOG_FILE` | — | Without it, logs are discarded |
| `--log-format` | `OCTIFY_LOG_FORMAT` | `text` | `text` or `json` |

**`--interval` cannot go below what GitHub asks for.** GitHub returns an
`x-poll-interval` header, normally 60 seconds, and octify waits at least that
long. Setting a smaller interval has no effect.

Logs go nowhere unless `--log-file` is given: the terminal belongs to the list.
Access tokens are removed from the output regardless of level or format.

### Where files live

Under `$XDG_CONFIG_HOME/octify`, or `os.UserConfigDir()/octify` when that is not
set:

- `credential.json` — only when the OS keychain is unavailable. Mode `0600`;
  octify refuses to read it if anyone else can.
- `read-state.json` — the read/unread records. Safe to delete.

## Development

```
go test -race ./...
go vet ./...
```
