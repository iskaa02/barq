package main

// cmdAI prints the guide for AI agents: what barq is, how to use it from
// the command line, and how secrets work.
func cmdAI(c *cli, args []string) error {
	if _, err := c.parse(c.flags("ai"), args, 0, 0, ""); err != nil {
		return err
	}
	c.printf("%s", aiGuide)
	return nil
}

const aiGuide = `# barq — API client CLI for agents

barq keeps saved HTTP requests, folders, environments and run history per
project directory (in ~/.barq, not in the project). Use it to call and test
APIs instead of hand-written curl. A human may have the barq TUI open on the
same project; your changes appear there within a second.

## Secrets: you will never see them, and that's intended
- Secret variables (tokens, passwords, keys) are stored in the OS keyring.
  Refer to them as {{name}}; barq fills them in when sending.
- All output is redacted: secret values show as «redacted:name», and
  credential headers / JSON fields / query params as «redacted».
- Never paste real tokens into requests. Capture them from responses
  instead (see captures) or ask the user to set them:
  barq env set <env> token - --secret   (value read from stdin)
- When editing a body that shows «redacted», keep the marker as is: barq
  restores the real value. Don't try --reveal; it needs a human.
- Protected environments (often production) need a human to confirm in a
  terminal; runs there fail for you. Use another environment or ask.

## References
Requests and folders are named by ID or path, e.g. "Auth/Login". Paths
match case-insensitively and by unique substring. Prefer IDs from --json.

## Commands  (all accept --dir <project> and --json)
  barq ls [folder]                     tree of folders and requests
  barq show <request>                  method, URL, headers, body, captures
  barq new <folder/…/name> [edit flags] create (folders are created as needed)
  barq new <folder/…/name> --from <request> [edit flags]   copy, then change
  barq set <request> [edit flags]      change a request
  barq mkdir <folder/…>  ·  barq mv <ref> <folder or />  ·  barq rename <ref> <name>
  barq rm <request>  ·  barq rm -r <folder>
  barq run <request> [--env E] [--var k=v]… [--capture var=jq]… [--jq F] [-i] [--fail]
  barq run --curl 'curl …'             send without saving
  barq history [request] [-n N]  ·  barq history show <run-id>
  barq env ls | show [E] | use E|none | new E [--use] | set E KEY VALUE|- [--secret]
  barq env unset E KEY | rename E NAME | rm E | protect E
  barq curl <request> [--env E]        curl with secrets kept as {{vars}}
  barq import <openapi.json|yaml|url> [--dry-run]

Edit flags (new/set):
  --method M  --url U  -H 'Key: value' (repeatable, replaces same key)
  --param k=v  --body TEXT|@file|-  --form k=v  --form file=@path/to/file
  --body-mode raw|form  --curl 'curl …'  --capture var=jq-filter
  set only: --name N  --unset-header K  --unset-param k  --unset-form k  --unset-capture var
Form file paths are relative to the project directory, or absolute.

## Variables and captures
URLs, headers, params and bodies may use {{var}} from the active (or --env)
environment, plus {{$uuid}} {{$timestamp}} {{$isoTimestamp}} {{$randomInt}}.
A capture stores part of each successful response in a variable:
  barq set "Auth/Login" --capture token=.data.accessToken
  barq run "Auth/Login"            # now {{token}} is set (secret, not shown)
  barq run "Orders/List orders"    # uses Authorization: Bearer {{token}}
If a request returns 401, the token has probably expired: run the login
request again (its capture refreshes {{token}}), then retry.

## Output
Human-readable by default; --json for structured output:
  run:  {run_id, status, code, duration_ms, size, headers, body, captured[], redacted}
  show/new/set: {id, path, method, url, headers[], disabled_params[], body_mode, body, form[], captures[]}
Exit status: 0 ok, 1 error, 2 bad usage, 3 HTTP >= 400 with --fail.
`
