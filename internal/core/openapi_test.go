package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const petYAML = `
openapi: 3.0.3
info:
  title: Pets
servers:
  - url: https://{region}.pets.example/v1/
    description: Prod
    variables:
      region:
        default: eu
security:
  - ApiKey: []
components:
  securitySchemes:
    ApiKey: {type: apiKey, in: header, name: X-API-Key}
  parameters:
    Limit:
      name: limit
      in: query
      schema: {type: integer, default: 20}
  schemas:
    Named:
      type: object
      properties:
        name: {type: string, example: Rex}
    Pet:
      allOf:
        - $ref: '#/components/schemas/Named'
        - type: object
          properties:
            age: {type: integer, minimum: 0}
            tags: {type: array, items: {type: string}}
            born: {type: string, format: date}
paths:
  /pets:
    get:
      tags: [Pets - Read]
      summary: List pets
      parameters:
        - $ref: '#/components/parameters/Limit'
        - {name: species, in: query, required: true, schema: {type: string}}
    post:
      tags: [Pets - Write]
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Pet'}
  /pets/{petId}:
    parameters:
      - {name: petId, in: path, required: true, schema: {type: string}}
    delete:
      security: []
    patch:
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema:
              type: object
              properties:
                name: {type: string, example: Max Power}
`

func writeSpec(t *testing.T, name, content string) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func importFixture(t *testing.T) (*Workspace, ImportResult) {
	t.Setenv("HOME", t.TempDir())
	spec, err := LoadSpec(writeSpec(t, "pets.yaml", petYAML))
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := LoadWorkspace("/proj")
	res, err := ImportOpenAPI(ws, spec)
	if err != nil {
		t.Fatal(err)
	}
	return ws, res
}

func findReq(t *testing.T, ws *Workspace, source string) Request {
	for _, r := range ws.Requests {
		if strings.HasSuffix(r.Source, ":"+source) {
			return r
		}
	}
	t.Fatalf("no request for %s", source)
	return Request{}
}

func TestImportOpenAPIYAML(t *testing.T) {
	ws, res := importFixture(t)
	if res.Added != 4 || res.Title != "Pets" {
		t.Fatalf("result: %+v", res)
	}

	list := findReq(t, ws, "GET /pets")
	if list.Name != "List pets" || list.URL != "{{baseUrl}}/pets?species={{species}}" {
		t.Errorf("list: name %q url %q", list.Name, list.URL)
	}
	if len(list.DisabledParams) != 1 || list.DisabledParams[0].Key != "limit" || list.DisabledParams[0].Value != "20" {
		t.Errorf("optional params should be switched off with defaults: %+v", list.DisabledParams)
	}
	if len(list.Headers) != 1 || list.Headers[0] != (SavedHeader{Key: "X-API-Key", Value: "{{apiKey}}", Enabled: true}) {
		t.Errorf("global apiKey security: %+v", list.Headers)
	}
	if got := ws.FolderPath(list.Folder); got != "Pets / Pets / Read" {
		t.Errorf("tag folder = %q", got)
	}

	create := findReq(t, ws, "POST /pets")
	if create.Name != "createPet" {
		t.Errorf("name should fall back to operationId: %q", create.Name)
	}
	wantBody := "{\n  \"name\": \"Rex\",\n  \"age\": 0,\n  \"tags\": [\n    \"string\"\n  ],\n  \"born\": \"2026-01-01\"\n}"
	if create.Body != wantBody {
		t.Errorf("body from allOf + $ref, in order:\n%s", create.Body)
	}

	del := findReq(t, ws, "DELETE /pets/{petId}")
	if del.URL != "{{baseUrl}}/pets/{{petId}}" || len(del.Headers) != 0 {
		t.Errorf("path-level param and security: [] override: %q %+v", del.URL, del.Headers)
	}
	patch := findReq(t, ws, "PATCH /pets/{petId}")
	if patch.Body != "name=Max+Power" {
		t.Errorf("form body: %q", patch.Body)
	}

	if len(ws.Environments) != 1 || ws.Environments[0].Name != "Prod" {
		t.Fatalf("envs: %+v", ws.Environments)
	}
	vars := ws.Environments[0].Vars
	if vars[0] != (SavedHeader{Key: "baseUrl", Value: "https://eu.pets.example/v1", Enabled: true}) || vars[1].Key != "apiKey" {
		t.Errorf("env vars: %+v", vars)
	}
	if ws.ActiveEnv != ws.Environments[0].ID {
		t.Error("the first server should become the active environment")
	}
}

func TestReimportKeepsEdits(t *testing.T) {
	ws, _ := importFixture(t)
	spec, _ := LoadSpec(writeSpec(t, "pets.yaml", petYAML))

	ws.Environments[0].Vars[1].Value = "secret" // filled-in key
	for i := range ws.Requests {
		ws.Requests[i].Body = "edited"
	}
	res, err := ImportOpenAPI(ws, spec)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Skipped != 4 || len(ws.Requests) != 4 || res.Folders != 0 {
		t.Errorf("re-import: %+v, %d requests", res, len(ws.Requests))
	}
	if ws.Requests[0].Body != "edited" || ws.Environments[0].Vars[1].Value != "secret" {
		t.Error("re-import overwrote edits")
	}
}

func TestLoadSpecRejects(t *testing.T) {
	if _, err := LoadSpec(writeSpec(t, "s.json", `{"swagger":"2.0","paths":{}}`)); err == nil || !strings.Contains(err.Error(), "Swagger 2.0") {
		t.Errorf("swagger 2: %v", err)
	}
	if _, err := LoadSpec(writeSpec(t, "x.json", `{"hello":1}`)); err == nil {
		t.Error("expected an error for a non-OpenAPI document")
	}
}
