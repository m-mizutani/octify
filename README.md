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

## Install

```
go install github.com/m-mizutani/octify@latest
```

Then run `octify` and press `o` to sign in. There is nothing to configure first:
octify ships with its own OAuth app, and the client ID is compiled in.

No client secret is involved — the device flow does not use one — and the client
ID grants nothing by itself. Every token still comes from you approving the
request on github.com.

### Using your own OAuth app

Only needed if you want octify to authenticate as an app you control, or if you
are pointing it at GitHub Enterprise.

1. Register an OAuth app at <https://github.com/settings/developers>. The
   homepage and callback URLs are required by the form but unused here.
2. Tick **Enable Device Flow**. Without it GitHub answers every sign-in with
   `device_flow_disabled`.
3. Pass the client ID:

```
export OCTIFY_CLIENT_ID=<client id>
```

### Scopes

octify requests `repo` and `notifications`.

`notifications` alone is not enough: GitHub's documentation does not state that
it covers notifications from private repositories, and the review-request search
needs to see private pull requests to mark them.

The merged, closed and author markers come from one GraphQL query per poll. It
needs no scope beyond `repo`, which already covers reading the same pull
requests over REST.

## Usage

```
octify                # open the list
octify auth login     # sign in
octify auth logout    # delete the saved token and list (read state is kept)
octify auth status    # show whether a token is saved, and where
```

### Starting up

octify saves the list from every poll that changed it, so a start opens on the
notifications the last run ended with instead of an empty screen. Those rows are
whatever GitHub last said, not a guess: the status line marks them `saved list`
and replaces them the moment the first poll of the session answers, which it
always asks for in full.

The status line also says what octify is doing — `loading…` before the first
answer of a session, `updating…` while a later poll is in flight.

Turn the whole thing off with `--no-cache`: every start begins empty, and a file
an earlier run left behind is removed.

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

### Markers

Each row opens with five marker columns.

| Marker | Meaning |
| --- | --- |
| `▏` | You opened the pull request or issue |
| `x` | Selected |
| `●` / `○` | Unread / read |
| `R` | You are currently asked to review it |
| `M` | Merged |
| `C` | Closed without being merged |

A merged or closed row is drawn dim, so what still needs attention stands out.

`R`, `M` and `C` are the **current** state, not the reason the notification was
created, so `R` disappears once you have reviewed. The author marker likewise
comes from the pull request itself, not from the notification's `reason`, which
reports only one cause and drops `author` as soon as you are also mentioned.

They refresh on every poll that returns a changed notification list. A poll
GitHub answers with `304 Not Modified` costs no requests at all, markers
included — but the markers come from the pull requests themselves, not from the
notification threads, so one can be merged without its thread being touched
(GitHub does not notify you of your own actions). After ten such cycles in a
row, octify asks unconditionally, which refreshes everything. A marker is
therefore at most ten polls behind.

Rows whose subject GitHub cannot resolve — check suites, workflow runs,
releases, discussions and commits — carry no author bar and no state marker.
The same is true for one polling cycle when the lookup fails, which the status
line reports as `marker status unavailable`; the list itself is never lost
over it.

Press `?` for the same legend inside octify.

## Desktop notifications inside herdr

Run octify in a [herdr](https://herdr.dev) pane and a poll that finds something
new shows a toast. Nothing has to be configured: octify sees `HERDR_ENV=1`,
connects to the session's socket and asks herdr to draw it.

Outside a herdr pane there is nothing to connect to, and octify never looks. The
socket is not touched, no toast is composed, and a herdr that is not installed,
not running or has toasts switched off changes nothing about the list — a
failure to show one goes to the log and nowhere else.

**One poll shows at most one toast.** A single new notification is announced by
its repository and title; several are announced by their count, with the first
one named.

**The first poll of a session shows nothing.** It establishes what the next poll
is compared against. Without that, a start after a few days away would announce
the whole inbox as one arrival. The same happens after GitHub rejects the token:
the first list you see after signing in again is a new starting point, not news.

A notification is new when its thread was not in the previous poll's list, or
when it was and has been updated since — which is how a new comment on a thread
you are already watching arrives. Either way it is only announced if octify
would draw it as unread, so what you have already read stays quiet, and a new
comment on it does not, because an update supersedes the read record.

**A toast carries the repository name and the title, private repositories
included.** The socket it goes to is herdr's own, which only your user can
connect to — but the toast itself is drawn over whatever is on screen, so it is
visible while you are in another pane, sharing your screen or recording your
terminal. `--no-herdr` turns it off.

octify reads `HERDR_ENV`, `HERDR_SOCKET_PATH`, `HERDR_SESSION` and
`XDG_CONFIG_HOME` to find the session. It writes nothing to them. Where the
toast appears is herdr's `ui.toast` setting, which octify does not override.

Turn it off with `--no-herdr`. Give it a sound with `--herdr-sound=done`.

## Configuration

Every option is a flag with a matching environment variable. The flag wins.

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `--interval` | `OCTIFY_INTERVAL` | `60s` | Lower bound for polling |
| `--client-id` | `OCTIFY_CLIENT_ID` | compiled in | OAuth app client ID |
| `--api-base` | `OCTIFY_API_BASE` | `https://api.github.com` | REST root |
| `--graphql-base` | `OCTIFY_GRAPHQL_BASE` | `https://api.github.com/graphql` | GraphQL endpoint |
| `--web-base` | `OCTIFY_WEB_BASE` | `https://github.com` | Web root |
| `--credential-file` | `OCTIFY_CREDENTIAL_FILE` | see below | Token file, when no keychain |
| `--no-keyring` | `OCTIFY_NO_KEYRING` | `false` | Never use the OS keychain |
| `--state-file` | `OCTIFY_STATE_FILE` | see below | Read/unread records |
| `--state-ttl` | `OCTIFY_STATE_TTL` | `720h` | How long a record outlives its notification |
| `--cache-file` | `OCTIFY_CACHE_FILE` | see below | The list shown at the next start |
| `--no-cache` | `OCTIFY_NO_CACHE` | `false` | Never save the list, and delete a saved one |
| `--all` | `OCTIFY_ALL` | `false` | Start with read notifications shown |
| `--max-pages` | `OCTIFY_MAX_PAGES` | `10` | Pages of 50 fetched per poll |
| `--archive-gap` | `OCTIFY_ARCHIVE_GAP` | `1s` | Wait between bulk archive requests |
| `--no-herdr` | `OCTIFY_NO_HERDR` | `false` | Never show a desktop notification |
| `--herdr-sound` | `OCTIFY_HERDR_SOUND` | `none` | `none`, `done` or `request` |
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
- `poll-cache.json` — the list from the last poll, which is what a start shows
  before its own first poll answers. It holds the titles of everything in your
  inbox, private repositories included, so it is written mode `0600`. Safe to
  delete: the next poll writes it again. `auth logout` removes it, and so does
  GitHub rejecting the token.

## Development

```
go test -race ./...
go vet ./...
```
