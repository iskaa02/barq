package cli

// cmdAI prints the guide for AI agents: the .http format, the commands and how secrets work.
func cmdAI(c *cli, args []string) error {
	if _, err := c.parse(c.flags("ai"), args, 0, 0, ""); err != nil {
		return err
	}
	c.printf("%s", aiGuide)
	return nil
}

const aiGuide = `# barq — API client for agents

barq sends the requests written in a project's .http files. The requests are
plain text in the repo: you edit the .http files directly (there are no
commands to create, change or delete requests), and run them with barq
instead of hand-written curl. A human may have the barq UI open on the same
files; it picks up your edits.

## The .http format
One or more requests per file, separated by lines starting with ###:

  ### login                        <- separator; the text is the request's name
  # @capture token = .data.accessToken
  # @capture rid = header X-Request-Id   <- response header (case-insensitive)
  # @capture sid = cookie session_id     <- cookie from Set-Cookie
  # @expect status 200
  # @expect jq .data.ok              <- passes when the jq result is truthy
  # @confirm                         <- ask a person before sending
  POST {{baseUrl}}/login           <- METHOD URL (just a URL means GET)
  Content-Type: application/json   <- headers, until the first blank line
  # X-Debug: 1                       <- "# Key: Value" among headers = disabled
  ## note                            <- "##" lines are ignored

  {"user": "x", "password": "{{password}}"}   <- body: the rest of the block

- Name: text after ### (or "# @name x" before the request line).
- Other "# …" and "// …" lines before the request line are comments.
- Body "< ./payload.json" (the only line of the body) sends that file; the
  path is relative to the project directory (where barq runs).
- File uploads: with "Content-Type: multipart/form-data" the body is one
  "name: value" field per line; "avatar: @./images/me.png" sends a file
  (path relative to the project directory), "# name: v" is disabled. Don't
  write a boundary; barq adds it.
- {{var}} comes from the active (or --env) environment. Built in:
  {{$uuid}} {{$timestamp}} {{$isoTimestamp}} {{$randomInt}}.
- @capture stores a jq result of each successful (< 400) response in an
  environment variable, e.g. a token for the next request. "header Name"
  or "cookie name" instead of jq captures a response header / Set-Cookie
  value.
- @expect status N / @expect jq F make "barq run" exit with status 4 when
  they fail, so .http files double as API tests.

## Referring to a request
  api.http#login      file (relative to the project) and name
  api.http#2          the 2nd request of the file, for unnamed ones
  api.http            the file's first request
  login               a bare name, when only one request has it
"barq ls" prints every ref. Run barq from the project directory or use --dir.

## Secrets: you will never see them, and that's intended
- Secret variables (tokens, passwords, keys) are stored in the OS keyring.
  Refer to them as {{name}} in .http files; barq fills them in when sending.
- All output is redacted: secret values show as «redacted:name», and
  credential headers / JSON fields / query params as «redacted».
- Never paste real tokens into .http files. Capture them from responses
  (@capture) or ask the user to set them:
  barq env set <env> token - --secret   (value read from stdin)
- Don't try --reveal; it needs a person at a terminal.
- Protected environments (often production) need a human to confirm in a
  terminal. GET, HEAD and OPTIONS usually go through; anything that can
  change something fails for you, and so does everything in environments
  protected with --all. Use another environment or ask. Requests marked
  @confirm need a person too, unless you pass --yes (protected
  environments still need one).

## Commands  (all accept --dir <project> and --json)
  barq ls                              every request: ref, method, URL
  barq show <ref> [--env E]            the request's text and its resolved URL
  barq run <ref> [--env E] [--var k=v]… [--capture var=jq]… [--jq F] [-i] [-o FILE] [--fail] [--yes]
  barq curl <ref> [--env E]            curl with secrets kept as {{vars}}
  barq history [ref] [-n N]  ·  barq history show <run-id>
  barq history body <run-id> [--jq F | --grep RE | --lines A:B | --bytes A:B | --path]
  barq env ls | show [E] | use E|none | new E [--use] [--protect|--protect-all] | set E KEY VALUE|- [--secret]
  barq env unset E KEY | rename E NAME | rm E | protect E [--all]
  barq import <openapi.json|yaml|url> [--dry-run]   writes requests/<tag>.http files
  barq import --saved                  writes the old saved requests as .http files
  barq                                 with no arguments, opens the UI

## Workflow
  1. barq ls                           find or add the request in a .http file
  2. barq run api.http#login           {{token}} is captured (secret, not shown)
  3. barq run api.http#list-orders     uses Authorization: Bearer {{token}}
If a request returns 401, the token has probably expired: run the login
request again, then retry. Exit status 4 means an @expect failed.

## Large responses
barq keeps every body whole in history, but run and history show print at
most 1 MB of it. When they cut a body, --json has "partial": true and
"size" is the whole size. Don't print large bodies whole; read what you need:
  barq history body <run-id> --jq '.items | length'     (bodies up to 128 MB)
  barq history body <run-id> --grep '"status": *"failed"'   numbered matches
  barq history body <run-id> --lines 1:50   ·   --bytes 0:4096
  barq history body <run-id> --path         the file, already redacted, for
                                            your own tools (jq, rg, head)
  barq run <ref> -o out.json                save the whole body (redacted)
--jq on run reads the whole body too. Old bodies may be pruned from history
to save space; the run stays and says so ("body_pruned").

## Output
Human-readable by default; --json for structured output:
  run:  {run_id, request, status, code, duration_ms, size, headers, body, captured[],
         expects, failed_expects[], redacted, partial, body_file}
  ls:   [{ref, path, name, index, method, url}]
Exit status: 0 ok, 1 error, 2 bad usage, 3 HTTP >= 400 with --fail, 4 an @expect failed.
`
