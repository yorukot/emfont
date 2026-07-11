package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestSpecHasNoDuplicateJSONObjectKeys(t *testing.T) {
	decoder := json.NewDecoder(bytes.NewReader(rawSpec))
	if err := consumeUniqueJSONValue(decoder, "$"); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		t.Fatalf("OpenAPI document has trailing JSON content: %v", err)
	}
}

func consumeUniqueJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode JSON at %s: %w", path, err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode object key at %s: %w", path, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key at %s has type %T", path, keyToken)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key at %s.%s", path, key)
			}
			seen[key] = struct{}{}
			if err := consumeUniqueJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := consumeUniqueJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
	}
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode closing delimiter at %s: %w", path, err)
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return fmt.Errorf("closing delimiter at %s = %q, want %q", path, closing, want)
	}
	return nil
}

func TestErrorDetailUsesFieldLevelCodes(t *testing.T) {
	document := loadDocument(t)
	code := object(t, object(t, schema(t, document, "ErrorDetail"), "properties"), "code")

	want := []string{
		"INVALID_VALUE",
		"INVALID_FORMAT",
		"INVALID_ENUM",
		"REQUIRED",
		"VALUE_TOO_SHORT",
		"VALUE_TOO_LONG",
		"VALUE_TOO_SMALL",
		"VALUE_TOO_LARGE",
	}
	if got := stringsValue(t, code, "enum"); !reflect.DeepEqual(got, want) {
		t.Fatalf("ErrorDetail.code enum = %v, want %v", got, want)
	}
}

func TestGenerateFontRequestContract(t *testing.T) {
	document := loadDocument(t)
	request := schema(t, document, "GenerateFontRequest")
	if got, ok := request["additionalProperties"].(bool); !ok || got {
		t.Fatalf("GenerateFontRequest.additionalProperties = %#v, want false", request["additionalProperties"])
	}

	format := object(t, object(t, request, "properties"), "format")
	if got := stringsValue(t, format, "enum"); !reflect.DeepEqual(got, []string{"woff2"}) {
		t.Fatalf("GenerateFontRequest.format enum = %v, want [woff2]", got)
	}
}

func TestCSSFormatQueryContract(t *testing.T) {
	document := loadDocument(t)
	operation := operation(t, document, "/css/{font}", "get")
	min := parameter(t, operation, "min", "query")
	if got := object(t, min, "schema")["type"]; got != "boolean" {
		t.Fatalf("CSS min type = %#v, want boolean", got)
	}
	responses := object(t, operation, "responses")
	response := object(t, responses, "422")
	content := object(t, response, "content")
	assertRef(t, object(t, object(t, content, "application/problem+json"), "schema"), "#/components/schemas/Problem")

	parameter := parameter(t, operation, "format", "query")
	if description, _ := parameter["description"].(string); !strings.Contains(strings.ToLower(description), "words") {
		t.Fatalf("format parameter description = %q, want words applicability", description)
	}
	if got := stringsValue(t, object(t, parameter, "schema"), "enum"); !reflect.DeepEqual(got, []string{"woff2"}) {
		t.Fatalf("CSS format enum = %v, want [woff2]", got)
	}
}

func TestSpecUsesConfiguredAPIVersion(t *testing.T) {
	data, err := Spec("v42", "https://api.example.test/")
	if err != nil {
		t.Fatalf("generate OpenAPI spec: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("unmarshal generated OpenAPI document: %v", err)
	}
	if got := object(t, document, "info")["version"]; got != "v42" {
		t.Fatalf("info.version = %#v, want v42", got)
	}
	servers, ok := document["servers"].([]any)
	if !ok || len(servers) != 1 {
		t.Fatalf("servers = %#v, want one server", document["servers"])
	}
	server, ok := servers[0].(map[string]any)
	if !ok || server["url"] != "https://api.example.test/api/v42" {
		t.Fatalf("server = %#v, want configured API path", servers[0])
	}
}

func TestScalarHTMLHasNoRemoteScript(t *testing.T) {
	html := string(ScalarHTML("/api/v42/openapi.json"))
	if strings.Contains(strings.ToLower(html), "<script") || strings.Contains(html, "http://") || strings.Contains(html, "https://") {
		t.Fatalf("documentation HTML contains a script or remote dependency: %s", html)
	}
	if !strings.Contains(html, `href="/api/v42/openapi.json"`) {
		t.Fatalf("documentation HTML does not use configured OpenAPI path: %s", html)
	}
}

func TestOperationProblemResponses(t *testing.T) {
	document := loadDocument(t)
	tests := []struct {
		path     string
		method   string
		statuses []string
	}{
		{path: "/list", method: "get", statuses: []string{"500", "503", "504"}},
		{path: "/info/{fontID}", method: "get", statuses: []string{"422", "500", "503", "504"}},
		{path: "/system", method: "get", statuses: []string{"422", "429", "500", "503", "504"}},
		{path: "/g/{font}", method: "post", statuses: []string{"500", "503", "504"}},
		{path: "/css/{font}", method: "get", statuses: []string{"500", "503", "504"}},
		{path: "/openapi.json", method: "get", statuses: []string{"500"}},
	}

	for _, tt := range tests {
		operation := operation(t, document, tt.path, tt.method)
		responses := object(t, operation, "responses")
		for _, status := range tt.statuses {
			response := object(t, responses, status)
			content := object(t, response, "content")
			problem := object(t, content, "application/problem+json")
			assertRef(t, object(t, problem, "schema"), "#/components/schemas/Problem")
		}
	}
}

func TestPublicFontOperationsDocumentRateLimitResponse(t *testing.T) {
	document := loadDocument(t)
	tests := []struct {
		path   string
		method string
	}{
		{path: "/g/{font}", method: "post"},
		{path: "/css/{font}", method: "get"},
		{path: "/list", method: "get"},
		{path: "/info/{fontID}", method: "get"},
		{path: "/system", method: "get"},
		{path: "/openapi.json", method: "get"},
		{path: "/docs", method: "get"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			response := object(t, object(t, operation(t, document, tt.path, tt.method), "responses"), "429")
			retryAfter := object(t, object(t, object(t, response, "headers"), "Retry-After"), "schema")
			if retryAfter["type"] != "integer" || retryAfter["minimum"] != float64(1) {
				t.Fatalf("Retry-After schema = %#v, want integer with minimum 1", retryAfter)
			}
			content := object(t, response, "content")
			assertRef(t, object(t, object(t, content, "application/problem+json"), "schema"), "#/components/schemas/Problem")
		})
	}
}

func TestSystemContract(t *testing.T) {
	document := loadDocument(t)
	operation := operation(t, document, "/system", "get")
	id := parameter(t, operation, "id", "query")
	if got := object(t, id, "schema")["default"]; got != "default" {
		t.Fatalf("system id default = %#v, want default", got)
	}

	response := object(t, object(t, operation, "responses"), "200")
	content := object(t, response, "content")
	assertRef(t, object(t, object(t, content, "application/json"), "schema"), "#/components/schemas/System")

	system := schema(t, document, "System")
	wantRequired := []string{"id", "name", "environment", "version", "status"}
	if got := stringsValue(t, system, "required"); !reflect.DeepEqual(got, wantRequired) {
		t.Fatalf("System.required = %v, want %v", got, wantRequired)
	}
	properties := object(t, system, "properties")
	for _, name := range append(wantRequired, "revision") {
		if _, ok := properties[name]; !ok {
			t.Errorf("System.properties is missing %q", name)
		}
	}
	status := object(t, properties, "status")
	wantStatuses := []string{"ready", "degraded", "maintenance"}
	if got := stringsValue(t, status, "enum"); !reflect.DeepEqual(got, wantStatuses) {
		t.Fatalf("System.status enum = %v, want %v", got, wantStatuses)
	}
}

func loadDocument(t *testing.T) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(rawSpec, &document); err != nil {
		t.Fatalf("unmarshal OpenAPI document: %v", err)
	}
	return document
}

func operation(t *testing.T, document map[string]any, path, method string) map[string]any {
	t.Helper()
	return object(t, object(t, object(t, document, "paths"), path), method)
}

func schema(t *testing.T, document map[string]any, name string) map[string]any {
	t.Helper()
	return object(t, object(t, object(t, document, "components"), "schemas"), name)
}

func parameter(t *testing.T, operation map[string]any, name, location string) map[string]any {
	t.Helper()
	parameters, ok := operation["parameters"].([]any)
	if !ok {
		t.Fatalf("parameters = %T, want array", operation["parameters"])
	}
	for _, raw := range parameters {
		candidate, ok := raw.(map[string]any)
		if ok && candidate["name"] == name && candidate["in"] == location {
			return candidate
		}
	}
	t.Fatalf("parameter %s in %s not found", name, location)
	return nil
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want object", key, parent[key])
	}
	return value
}

func stringsValue(t *testing.T, parent map[string]any, key string) []string {
	t.Helper()
	values, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%s = %T, want array", key, parent[key])
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("%s contains %T, want string", key, value)
		}
		result = append(result, text)
	}
	return result
}

func assertRef(t *testing.T, schema map[string]any, want string) {
	t.Helper()
	if got := schema["$ref"]; got != want {
		t.Fatalf("schema $ref = %#v, want %q", got, want)
	}
}
