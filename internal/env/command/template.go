package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// templateContext is the data passed to every command template.
type templateContext struct {
	SessionID     string
	ProjectID     string
	Workdir       string
	Workdirs      string // shell-escaped space-joined list
	ProviderRef   string
	EnvSpecConfig string // full config as JSON
	SpecDir       string // directory of the loaded spec file (empty for in-memory specs)
}

// render renders a command template with the given context. Returns the
// rendered string or an error on template syntax / missing-key issues.
func render(tmpl string, ctx templateContext) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("cmd").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("command: parse template %q: %w", tmpl, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("command: execute template %q: %w", tmpl, err)
	}
	return buf.String(), nil
}

// shellEscapeList joins list elements into a single shell-safe argument
// string: each element is single-quoted with embedded single quotes escaped.
// Consumers can use `$ARGS` directly in a script without re-quoting.
func shellEscapeList(list []string) string {
	parts := make([]string, 0, len(list))
	for _, s := range list {
		parts = append(parts, shellQuote(s))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// jsonOrEmpty serializes v as JSON. Returns "{}" on error so templates never
// get a bare "" — the command script can parse "{}" cleanly.
func jsonOrEmpty(v any) string {
	buf, err := json.Marshal(v)
	if err != nil || len(buf) == 0 {
		return "{}"
	}
	return string(buf)
}
