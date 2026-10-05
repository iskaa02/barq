package tui

import (
	"fmt"
	"slices"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/iskaa02/barq/internal/core"
)

// resolve fills in the active environment's variables.
func (m Model) resolve(r core.Request) (core.Request, []string) {
	return m.ws.Resolve(m.ws.ActiveEnv, r, nil)
}

// Environment management ----------------------------------------------------

func (m *Model) newEnv(name string) {
	if m.mutate(func(w *core.Workspace) error { return w.UseEnv(w.AddEnv(name, nil)) }) {
		m.openEnvEditor()
	}
}

func (m *Model) switchEnv(id string) {
	if !m.mutate(func(w *core.Workspace) error { return w.UseEnv(id) }) {
		return
	}
	if env := m.ws.CurrentEnv(); env != nil {
		m.flash("environment: " + env.Name)
	} else {
		m.flash("no environment")
	}
}

func (m *Model) renameEnv(id, name string) {
	m.mutate(func(w *core.Workspace) error { return w.RenameEnv(id, name) })
}

func (m *Model) deleteEnv(id string) {
	i := m.ws.FindEnv(id)
	if i < 0 {
		return
	}
	name := m.ws.Environments[i].Name
	if m.mutate(func(w *core.Workspace) error { return w.DeleteEnv(id) }) {
		m.flash("deleted environment “" + name + "”")
	}
}

func (m *Model) duplicateEnv() {
	env := m.ws.CurrentEnv()
	if env == nil {
		return
	}
	id, name := env.ID, env.Name+" copy"
	if m.mutate(func(w *core.Workspace) error {
		src, err := w.Env(id)
		if err != nil {
			return err
		}
		return w.UseEnv(w.AddEnv(name, slices.Clone(src.Vars)))
	}) {
		m.flash("created “" + name + "”")
	}
}

// setVar sets a variable in the active environment, adding it if needed.
func (m *Model) setVar(name, value string) {
	env := m.ws.CurrentEnv()
	if env == nil {
		return
	}
	id := env.ID
	m.mutate(func(w *core.Workspace) error { return w.SetEnvVar(id, name, value) })
}

// Variables editor ------------------------------------------------------------

// envEditor is a popup for editing the active environment's variables.
type envEditor struct {
	envID string
	table headerEditor
}

func (m *Model) openEnvEditor() {
	env := m.ws.CurrentEnv()
	if env == nil {
		m.ask(promptNewEnv, "New environment name:", "dev", 0)
		return
	}
	t := newKVEditor("Variable", "Value", "+ add variable", false)
	t.secrets = true
	t.SetRows(core.FromSavedHeaders(env.Vars))
	t.Focus()
	m.envEdit = &envEditor{envID: env.ID, table: t}
	m.layoutEnvEditor()
}

func (m *Model) closeEnvEditor() {
	e := m.envEdit
	m.envEdit = nil
	if m.ws.FindEnv(e.envID) >= 0 {
		e.table.commit()
		vars := core.ToSavedHeaders(e.table.Rows())
		if m.mutate(func(w *core.Workspace) error { return w.SetEnvVars(e.envID, vars) }) {
			env, _ := m.ws.Env(e.envID)
			m.flash(fmt.Sprintf("saved %d variable(s) in “%s”", len(vars), env.Name))
		}
	}
}

func (m Model) envEditorSize() (w, h int) {
	return min(90, m.width-4), min(18, m.height-4)
}

func (m *Model) layoutEnvEditor() {
	if m.envEdit == nil {
		return
	}
	w, h := m.envEditorSize()
	m.envEdit.table.SetSize(w-4, h-5) // border, padding, title, blank, footer
}

func (m Model) envEditorGeometry() (x, y int) {
	w, _ := m.envEditorSize()
	return (m.width - w) / 2, 2
}

func (m Model) envEditorView() string {
	w, h := m.envEditorSize()
	name := ""
	if i := m.ws.FindEnv(m.envEdit.envID); i >= 0 {
		name = m.ws.Environments[i].Name
	}
	title := titleStyle.Render("Environment: "+name) + mutedStyle.Render("  use as {{variable}} in URL, headers and body")
	if i := m.ws.FindEnv(m.envEdit.envID); i >= 0 && m.ws.Environments[i].Protected {
		title += lipgloss.NewStyle().Foreground(colorYellow).Render("  🔒 protected (" + m.ws.Environments[i].Protection() + ")")
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

func (m Model) updateEnvEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
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

func (m Model) envEditorMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
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

func envItems(m Model) []paletteItem {
	var items []paletteItem
	for _, e := range m.ws.Environments {
		id := e.ID
		hint := fmt.Sprintf("%d vars", len(e.Vars))
		items = append(items, paletteItem{title: e.Name, hint: hint, current: id == m.ws.ActiveEnv,
			run: func(m *Model) tea.Cmd { m.switchEnv(id); return nil }})
	}
	items = append(items,
		paletteItem{title: "No environment", current: m.ws.ActiveEnv == "",
			run: func(m *Model) tea.Cmd { m.switchEnv(""); return nil }},
		paletteItem{title: "+ New environment…",
			run: func(m *Model) tea.Cmd { m.ask(promptNewEnv, "New environment name:", "", 0); return nil }},
	)
	return items
}

var envStep = paletteStep{title: "Switch environment", placeholder: "Environment…", items: envItems}

func (m Model) envCommands() []paletteItem {
	env := m.ws.CurrentEnv()
	cur := "none"
	if env != nil {
		cur = env.Name
	}
	cmd := func(title, hint string, run func(m *Model)) paletteItem {
		return paletteItem{key: "cmd:Environment:" + title, category: "Environment", title: title, hint: hint,
			run: func(m *Model) tea.Cmd { run(m); return nil }}
	}
	items := []paletteItem{
		{key: "cmd:env-switch", category: "Environment", title: "Switch…", hint: cur + "  alt+e", next: &envStep},
		cmd("Edit variables", "alt+v", (*Model).openEnvEditor),
		cmd("New…", "", func(m *Model) { m.ask(promptNewEnv, "New environment name:", "", 0) }),
	}
	if env != nil {
		id, name := env.ID, env.Name
		items = append(items,
			cmd("Rename…", "", func(m *Model) {
				m.ask(promptRenameEnv, "Rename environment to:", name, 0)
				m.prompt.id = id
			}),
			cmd("Duplicate", "", (*Model).duplicateEnv),
			cmd("Delete", "", func(m *Model) {
				m.ask(promptDeleteEnv, fmt.Sprintf("Delete environment “%s”?", name), "", 0)
				m.prompt.id = id
			}),
		)
		if env.Protection() != "writes" {
			items = append(items, cmd("Protect writes (the CLI confirms POST, PUT, …)", "", func(m *Model) { m.setProtected(id, true, false) }))
		}
		if env.Protection() != "all" {
			items = append(items, cmd("Protect all requests (the CLI confirms every send)", "", func(m *Model) { m.setProtected(id, true, true) }))
		}
		if env.Protected {
			items = append(items, cmd("Unprotect", "", func(m *Model) { m.setProtected(id, false, false) }))
		}
	}
	return items
}

// envIndicator is the title bar's environment label.
func (m Model) envIndicator() string {
	if env := m.ws.CurrentEnv(); env != nil {
		lock := ""
		if env.Protected {
			lock = " 🔒"
		}
		warn := ""
		if m.ws.SecretErr != nil {
			warn = lipgloss.NewStyle().Foreground(colorYellow).Render("⚠ secrets in file (no keyring)  ")
		}
		return warn + mutedStyle.Render("env ") + lipgloss.NewStyle().Bold(true).Foreground(colorGreen).Render(env.Name) + lock + " "
	}
	return mutedStyle.Render("no environment ")
}

func (m *Model) setProtected(id string, protect, reads bool) {
	if !m.mutate(func(w *core.Workspace) error {
		env, err := w.Env(id)
		if err == nil {
			env.Protected, env.ProtectReads = protect, protect && reads
		}
		return err
	}) {
		return
	}
	env, _ := m.ws.Env(id)
	switch env.Protection() {
	case "writes":
		m.flash("protected “" + env.Name + "”: the CLI must confirm requests that can change something")
	case "all":
		m.flash("protected “" + env.Name + "”: the CLI must confirm every request")
	default:
		m.flash("unprotected “" + env.Name + "”")
	}
}
