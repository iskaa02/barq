package cli

const Usage = `barq — an API client for the terminal

Usage:
  barq                         open the UI (needs nvim 0.10+)
  barq [url | "curl …"]        add the request to scratch.http and open the UI
  barq <command> [flags]       run requests from scripts and AI agents

Requests live in .http files in the project:

  ### list users
  GET {{baseUrl}}/users

Refer to one as file.http#name, file.http#n, or just its name when unique.

Commands:
  ls, show                  list requests, show one with its resolved URL
  run, curl, history        send a request, print it as curl, past runs
  env                       environments and variables
  import                    import an OpenAPI 3 spec (or --saved: the old store)
  ai                        the full guide for AI agents

Environments and history are per directory, stored in ~/.barq/workspaces.
Secret values live in the OS keyring and are never printed.
`
