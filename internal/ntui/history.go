package ntui

import "github.com/iskaa02/barq/internal/core"

// showHistory fills the History view with the current request's past runs.
func (a *App) showHistory() {
	respState.runs = nil
	if respState.key != "" {
		if h, err := core.OpenHistory(a.ws); err == nil {
			respState.runs = h.ForKey(respState.key)
		}
	}
	if _, err := a.rp.SetScratch(viewBufs[viewHistory], "text", histLines(respState.runs)); err != nil {
		a.flashErr(err.Error())
	}
}

// histAction handles <CR> (show the run) and d (diff it with the current
// response) on row of the History view.
func (a *App) histAction(act string, row int) {
	if row < 0 || row >= len(respState.runs) {
		return
	}
	h, err := core.OpenHistory(a.ws)
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	id := respState.runs[row].ID
	switch act {
	case "show":
		e, err := h.Load(id)
		if err != nil {
			a.flashErr(err.Error())
			return
		}
		a.shown = respFromRun(e, a.cwd)
		respState.view = viewBody
		a.showView()
	case "diff":
		cur := a.currentResponse()
		if cur == nil || cur.RunID == id {
			a.flash("nothing else to diff against")
			return
		}
		a.diffWith(h, id, cur)
	}
}

// currentResponse is the newest response of the key in History: the one
// sent in this session, else the newest stored run.
func (a *App) currentResponse() *Response {
	if rs := a.LastResponses(respState.key); len(rs) > 0 {
		return rs[len(rs)-1]
	}
	if len(respState.runs) == 0 {
		return nil
	}
	h, err := core.OpenHistory(a.ws)
	if err != nil {
		return nil
	}
	e, err := h.Load(respState.runs[0].ID)
	if err != nil {
		return nil
	}
	return respFromRun(e, a.cwd)
}
