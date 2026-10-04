package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type promptKind int

const (
	promptSaveAs promptKind = iota
	promptRename
	promptRenameDraft
	promptDelete
	promptCloseTab
	promptNewFolder
	promptRenameFolder
	promptDeleteFolder
	promptSaveResponse
	promptNewEnv
	promptRenameEnv
	promptDeleteEnv
	promptSetVar
	promptClearHistory
	promptImportSpec
	promptAddCapture
)

// prompt is a one-line question shown in the help bar: either a text input
// or a yes/no confirmation.
type prompt struct {
	kind   promptKind
	label  string
	input  textinput.Model
	id     string // saved request the prompt acts on
	tabUID int    // tab the prompt acts on
}

func (p prompt) confirm() bool {
	return p.kind == promptDelete || p.kind == promptCloseTab || p.kind == promptDeleteFolder ||
		p.kind == promptDeleteEnv || p.kind == promptClearHistory
}

func (m *model) ask(kind promptKind, label, value string, tabUID int) {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 500
	in.Width = 40
	in.SetValue(value)
	in.CursorEnd()
	in.Focus()
	m.prompt = &prompt{kind: kind, label: label, input: in, id: m.cur().savedID, tabUID: tabUID}
}

func (p prompt) view() string {
	label := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render(p.label)
	if p.confirm() {
		return label + mutedStyle.Render("  y/n")
	}
	return label + " " + p.input.View() + mutedStyle.Render("  enter ok • esc cancel")
}

func (m model) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.prompt
	if p.confirm() {
		switch strings.ToLower(msg.String()) {
		case "y", "enter":
			m.prompt = nil
			switch p.kind {
			case promptDelete:
				m.deleteSaved(p.id)
			case promptDeleteFolder:
				m.deleteFolder(p.id)
			case promptDeleteEnv:
				m.deleteEnv(p.id)
			case promptClearHistory:
				key := m.histKey(m.cur())
				_ = m.hist.remove(func(h histMeta) bool { return h.Key == key })
				m.cur().viewing, m.cur().histSel = nil, 0
				m.refreshResponse()
			case promptCloseTab:
				if i := m.tabIndex(p.tabUID); i >= 0 {
					m.closeTab(i, true)
				}
			}
		case "n", "esc", "ctrl+c":
			m.prompt = nil
		}
		return m, nil
	}

	switch msg.String() {
	case "esc", "ctrl+c":
		m.prompt = nil
		return m, nil
	case "enter":
		m.prompt = nil
		name := strings.TrimSpace(p.input.Value())
		if name == "" {
			return m, nil
		}
		switch p.kind {
		case promptSaveAs:
			m.saveAs(name, p.id)
		case promptNewFolder:
			m.newFolder(p.id, name)
		case promptRenameFolder:
			m.renameFolder(p.id, name)
		case promptSaveResponse:
			m.saveResponse(name)
		case promptNewEnv:
			m.newEnv(name)
		case promptRenameEnv:
			m.renameEnv(p.id, name)
		case promptSetVar:
			m.setVarFromResponse(name)
		case promptAddCapture:
			m.addCapture(name)
		case promptImportSpec:
			return m, m.loadSpecCmd(name)
		case promptRename:
			m.renameSaved(p.id, name)
		case promptRenameDraft:
			m.cur().req.Name = name
			m.persist()
		}
		return m, nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return m, cmd
}
