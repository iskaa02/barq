package core

type HeaderRow struct {
	Key, Value string
	Enabled    bool
	Secret     *bool // environment variables only; see savedHeader.Secret
}

func (r HeaderRow) Empty() bool { return r.Key == "" && r.Value == "" }

// Tabs ----------------------------------------------------------------------

func ToSavedHeaders(rows []HeaderRow) []SavedHeader {
	var out []SavedHeader
	for _, r := range rows {
		out = append(out, SavedHeader{Key: r.Key, Value: r.Value, Enabled: r.Enabled, Secret: r.Secret})
	}
	return out
}

func FromSavedHeaders(hs []SavedHeader) []HeaderRow {
	var out []HeaderRow
	for _, h := range hs {
		out = append(out, HeaderRow{Key: h.Key, Value: h.Value, Enabled: h.Enabled, Secret: h.Secret})
	}
	return out
}
