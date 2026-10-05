package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

// OpenAPI 3 import. The spec becomes a folder of saved requests (one
// subfolder per tag) plus an environment per server. Specs are decoded
// into an order-preserving tree so generated bodies list properties in the
// order the spec defines them.

// omap is a JSON/YAML object that remembers key order.
type omap struct {
	keys []string
	vals map[string]any
}

func (o *omap) get(k string) any {
	if o == nil {
		return nil
	}
	return o.vals[k]
}

func (o *omap) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func newOmap() *omap { return &omap{vals: map[string]any{}} }

// Decoding --------------------------------------------------------------------

func decodeOrderedJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var read func() (any, error)
	read = func() (any, error) {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				o := newOmap()
				for dec.More() {
					kt, err := dec.Token()
					if err != nil {
						return nil, err
					}
					v, err := read()
					if err != nil {
						return nil, err
					}
					o.set(kt.(string), v)
				}
				_, err := dec.Token() // }
				return o, err
			case '[':
				var arr []any
				for dec.More() {
					v, err := read()
					if err != nil {
						return nil, err
					}
					arr = append(arr, v)
				}
				_, err := dec.Token() // ]
				return arr, err
			}
		}
		return tok, nil
	}
	return read()
}

func fromYAML(n *yaml.Node) any {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) > 0 {
			return fromYAML(n.Content[0])
		}
		return nil
	case yaml.AliasNode:
		return fromYAML(n.Alias)
	case yaml.MappingNode:
		o := newOmap()
		for i := 0; i+1 < len(n.Content); i += 2 {
			o.set(n.Content[i].Value, fromYAML(n.Content[i+1]))
		}
		return o
	case yaml.SequenceNode:
		arr := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			arr = append(arr, fromYAML(c))
		}
		return arr
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return n.Value
	}
	if f, ok := v.(float64); ok {
		return json.Number(strconv.FormatFloat(f, 'f', -1, 64))
	}
	if i, ok := v.(int); ok {
		return json.Number(strconv.Itoa(i))
	}
	return v
}

// loadSpec reads an OpenAPI document from a file or an http(s) URL, as JSON
// or YAML.
func loadSpec(src string) (*omap, error) {
	var data []byte
	var err error
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		client := &http.Client{Timeout: 30 * time.Second}
		res, err := client.Get(src)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			return nil, fmt.Errorf("fetching %s: %s", src, res.Status)
		}
		data, err = io.ReadAll(io.LimitReader(res.Body, 64<<20))
		if err != nil {
			return nil, err
		}
	} else if data, err = os.ReadFile(src); err != nil {
		return nil, err
	}

	var doc any
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		doc, err = decodeOrderedJSON(data)
	} else {
		var n yaml.Node
		if err = yaml.Unmarshal(data, &n); err == nil {
			doc = fromYAML(&n)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", src, err)
	}
	spec, ok := doc.(*omap)
	if !ok {
		return nil, errors.New("not an OpenAPI document")
	}
	if v, _ := spec.get("openapi").(string); !strings.HasPrefix(v, "3") {
		if spec.get("swagger") != nil {
			return nil, errors.New("this is a Swagger 2.0 spec; only OpenAPI 3.x is supported")
		}
		return nil, errors.New(`not an OpenAPI 3 document (no "openapi: 3.x" field)`)
	}
	return spec, nil
}

// Walking the spec ------------------------------------------------------------

type specWalker struct{ root *omap }

// resolve follows a local "$ref" (e.g. #/components/schemas/User).
func (s specWalker) resolve(v any) *omap {
	o, _ := v.(*omap)
	for depth := 0; o != nil && depth < 20; depth++ {
		ref, _ := o.get("$ref").(string)
		if ref == "" {
			return o
		}
		o = s.pointer(ref)
	}
	return o
}

func (s specWalker) pointer(ref string) *omap {
	path, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil // external refs aren't supported
	}
	cur := any(s.root)
	for _, part := range strings.Split(path, "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		o, ok := cur.(*omap)
		if !ok {
			return nil
		}
		cur = o.get(part)
	}
	o, _ := cur.(*omap)
	return o
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// sample builds an example value for a schema: its example, default or
// first enum value, otherwise a placeholder of the right type.
func (s specWalker) sample(schemaV any, depth int) any {
	sc := s.resolve(schemaV)
	if sc == nil || depth > 8 {
		return nil
	}
	for _, k := range []string{"example", "default"} {
		if v := sc.get(k); v != nil {
			return v
		}
	}
	if enum, ok := sc.get("enum").([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	if all, ok := sc.get("allOf").([]any); ok {
		merged := newOmap()
		for _, part := range all {
			if o, ok := s.sample(part, depth+1).(*omap); ok {
				for _, k := range o.keys {
					merged.set(k, o.vals[k])
				}
			}
		}
		return merged
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if alts, ok := sc.get(k).([]any); ok && len(alts) > 0 {
			return s.sample(alts[0], depth+1)
		}
	}

	typ, _ := sc.get("type").(string)
	if typ == "" && sc.get("properties") != nil {
		typ = "object"
	}
	switch typ {
	case "object":
		o := newOmap()
		if props := s.resolve(sc.get("properties")); props != nil {
			for _, k := range props.keys {
				o.set(k, s.sample(props.vals[k], depth+1))
			}
		}
		return o
	case "array":
		return []any{s.sample(sc.get("items"), depth+1)}
	case "integer", "number":
		if minV, ok := sc.get("minimum").(json.Number); ok {
			n, _ := minV.Float64()
			if b, _ := sc.get("exclusiveMinimum").(bool); b {
				n++
			}
			return json.Number(strconv.FormatFloat(n, 'f', -1, 64))
		}
		return json.Number("0")
	case "boolean":
		return false
	case "string":
		switch f, _ := sc.get("format").(string); f {
		case "date-time":
			return "2026-01-01T00:00:00Z"
		case "date":
			return "2026-01-01"
		case "email":
			return "user@example.com"
		case "uuid":
			return "00000000-0000-0000-0000-000000000000"
		case "uri", "url":
			return "https://example.com"
		case "binary", "byte":
			return ""
		}
		return "string"
	}
	return nil
}

// writeJSON encodes a sample with two-space indentation, keeping key order.
func writeJSON(b *strings.Builder, v any, indent string) {
	switch x := v.(type) {
	case *omap:
		if len(x.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range x.keys {
			kb, _ := json.Marshal(k)
			b.WriteString(indent + "  " + string(kb) + ": ")
			writeJSON(b, x.vals[k], indent+"  ")
			if i < len(x.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range x {
			b.WriteString(indent + "  ")
			writeJSON(b, e, indent+"  ")
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	case json.Number:
		b.WriteString(x.String())
	default:
		enc, err := json.Marshal(x)
		if err != nil {
			enc = []byte("null")
		}
		b.WriteString(string(enc))
	}
}

// paramValue picks a parameter's example value, or "" when there's none.
func (s specWalker) paramValue(p *omap) string {
	if v := p.get("example"); v != nil {
		return str(v)
	}
	if ex := s.resolve(p.get("examples")); ex != nil && len(ex.keys) > 0 {
		if e := s.resolve(ex.vals[ex.keys[0]]); e != nil {
			return str(e.get("value"))
		}
	}
	sc := s.resolve(p.get("schema"))
	for _, k := range []string{"example", "default"} {
		if v := sc.get(k); v != nil {
			return str(v)
		}
	}
	if enum, ok := sc.get("enum").([]any); ok && len(enum) > 0 {
		return str(enum[0])
	}
	return ""
}

// Import ------------------------------------------------------------------------

type importResult struct {
	Title     string
	FolderID  string
	Added     int
	Skipped   int // already imported earlier
	Folders   int
	Envs      []string
	Multipart []string // file-upload operations, whose @ paths need filling in
}

var serverVar = regexp.MustCompile(`\{([^}]+)\}`)

var specMethods = []string{"get", "post", "put", "patch", "delete", "head", "options", "trace"}

// importOpenAPI adds a spec's operations to the workspace. Operations that
// were imported before (same method and path) are left as they are, so the
// import can be re-run after the spec changes without losing edits.
func importOpenAPI(ws *workspace, spec *omap) (importResult, error) {
	s := specWalker{root: spec}
	info := s.resolve(spec.get("info"))
	res := importResult{Title: strings.TrimSpace(str(info.get("title")))}
	if res.Title == "" {
		res.Title = "Imported API"
	}
	paths := s.resolve(spec.get("paths"))
	if paths == nil || len(paths.keys) == 0 {
		return res, errors.New("the spec has no paths")
	}

	// Environments, one per server.
	schemes := s.resolve(s.resolve(spec.get("components")).get("securitySchemes"))
	servers, _ := spec.get("servers").([]any)
	for i, sv := range servers {
		so := s.resolve(sv)
		u := str(so.get("url"))
		vars := s.resolve(so.get("variables"))
		u = serverVar.ReplaceAllStringFunc(u, func(m string) string {
			if d := s.resolve(vars.get(m[1 : len(m)-1])); d != nil {
				return str(d.get("default"))
			}
			return m
		})
		name := strings.TrimSpace(str(so.get("description")))
		if name == "" {
			name = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		}
		if name == "" {
			name = fmt.Sprintf("server %d", i+1)
		}
		ensureEnv(ws, name, savedHeader{Key: "baseUrl", Value: strings.TrimSuffix(u, "/"), Enabled: true}, schemes)
		res.Envs = append(res.Envs, name)
	}
	if len(servers) == 0 {
		ensureEnv(ws, res.Title, savedHeader{Key: "baseUrl", Value: "http://localhost", Enabled: true}, schemes)
		res.Envs = append(res.Envs, res.Title)
	}
	if ws.currentEnv() == nil {
		ws.ActiveEnv = envIDByName(ws, res.Envs[0])
	}

	// Requests already imported from this spec, by "METHOD path".
	existing := map[string]bool{}
	sourcePrefix := "openapi:" + res.Title + ":"
	for _, r := range ws.Requests {
		if k, ok := strings.CutPrefix(r.Source, sourcePrefix); ok {
			existing[k] = true
		}
	}

	foldersBefore := len(ws.Folders)
	root := ensureFolder(ws, "", res.Title)
	res.FolderID = root
	globalSec, _ := spec.get("security").([]any)

	for _, path := range paths.keys {
		item := s.resolve(paths.vals[path])
		if item == nil {
			continue
		}
		shared, _ := item.get("parameters").([]any)
		for _, method := range specMethods {
			op := s.resolve(item.get(method))
			if op == nil {
				continue
			}
			key := strings.ToUpper(method) + " " + path
			if existing[key] {
				res.Skipped++
				continue
			}

			// Folder from the first tag; "Store Manager - Products" nests.
			folderID := root
			if tags, ok := op.get("tags").([]any); ok && len(tags) > 0 {
				for _, part := range strings.Split(str(tags[0]), " - ") {
					if part = strings.TrimSpace(part); part != "" {
						folderID = ensureFolder(ws, folderID, part)
					}
				}
			}

			r, multipart := s.buildRequest(op, strings.ToUpper(method), path, shared, globalSec, schemes)
			r.ID, r.Folder, r.Source = newID(), folderID, sourcePrefix+key
			ws.Requests = append(ws.Requests, r)
			res.Added++
			if multipart {
				res.Multipart = append(res.Multipart, key)
			}
		}
	}
	res.Folders = len(ws.Folders) - foldersBefore
	return res, nil
}

func (s specWalker) buildRequest(op *omap, method, path string, shared, globalSec []any, schemes *omap) (request, bool) {
	r := request{Method: method}
	r.Name = strings.TrimSpace(str(op.get("summary")))
	if r.Name == "" {
		r.Name = str(op.get("operationId"))
	}
	if r.Name == "" {
		r.Name = method + " " + path
	}

	// Parameters: path ones become {{variables}} unless they have an
	// example; required query ones go in the URL, optional ones are added
	// switched off.
	params, _ := op.get("parameters").([]any)
	var query []headerRow
	urlPath := path
	for _, pv := range append(append([]any{}, shared...), params...) {
		p := s.resolve(pv)
		if p == nil {
			continue
		}
		name, in := str(p.get("name")), str(p.get("in"))
		required, _ := p.get("required").(bool)
		val := s.paramValue(p)
		switch in {
		case "path":
			if val == "" {
				val = "{{" + name + "}}"
			}
			urlPath = strings.ReplaceAll(urlPath, "{"+name+"}", val)
		case "query":
			if required && val == "" {
				val = "{{" + name + "}}"
			}
			query = append(query, headerRow{key: name, value: val, enabled: required})
		case "header":
			r.Headers = append(r.Headers, savedHeader{Key: name, Value: val, Enabled: required})
		}
	}
	r.URL = buildURL("{{baseUrl}}"+urlPath, query)
	r.DisabledParams = toSavedHeaders(disabledRows(query))

	// Auth: the operation's security, or the spec-wide default.
	sec, hasOwn := op.get("security").([]any)
	if !hasOwn {
		sec = globalSec
	}
	if len(sec) > 0 {
		if req := s.resolve(sec[0]); req != nil {
			for _, name := range req.keys {
				r.Headers, r.URL = applyScheme(s.resolve(schemes.get(name)), r.Headers, r.URL)
			}
		}
	}

	// Body.
	multipart := false
	if rb := s.resolve(op.get("requestBody")); rb != nil {
		content := s.resolve(rb.get("content"))
		switch {
		case content.get("application/json") != nil:
			r.Headers = append(r.Headers, savedHeader{Key: "Content-Type", Value: "application/json", Enabled: true})
			r.Body = s.mediaSample(s.resolve(content.get("application/json")))
		case content.get("application/x-www-form-urlencoded") != nil:
			r.Headers = append(r.Headers, savedHeader{Key: "Content-Type", Value: "application/x-www-form-urlencoded", Enabled: true})
			mt := s.resolve(content.get("application/x-www-form-urlencoded"))
			if o, ok := s.sample(mt.get("schema"), 0).(*omap); ok {
				var rows []headerRow
				for _, k := range o.keys {
					rows = append(rows, headerRow{key: k, value: str(o.vals[k]), enabled: true})
				}
				r.Body = strings.TrimPrefix(buildURL("", rows), "?")
			}
		case content.get("multipart/form-data") != nil:
			multipart = true
			r.BodyMode = bodyForm
			r.Form = s.formFields(s.resolve(content.get("multipart/form-data")))
		}
	}
	return r, multipart
}

// formFields turns a multipart schema into form rows. File fields
// (format: binary) get "@" for you to complete with a path; other fields
// get sample values. Optional fields start switched off.
func (s specWalker) formFields(mt *omap) []savedHeader {
	sc := s.resolve(mt.get("schema"))
	required := map[string]bool{}
	if req, ok := sc.get("required").([]any); ok {
		for _, k := range req {
			required[str(k)] = true
		}
	}
	props := s.resolve(sc.get("properties"))
	if props == nil {
		return nil
	}
	var rows []savedHeader
	for _, k := range props.keys {
		p := s.resolve(props.vals[k])
		isFile := str(p.get("format")) == "binary"
		if items := s.resolve(p.get("items")); str(p.get("type")) == "array" && items != nil {
			isFile = str(items.get("format")) == "binary"
		}
		if isFile {
			// On when required, or when the schema doesn't say (an upload
			// endpoint's file is usually the point); optional files stay off
			// so the request can be sent without one.
			rows = append(rows, savedHeader{Key: k, Value: "@", Enabled: required[k] || len(required) == 0})
			continue
		}
		var val string
		switch v := s.sample(p, 0).(type) {
		case *omap, []any:
			var b strings.Builder
			writeJSON(&b, v, "")
			val = strings.Join(strings.Fields(b.String()), " ")
		default:
			val = str(v)
		}
		rows = append(rows, savedHeader{Key: k, Value: val, Enabled: required[k]})
	}
	return rows
}

// mediaSample prefers the spec's own example, then one built from the schema.
func (s specWalker) mediaSample(mt *omap) string {
	var v any
	if ex := mt.get("example"); ex != nil {
		v = ex
	} else if exs := s.resolve(mt.get("examples")); exs != nil && len(exs.keys) > 0 {
		v = s.resolve(exs.vals[exs.keys[0]]).get("value")
	} else {
		v = s.sample(mt.get("schema"), 0)
	}
	var b strings.Builder
	writeJSON(&b, v, "")
	return b.String()
}

// applyScheme adds what a security scheme needs, using variables the
// environments define.
func applyScheme(sc *omap, headers []savedHeader, url string) ([]savedHeader, string) {
	if sc == nil {
		return headers, url
	}
	switch str(sc.get("type")) {
	case "http":
		if strings.EqualFold(str(sc.get("scheme")), "basic") {
			return append(headers, savedHeader{Key: "Authorization", Value: "Basic {{basicAuth}}", Enabled: true}), url
		}
		return append(headers, savedHeader{Key: "Authorization", Value: "Bearer {{token}}", Enabled: true}), url
	case "oauth2", "openIdConnect":
		return append(headers, savedHeader{Key: "Authorization", Value: "Bearer {{token}}", Enabled: true}), url
	case "apiKey":
		name := str(sc.get("name"))
		switch str(sc.get("in")) {
		case "header":
			return append(headers, savedHeader{Key: name, Value: "{{apiKey}}", Enabled: true}), url
		case "query":
			_, q, _, _ := splitURL(url)
			return headers, buildURL(url, append(parseParams(q), headerRow{key: name, value: "{{apiKey}}", enabled: true}))
		}
	}
	return headers, url
}

// schemeVars lists the variables the security schemes need, empty.
func schemeVars(schemes *omap) []savedHeader {
	var out []savedHeader
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, savedHeader{Key: k, Enabled: true})
		}
	}
	if schemes == nil {
		return nil
	}
	keys := append([]string{}, schemes.keys...)
	sort.Strings(keys)
	for _, k := range keys {
		sc, _ := schemes.vals[k].(*omap)
		switch str(sc.get("type")) {
		case "http":
			if strings.EqualFold(str(sc.get("scheme")), "basic") {
				add("basicAuth")
			} else {
				add("token")
			}
		case "oauth2", "openIdConnect":
			add("token")
		case "apiKey":
			add("apiKey")
		}
	}
	return out
}

// ensureEnv creates or updates an environment by name. Existing values,
// such as a token you've filled in, are kept; only baseUrl is refreshed.
func ensureEnv(ws *workspace, name string, baseURL savedHeader, schemes *omap) {
	i := ws.findEnv(envIDByName(ws, name))
	if i < 0 {
		// Production servers start protected: the CLI must confirm before
		// sending anything there.
		prod := strings.Contains(strings.ToLower(name+" "+baseURL.Value), "prod")
		ws.Environments = append(ws.Environments, environment{ID: newID(), Name: name, Protected: prod})
		i = len(ws.Environments) - 1
	}
	env := &ws.Environments[i]
	for _, v := range append([]savedHeader{baseURL}, schemeVars(schemes)...) {
		j := -1
		for k := range env.Vars {
			if env.Vars[k].Key == v.Key {
				j = k
			}
		}
		switch {
		case j < 0:
			env.Vars = append(env.Vars, v)
		case v.Key == "baseUrl":
			env.Vars[j].Value = v.Value
		}
	}
}

func envIDByName(ws *workspace, name string) string {
	for _, e := range ws.Environments {
		if e.Name == name {
			return e.ID
		}
	}
	return ""
}

// ensureFolder returns the ID of the named folder under parent, creating it.
func ensureFolder(ws *workspace, parent, name string) string {
	for _, f := range ws.Folders {
		if f.Parent == parent && f.Name == name {
			return f.ID
		}
	}
	f := folder{ID: newID(), Name: name, Parent: parent, Collapsed: parent != ""}
	ws.Folders = append(ws.Folders, f)
	return f.ID
}

func (r importResult) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d request(s) added", r.Added)
	if r.Skipped > 0 {
		fmt.Fprintf(&b, ", %d already imported (left as they were)", r.Skipped)
	}
	return b.String()
}

// In-app import -----------------------------------------------------------------

type specLoadedMsg struct {
	src  string
	spec *omap
	err  error
}

// loadSpecCmd reads a spec in the background; URLs can take a while.
func (m *model) loadSpecCmd(src string) tea.Cmd {
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
		spec, err := loadSpec(src)
		return specLoadedMsg{src: src, spec: spec, err: err}
	}
}

func (m *model) finishImport(msg specLoadedMsg) {
	if msg.err != nil {
		m.notice = errorStyle.Render("import failed: " + msg.err.Error())
		return
	}
	var res importResult
	if !m.mutate(func(w *workspace) (err error) {
		if res, err = importOpenAPI(w, msg.spec); err != nil {
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
		res.Title, res.summary(), strings.Join(res.Envs, ", "), note))
}
