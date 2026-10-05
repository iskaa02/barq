package cli

const Usage = `barq — an API client for the terminal

Usage:
  barq [url | "curl …"]        open the TUI for this directory's workspace
  barq <command> [flags]       do the same from scripts and AI agents

Commands:
  ls, show, new, set, mkdir, mv, rename, rm     requests and folders
  run, history                                  send requests, past runs
  env                                           environments and variables
  curl                                          print a request as curl
  import                                        import an OpenAPI 3 spec
  ai                                            the full guide for AI agents

Workspaces are per directory and stored in ~/.barq/workspaces.
Secret values live in the OS keyring and are never printed.
`
