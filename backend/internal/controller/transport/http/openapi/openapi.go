package openapi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed openapi.json
var rawSpec []byte

func Spec(apiVersion, backendBaseURL string) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(rawSpec, &document); err != nil {
		return nil, err
	}
	if apiVersion == "" {
		apiVersion = "v1"
	}

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
	return []byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Emfont Backend API</title>
  <style>body{margin:0;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}</style>
</head>
<body>
  <script id="api-reference" data-url="` + specURL + `"></script>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>`)
}
