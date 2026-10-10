# barq

An API client for the terminal: a Neovim-based UI for people,
and a CLI that lets scripts and AI agents do the same things **without ever
seeing your secrets**.

Requests are `.http` files in your project. Environments, variables and run
history are saved per project directory in `~/.barq`, never inside the project.

![barq demo: log in, list users with the captured token, filter with jq, browse runs](assets/demo.gif)

## Install

Requires Go 1.24 or newer.

```sh
go install github.com/iskaa02/barq@latest
```

This puts `barq` in `$(go env GOPATH)/bin` (usually `~/go/bin`); make sure
that's on your `PATH`. Check with `barq --version`.

To build from a clone instead: `go build -o barq .`

## The UI

`barq` opens a Neovim-based UI for the project directory: `.http` files | editor | response.
The editor is an embedded Neovim running your own config. Requires **Neovim 0.10+** on `PATH`.
`barq <url>` or `barq "curl …"` appends the request to `scratch.http` and opens it.

| Keys | |
|---|---|
| `ctrl+enter` / `alt+enter` | send the request under the cursor (unsaved edits included) |
| `alt+h` / `alt+l` | focus the pane to the left / right |
| `alt+e` | cycle environment |
| `ctrl+p` | `.http` file picker |
| `ctrl+q` | quit |
| sidebar: `j` `k` `enter` `n` `R` | move, open, new file, rescan |
| response: `tab` / `S-tab` | next / previous view |
| response: `gb` `gh` `gr` `gi` | body / headers / raw / info view |
| response: `gc` | capture the value under the cursor into a variable |
| response: `gd` | diff with the previous response |

Editor commands: `:BarqSend`, `:BarqEnv [name]`, `:BarqSave`, `:BarqCurl` (copy as curl),
`:BarqImportSaved`, `:BarqQuit`.

Requests are plain `.http` files:

```
### login                          # starts a request; the name is optional
# @name login
# @capture token = .data.accessToken
# @capture requestId = header X-Request-Id   # a response header
# @capture session = cookie session_id       # a Set-Cookie value
# @expect status 200
# @expect jq .data.ok
# @confirm                         # ask before sending
POST {{baseUrl}}/login
Content-Type: application/json
# X-Debug: 1                       # a commented header is disabled

{"user": "me"}                     # or `< ./payload.json` to use a file
```

`{{variables}}` come from the current environment. A `< ./payload.json` body
and `@file` uploads are relative to the project directory (where you started
barq), not the `.http` file.

File uploads use `multipart/form-data`: with that `Content-Type`, the body is
one `name: value` field per line (barq adds the boundary itself):

```
### upload
POST {{baseUrl}}/upload
Content-Type: multipart/form-data

name: {{user}}
avatar: @./images/me.png           # @path = a file
# note: off                        # a commented field is disabled
```

## The CLI (for scripts and AI agents)

Commands: `run`, `ls`, `show`, `curl`, `history`, `env`, `import`, `ai`. A request is
`file.http#name`, `file.http#n` (1-based block number) or a bare `name` that is unique in the project.

```sh
barq ai                                   # the full guide, written for agents
barq ls --json
barq run auth.http#login                  # stores {{token}} as a secret
barq run "list orders" --jq '.data[0]' --json
barq show auth.http#2
barq env set dev token - --secret         # value from stdin
barq import openapi.json
barq import --saved                       # write old saved requests out as .http files
```

Edit the `.http` files directly to add or change requests.

**Large responses.** Bodies are kept whole, in history too, scrubbed of
secrets. The UI shows the first 10 MB, and `barq run` prints the first 1 MB
and says where the rest is. Read it with `barq history body <run-id>` and
`--jq`, `--grep`, `--lines`, `--bytes` or `--path`, or save it with
`barq run … -o file`. Reading stops at 1 GB by default (`--max-body`,
`BARQ_MAX_BODY_MB`, 0 for no limit). History stays under 500 MB per project
(`BARQ_HISTORY_MB`) by deleting the oldest bodies first and keeping their runs.

To let an agent such as Claude Code use it, add to the project's `CLAUDE.md`:

> Use `barq` for HTTP/API calls in this project. Run `barq ai` first to learn
> how. Never put real tokens in requests; use `{{variables}}` and captures.

## Secrets

- Variables named like credentials (`token`, `password`, `api_key`, …), or
  marked secret (`--secret` in the CLI),
  are stored in the OS keyring (Secret Service/KWallet, Keychain, Credential
  Manager). Set `BARQ_KEYRING=off` to keep them in the workspace file instead
  (barq shows a warning in the title bar).
- CLI output never contains secret values, credential headers, or
  credential-like fields in bodies and query strings. `--reveal` works only
  for a person at an interactive terminal.
- History on disk is scrubbed the same way.
- Environments can be **protected** (OpenAPI imports protect production).
  In a protected environment the CLI asks a person at the terminal before
  sending anything that can change something (POST, PUT, PATCH, DELETE…);
  GET, HEAD and OPTIONS go through. `barq env protect prod --all` asks
  before every request. The prompt shows the real method and URL, with
  secrets hidden, and one keypress answers it:

  ```
  ⚠ Sending Delete user in the protected environment "prod"
    DELETE https://api.example.com/users/42
  Continue? [y/N]
  ```

  It's read from the terminal itself, so piped input can't answer it.
  Unprotecting an environment or making a secret variable visible needs
  the same confirmation.

**Limits.** Redaction keeps secrets out of what barq prints and stores. It
can't stop an agent from deliberately sending `{{token}}` to a server it
controls, or from reading the keyring itself (e.g. `secret-tool`). Protected
environments and your agent's own permission prompts are the safeguards for
those.

## Code layout

```
main.go            starts the CLI or the UI
internal/core      workspaces, environments, secrets, history, sending,
                   redaction, curl and OpenAPI import (no UI code)
internal/httpfile  the .http format
internal/runner    sending a request: env, captures, expects, history
internal/cli       the `barq <command>` interface
internal/ntui      the Neovim-based UI (with internal/nvimpane)
```

## License

MIT — see [LICENSE](LICENSE).
