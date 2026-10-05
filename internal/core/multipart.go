package core

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
)

// Multipart form-data bodies. Each field is "name: value"; a value starting
// with @ is a file, as with curl -F. File paths are relative to the
// project directory (the one barq was opened in) unless absolute.

const BodyForm = "multipart"

const maxUploadSize = 100 << 20

// fileValue reports whether a form value names a file, and its path.
func fileValue(v string) (string, bool) {
	if p, ok := strings.CutPrefix(v, "@"); ok {
		return strings.TrimSpace(p), true
	}
	return "", false
}

// resolveFilePath makes a form file path absolute: relative paths are
// taken from the project directory.
func resolveFilePath(p, cwd string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(cwd, p)
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// BuildBody encodes the request body. For form-data it returns the
// multipart content type (with its boundary) to send it with.
func BuildBody(r Request, cwd string) ([]byte, string, error) {
	if r.BodyMode != BodyForm {
		return []byte(r.Body), "", nil
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range r.Form {
		name := strings.TrimSpace(f.Key)
		if !f.Enabled || name == "" {
			continue
		}
		p, isFile := fileValue(f.Value)
		if !isFile {
			if err := w.WriteField(name, f.Value); err != nil {
				return nil, "", err
			}
			continue
		}
		if p == "" {
			return nil, "", fmt.Errorf("form field %q: add a file path after @", name)
		}
		full := resolveFilePath(p, cwd)
		st, err := os.Stat(full)
		if err != nil {
			return nil, "", fmt.Errorf("form field %q: %w", name, err)
		}
		if st.IsDir() {
			return nil, "", fmt.Errorf("form field %q: %s is a directory", name, full)
		}
		if st.Size()+int64(buf.Len()) > maxUploadSize {
			return nil, "", fmt.Errorf("form field %q: upload is over %s", name, HumanSize(maxUploadSize))
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, "", fmt.Errorf("form field %q: %w", name, err)
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
			quoteEscaper.Replace(name), quoteEscaper.Replace(filepath.Base(full))))
		h.Set("Content-Type", fileContentType(full, data))
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// fileContentType guesses a file's type from its extension, then from its
// first bytes. Servers often check it, e.g. to accept only images.
func fileContentType(path string, data []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); t != "" {
		return t
	}
	return http.DetectContentType(data)
}

// formRowState is how a form value should look in the table: a text value,
// an existing file, or a file that can't be found.
func FormFileState(value, cwd string) (isFile, exists bool) {
	p, ok := fileValue(value)
	if !ok {
		return false, false
	}
	if p == "" || strings.Contains(p, "{{") {
		return true, p != "" // variables are resolved at send time
	}
	st, err := os.Stat(resolveFilePath(p, cwd))
	return true, err == nil && !st.IsDir()
}
