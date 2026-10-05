package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/term"
)

// The CLI does everything the TUI does, for scripts and AI agents. Output
// is plain text by default and JSON with --json. Secrets are never printed:
// see redact.go. Anything that would reveal or risk them (--reveal,
// protected environments) needs a person at an interactive terminal.

type cliCommand struct {
	run     func(c *cli, args []string) error
	summary string
}

var cliCommands map[string]cliCommand

func init() {
	cliCommands = map[string]cliCommand{
		"ls":      {cmdLs, "list folders and requests"},
		"show":    {cmdShow, "show a request (secrets redacted)"},
		"new":     {cmdNew, "create a request"},
		"set":     {cmdSet, "change a request"},
		"mkdir":   {cmdMkdir, "create folders"},
		"mv":      {cmdMv, "move a request or folder"},
		"rename":  {cmdRename, "rename a request or folder"},
		"rm":      {cmdRm, "delete a request or folder"},
		"run":     {cmdRun, "send a request"},
		"history": {cmdHistory, "list or show past runs"},
		"env":     {cmdEnv, "manage environments and variables"},
		"curl":    {cmdCurl, "print a request as curl (secrets stay as {{vars}})"},
		"import":  {cmdImport, "import an OpenAPI 3 spec"},
		"ai":      {cmdAI, "print the guide for AI agents"},
	}
}

func isCLICommand(name string) bool {
	_, ok := cliCommands[name]
	return ok
}

type cli struct {
	in       io.Reader
	out, err io.Writer

	dir     string
	asJSON  bool
	reveal  bool
	ws      *workspace
	rd      redactor
	command string

	// interactive reports whether a person is at a terminal; tests replace it.
	interactive func() bool
}

// usageError makes cliMain exit with status 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// exitCode lets a command choose its exit status (e.g. --fail).
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// cliMain runs a CLI command and returns the process exit status.
func cliMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, ok := cliCommands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "barq: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	c := &cli{in: stdin, out: stdout, err: stderr, command: args[0], interactive: isInteractive}
	err := cmd.run(c, args[1:])
	// No need to warn when the keyring was switched off on purpose.
	if c.ws != nil && c.ws.secretErr != nil && !strings.EqualFold(os.Getenv("BARQ_KEYRING"), "off") {
		fmt.Fprintf(stderr, "barq: warning: %v\n", c.ws.secretErr)
	}
	var code exitCode
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "barq %s: %s\n", c.command, ue.msg)
		return 2
	default:
		fmt.Fprintf(stderr, "barq %s: %v\n", c.command, err)
		return 1
	}
}

// isInteractive reports whether a person is at a terminal (tests swap it).
var isInteractive = stdioIsTerminal

func stdioIsTerminal() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

// confirm shows what is about to happen, with any details, and asks the
// person at the terminal to press y. Agents and scripts have no terminal,
// so they're refused without being asked.
func (c *cli) confirm(what string, details ...string) error {
	if !c.interactive() {
		return fmt.Errorf("%s needs confirmation from a person at an interactive terminal", what)
	}
	var b strings.Builder
	b.WriteString("⚠ " + what + "\n")
	for _, d := range details {
		b.WriteString("  " + d + "\n")
	}
	b.WriteString("Continue? [y/N] ")
	key, err := askKey(b.String(), c.in, c.err)
	if err != nil {
		return err
	}
	if key != 'y' && key != 'Y' {
		return errors.New("cancelled")
	}
	return nil
}

// askKey shows prompt and returns the key pressed (tests replace it).
var askKey = ttyAskKey

// ttyAskKey asks on the controlling terminal rather than stdin and stderr,
// so neither piped input nor redirected output can answer or hide it. One
// keypress is enough; anything but y, including Ctrl-C, cancels. Without
// a /dev/tty (Windows) it falls back to a line on stdin.
func ttyAskKey(prompt string, in io.Reader, errOut io.Writer) (byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprint(errOut, prompt)
		line, _ := bufio.NewReader(in).ReadString('\n')
		if line = strings.TrimSpace(line); line == "" {
			return 0, nil
		}
		return line[0], nil
	}
	defer tty.Close()
	fmt.Fprint(tty, prompt) // before raw mode, so \n still starts a line
	state, err := term.MakeRaw(tty.Fd())
	if err != nil {
		return 0, err
	}
	key := make([]byte, 1)
	_, err = tty.Read(key)
	_ = term.Restore(tty.Fd(), state)
	if key[0] >= ' ' && key[0] < 0x7f {
		fmt.Fprintf(tty, "%c", key[0])
	}
	fmt.Fprint(tty, "\r\n")
	return key[0], err
}

// Flags -------------------------------------------------------------------------

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ", ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// flags makes a flag set with the options every command shares.
func (c *cli) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.dir, "dir", ".", "project directory whose workspace to use")
	fs.BoolVar(&c.asJSON, "json", false, "print JSON")
	return fs
}

// parse parses flags that may come before or after positional arguments,
// and checks the number of positionals.
func (c *cli) parse(fs *flag.FlagSet, args []string, minPos, maxPos int, posNames string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usagef("%v (usage: barq %s %s)", err, c.command, posNames)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) < minPos || (maxPos >= 0 && len(pos) > maxPos) {
		return nil, usagef("usage: barq %s %s", c.command, posNames)
	}
	return pos, nil
}

// open loads the workspace of --dir.
func (c *cli) open() error {
	dir, err := filepath.Abs(c.dir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	ws, err := loadWorkspace(dir)
	if err != nil {
		return err
	}
	c.ws, c.rd = ws, newRedactor(ws)
	return nil
}

// mutate changes the workspace safely, even while the TUI has it open.
func (c *cli) mutate(fn func(*workspace) error) error {
	err := c.ws.mutate(fn)
	c.rd = newRedactor(c.ws)
	return err
}

// Output --------------------------------------------------------------------------

func (c *cli) printf(format string, a ...any) { fmt.Fprintf(c.out, format, a...) }

func (c *cli) printJSON(v any) error {
	enc := json.NewEncoder(c.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

type fieldJSON struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

func fieldsJSON(hs []savedHeader) []fieldJSON {
	out := make([]fieldJSON, 0, len(hs))
	for _, h := range hs {
		out = append(out, fieldJSON{h.Key, h.Value, h.Enabled})
	}
	return out
}

type requestJSON struct {
	ID             string      `json:"id"`
	Path           string      `json:"path"`
	Folder         string      `json:"folder"`
	Name           string      `json:"name"`
	Method         string      `json:"method"`
	URL            string      `json:"url"`
	Headers        []fieldJSON `json:"headers"`
	DisabledParams []fieldJSON `json:"disabled_params,omitempty"`
	BodyMode       string      `json:"body_mode"`
	Body           string      `json:"body,omitempty"`
	Form           []fieldJSON `json:"form,omitempty"`
	Captures       []capture   `json:"captures,omitempty"`
}

// requestOut is a saved request as the CLI shows it, redacted.
func (c *cli) requestOut(r request) requestJSON {
	shown := r
	if !c.reveal {
		shown = c.rd.request(r, true)
	}
	mode := "raw"
	if r.BodyMode == bodyForm {
		mode = "form"
	}
	return requestJSON{
		ID: r.ID, Path: c.ws.slashPath(ref{id: r.ID}), Folder: strings.Join(splitPath(c.ws.folderPath(r.Folder)), "/"),
		Name: r.displayName(), Method: r.Method, URL: shown.URL, Headers: fieldsJSON(shown.Headers),
		DisabledParams: fieldsJSON(shown.DisabledParams), BodyMode: mode, Body: shown.Body,
		Form: fieldsJSON(shown.Form), Captures: r.Captures,
	}
}

func (c *cli) printRequest(r request) {
	j := c.requestOut(r)
	c.printf("%s %s\n", j.Method, j.URL)
	c.printf("id:   %s\npath: %s\n", j.ID, j.Path)
	printFields := func(title string, fs []fieldJSON) {
		if len(fs) == 0 {
			return
		}
		c.printf("%s:\n", title)
		for _, f := range fs {
			off := ""
			if !f.Enabled {
				off = "  (off)"
			}
			c.printf("  %s: %s%s\n", f.Key, f.Value, off)
		}
	}
	printFields("headers", j.Headers)
	printFields("params (off)", j.DisabledParams)
	if j.BodyMode == "form" {
		printFields("form-data", j.Form)
	} else if j.Body != "" {
		c.printf("body:\n%s\n", indent(j.Body, "  "))
	}
	if len(j.Captures) > 0 {
		c.printf("captures:\n")
		for _, cp := range j.Captures {
			c.printf("  %s\n", cp)
		}
	}
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
