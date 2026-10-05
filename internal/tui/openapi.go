package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/iskaa02/barq/internal/core"
)

// In-app import -----------------------------------------------------------------

type specLoadedMsg struct {
	src  string
	spec *core.Omap
	err  error
}

// loadSpecCmd reads a spec in the background; URLs can take a while.
func (m *Model) loadSpecCmd(src string) tea.Cmd {
	if rest, ok := strings.CutPrefix(src, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			src = filepath.Join(home, rest)
		}
	}
	if !strings.Contains(src, "://") && !filepath.IsAbs(src) {
		src = filepath.Join(m.ws.CWD, src)
	}
	m.flash("loading " + src + "…")
	return func() tea.Msg {
		spec, err := core.LoadSpec(src)
		return specLoadedMsg{src: src, spec: spec, err: err}
	}
}

func (m *Model) finishImport(msg specLoadedMsg) {
	if msg.err != nil {
		m.notice = errorStyle.Render("import failed: " + msg.err.Error())
		return
	}
	var res core.ImportResult
	if !m.mutate(func(w *core.Workspace) (err error) {
		if res, err = core.ImportOpenAPI(w, msg.spec); err != nil {
			return fmt.Errorf("import failed: %w", err)
		}
		return nil
	}) {
		return
	}
	m.revealFolder(res.FolderID)
	note := ""
	if n := len(res.Multipart); n > 0 {
		note = fmt.Sprintf(" — %d upload request(s) need @ file paths", n)
	}
	m.flash(fmt.Sprintf("imported “%s”: %s; environments: %s%s",
		res.Title, res.Summary(), strings.Join(res.Envs, ", "), note))
}
