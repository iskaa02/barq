package cli

const Usage = `barq — an API client for the terminal

Usage:
  barq                         open the UI (needs nvim 0.10+)
  barq [url | "curl …"]        add the request to .barq/scratch.http and open the UI
  barq <command> [flags]       run requests from scripts and AI agents

Requests are .http files, stored per project in ~/.barq/requests/ (see
"barq dir"); .http files in the project folder are picked up too:

  ### list users
  GET {{baseUrl}}/users

Refer to one as file.http#name, file.http#n, or just its name when unique.
Files in the store have a .barq/ prefix: .barq/api.http#name.

Commands:
  ls, show, dir             list requests, show one with its resolved URL, the store path
  run, curl, history        send a request, print it as curl, past runs
  env                       environments and variables
  import                    import an OpenAPI 3 spec (or --saved: the old store)
  ai                        the full guide for AI agents

Environments and history are per directory, stored in ~/.barq/workspaces.
Secret values live in the OS keyring and are never printed.
`
