package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// An environment is a named set of variables, e.g. "dev" or "prod".
// Requests reference them as {{name}} in the URL, headers and body.
type environment struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	Vars []savedHeader `json:"vars,omitempty"`
	// Protected environments (e.g. production) can't be used from the CLI
	// without confirming in an interactive terminal.
	Protected bool `json:"protected,omitempty"`
}

func (w *workspace) findEnv(id string) int {
	if id == "" {
		return -1
	}
	return slices.IndexFunc(w.Environments, func(e environment) bool { return e.ID == id })
}

func (w *workspace) activeEnv() *environment {
	if i := w.findEnv(w.ActiveEnv); i >= 0 {
		return &w.Environments[i]
	}
	return nil
}

var varPattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.$-]+)\s*\}\}`)

// dynamicVars are built in and produce a fresh value on every send.
var dynamicVars = map[string]func() string{
	"$timestamp":    func() string { return strconv.FormatInt(time.Now().Unix(), 10) },
	"$isoTimestamp": func() string { return time.Now().UTC().Format(time.RFC3339) },
	"$uuid":         newUUID,
	"$randomInt": func() string {
		n, _ := rand.Int(rand.Reader, big.NewInt(1000))
		return n.String()
	},
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// envVars returns an environment's enabled variables ("" for none).
func (w *workspace) envVars(envID string) map[string]string {
	vars := map[string]string{}
	if i := w.findEnv(envID); i >= 0 {
		for _, v := range w.Environments[i].Vars {
			if k := strings.TrimSpace(v.Key); v.Enabled && k != "" {
				vars[k] = v.Value
			}
		}
	}
	return vars
}

// substitute replaces {{name}} references, recording names it can't resolve.
func substitute(s string, vars map[string]string, missing map[string]bool) string {
	return varPattern.ReplaceAllStringFunc(s, func(ref string) string {
		name := varPattern.FindStringSubmatch(ref)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		if f, ok := dynamicVars[name]; ok {
			return f()
		}
		missing[name] = true
		return ref
	})
}

// resolve fills in the active environment's variables.
func (m model) resolve(r request) (request, []string) {
	return m.ws.resolve(m.ws.ActiveEnv, r, nil)
}

// resolve fills in variables from an environment, with overrides taking
// precedence, and reports any that aren't defined.
func (w *workspace) resolve(envID string, r request, overrides map[string]string) (request, []string) {
	vars := w.envVars(envID)
	for k, v := range overrides {
		vars[k] = v
	}
	return resolveVars(r, vars)
}

// resolveVars fills in the given variables and lists the ones missing.
func resolveVars(r request, vars map[string]string) (request, []string) {
	missing := map[string]bool{}
	r.URL = substitute(r.URL, vars, missing)
	r.Body = substitute(r.Body, vars, missing)
	hs := make([]savedHeader, len(r.Headers))
	for i, h := range r.Headers {
		if !h.Enabled {
			hs[i] = h // not sent, so don't complain about its variables
			continue
		}
		hs[i] = savedHeader{
			Key:     substitute(h.Key, vars, missing),
			Value:   substitute(h.Value, vars, missing),
			Enabled: h.Enabled,
		}
	}
	r.Headers = hs
	if len(r.Form) > 0 {
		fs := make([]savedHeader, len(r.Form))
		for i, f := range r.Form {
			if !f.Enabled {
				fs[i] = f
				continue
			}
			fs[i] = savedHeader{Key: substitute(f.Key, vars, missing), Value: substitute(f.Value, vars, missing), Enabled: true}
		}
		r.Form = fs
	}
	names := make([]string, 0, len(missing))
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	return r, names
}

func headersOf(r request) http.Header {
	h := http.Header{}
	for _, sh := range r.Headers {
		if k := strings.TrimSpace(sh.Key); sh.Enabled && k != "" {
			h.Add(k, strings.TrimSpace(sh.Value))
		}
	}
	return h
}

// Environment management ----------------------------------------------------

func (m *model) newEnv(name string) {
	if m.mutate(func(w *workspace) error { return w.useEnv(w.addEnv(name, nil)) }) {
		m.openEnvEditor()
	}
}

func (m *model) switchEnv(id string) {
	if !m.mutate(func(w *workspace) error { return w.useEnv(id) }) {
		return
	}
	if env := m.ws.activeEnv(); env != nil {
		m.flash("environment: " + env.Name)
	} else {
		m.flash("no environment")
	}
}

func (m *model) renameEnv(id, name string) {
	m.mutate(func(w *workspace) error { return w.renameEnv(id, name) })
}

func (m *model) deleteEnv(id string) {
	i := m.ws.findEnv(id)
	if i < 0 {
		return
	}
	name := m.ws.Environments[i].Name
	if m.mutate(func(w *workspace) error { return w.deleteEnv(id) }) {
		m.flash("deleted environment “" + name + "”")
	}
}

func (m *model) duplicateEnv() {
	env := m.ws.activeEnv()
	if env == nil {
		return
	}
	id, name := env.ID, env.Name+" copy"
	if m.mutate(func(w *workspace) error {
		src, err := w.env(id)
		if err != nil {
			return err
		}
		return w.useEnv(w.addEnv(name, slices.Clone(src.Vars)))
	}) {
		m.flash("created “" + name + "”")
	}
}

// setVar sets a variable in the active environment, adding it if needed.
func (m *model) setVar(name, value string) {
	env := m.ws.activeEnv()
	if env == nil {
		return
	}
	id := env.ID
	m.mutate(func(w *workspace) error { return w.setEnvVar(id, name, value) })
}

// Variables editor ------------------------------------------------------------

// envEditor is a popup for editing the active environment's variables.
type envEditor struct {
	envID string
	table headerEditor
}

func (m *model) openEnvEditor() {
	env := m.ws.activeEnv()
	if env == nil {
		m.ask(promptNewEnv, "New environment name:", "dev", 0)
		return
	}
	t := newKVEditor("Variable", "Value", "+ add variable", false)
	t.secrets = true
	t.SetRows(fromSavedHeaders(env.Vars))
	t.Focus()
	m.envEdit = &envEditor{envID: env.ID, table: t}
	m.layoutEnvEditor()
}

func (m *model) closeEnvEditor() {
	e := m.envEdit
	m.envEdit = nil
	if m.ws.findEnv(e.envID) >= 0 {
		e.table.commit()
		vars := toSavedHeaders(e.table.Rows())
		if m.mutate(func(w *workspace) error { return w.setEnvVars(e.envID, vars) }) {
			env, _ := m.ws.env(e.envID)
			m.flash(fmt.Sprintf("saved %d variable(s) in “%s”", len(vars), env.Name))
		}
	}
}

func (m model) envEditorSize() (w, h int) {
	return min(90, m.width-4), min(18, m.height-4)
}

func (m *model) layoutEnvEditor() {
	if m.envEdit == nil {
		return
	}
	w, h := m.envEditorSize()
	m.envEdit.table.SetSize(w-4, h-5) // border, padding, title, blank, footer
}

func (m model) envEditorGeometry() (x, y int) {
	w, _ := m.envEditorSize()
	return (m.width - w) / 2, 2
}

func (m model) envEditorView() string {
	w, h := m.envEditorSize()
	name := ""
	if i := m.ws.findEnv(m.envEdit.envID); i >= 0 {
		name = m.ws.Environments[i].Name
	}
	title := titleStyle.Render("Environment: "+name) + mutedStyle.Render("  use as {{variable}} in URL, headers and body")
	if i := m.ws.findEnv(m.envEdit.envID); i >= 0 && m.ws.Environments[i].Protected {
		title += lipgloss.NewStyle().Foreground(colorYellow).Render("  🔒 protected")
	}
	footer := mutedStyle.Render("enter next cell • ctrl+l secret • ctrl+t toggle • ctrl+d delete • esc/ctrl+s save & close")
	body := lipgloss.JoinVertical(lipgloss.Left, title, "", m.envEdit.table.View())
	body = lipgloss.NewStyle().Height(h - 3).Render(body)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(w - 2).
		Render(body + "\n" + footer)
}

func (m model) updateEnvEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "ctrl+s", "ctrl+c":
			if !m.envEdit.table.CloseSuggestions() {
				m.closeEnvEditor()
			}
			return m, nil
		case "tab", "shift+tab":
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.envEdit.table, cmd = m.envEdit.table.Update(msg)
	return m, cmd
}

func (m model) envEditorMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	x, y := m.envEditorGeometry()
	w, h := m.envEditorSize()
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.envEdit.table.Wheel(-1)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.envEdit.table.Wheel(1)
		return m, nil
	case tea.MouseButtonLeft:
	default:
		return m, nil
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	if msg.X < x || msg.X >= x+w || msg.Y < y || msg.Y >= y+h {
		m.closeEnvEditor()
		return m, nil
	}
	// Table starts after the border, title and blank line.
	m.envEdit.table.Click(msg.X-x-innerX, msg.Y-y-3)
	return m, nil
}

// Palette -------------------------------------------------------------------

func envItems(m model) []paletteItem {
	var items []paletteItem
	for _, e := range m.ws.Environments {
		id := e.ID
		hint := fmt.Sprintf("%d vars", len(e.Vars))
		items = append(items, paletteItem{title: e.Name, hint: hint, current: id == m.ws.ActiveEnv,
			run: func(m *model) tea.Cmd { m.switchEnv(id); return nil }})
	}
	items = append(items,
		paletteItem{title: "No environment", current: m.ws.ActiveEnv == "",
			run: func(m *model) tea.Cmd { m.switchEnv(""); return nil }},
		paletteItem{title: "+ New environment…",
			run: func(m *model) tea.Cmd { m.ask(promptNewEnv, "New environment name:", "", 0); return nil }},
	)
	return items
}

var envStep = paletteStep{title: "Switch environment", placeholder: "Environment…", items: envItems}

func (m model) envCommands() []paletteItem {
	env := m.ws.activeEnv()
	cur := "none"
	if env != nil {
		cur = env.Name
	}
	cmd := func(title, hint string, run func(m *model)) paletteItem {
		return paletteItem{key: "cmd:Environment:" + title, category: "Environment", title: title, hint: hint,
			run: func(m *model) tea.Cmd { run(m); return nil }}
	}
	items := []paletteItem{
		{key: "cmd:env-switch", category: "Environment", title: "Switch…", hint: cur + "  alt+e", next: &envStep},
		cmd("Edit variables", "alt+v", (*model).openEnvEditor),
		cmd("New…", "", func(m *model) { m.ask(promptNewEnv, "New environment name:", "", 0) }),
	}
	if env != nil {
		id, name := env.ID, env.Name
		items = append(items,
			cmd("Rename…", "", func(m *model) {
				m.ask(promptRenameEnv, "Rename environment to:", name, 0)
				m.prompt.id = id
			}),
			cmd("Duplicate", "", (*model).duplicateEnv),
			cmd(protectTitle(env.Protected), "", func(m *model) { m.setProtected(id, !env.Protected) }),
			cmd("Delete", "", func(m *model) {
				m.ask(promptDeleteEnv, fmt.Sprintf("Delete environment “%s”?", name), "", 0)
				m.prompt.id = id
			}),
		)
	}
	return items
}

// envIndicator is the title bar's environment label.
func (m model) envIndicator() string {
	if env := m.ws.activeEnv(); env != nil {
		lock := ""
		if env.Protected {
			lock = " 🔒"
		}
		warn := ""
		if m.ws.secretErr != nil {
			warn = lipgloss.NewStyle().Foreground(colorYellow).Render("⚠ secrets in file (no keyring)  ")
		}
		return warn + mutedStyle.Render("env ") + lipgloss.NewStyle().Bold(true).Foreground(colorGreen).Render(env.Name) + lock + " "
	}
	return mutedStyle.Render("no environment ")
}

func protectTitle(protected bool) string {
	if protected {
		return "Unprotect"
	}
	return "Protect (the CLI must then confirm)"
}

func (m *model) setProtected(id string, protect bool) {
	if !m.mutate(func(w *workspace) error {
		env, err := w.env(id)
		if err == nil {
			env.Protected = protect
		}
		return err
	}) {
		return
	}
	env, _ := m.ws.env(id)
	if protect {
		m.flash("protected “" + env.Name + "”: the CLI must confirm before using it")
	} else {
		m.flash("unprotected “" + env.Name + "”")
	}
}
