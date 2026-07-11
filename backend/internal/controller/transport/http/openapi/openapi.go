package openapi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"html"
	"strings"
)

//go:embed openapi.json
var rawSpec []byte

func Spec(apiVersion, backendBaseURL string) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(rawSpec, &document); err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiVersion) == "" {
		apiVersion = "v1"
	}
	info := document["info"].(map[string]any)
	info["version"] = apiVersion

	base := strings.TrimRight(strings.TrimSpace(backendBaseURL), "/")
	if base == "" {
		base = ""
	}
	document["servers"] = []map[string]string{
		{"url": base + "/api/" + apiVersion},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(document); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ScalarHTML(specURL string) []byte {
	if specURL == "" {
		specURL = "/api/v1/openapi.json"
	}
	specURL = html.EscapeString(specURL)
	return []byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Emfont Backend API</title>
  <style>body{margin:2rem;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;line-height:1.5}main{max-width:48rem}</style>
</head>
<body>
  <main>
    <h1>Emfont Backend API</h1>
    <p><a href="` + specURL + `">OpenAPI document</a></p>
  </main>
</body>
</html>`)
}
