package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jtmckay/decree-api/internal/config"
)

// OpenAPIVersion is the version of the OpenAPI document served at
// /openapi.json.
const OpenAPIVersion = "3.1.0"

// obj is a JSON object of the OpenAPI document. encoding/json writes its
// keys sorted, so the document is the same for the same config.
type obj = map[string]any

// openAPI is the OpenAPI 3.1 document of c (SPEC.md §7): every configured
// endpoint and enabled built-in, with its path parameters and their
// patterns, the bearer scheme, request bodies as text/plain, and the
// responses of SPEC.md §4 and §7.
func openAPI(c *config.Config) ([]byte, error) {
	paths := obj{}
	add := func(path, method string, op obj) {
		item, ok := paths[path].(obj)
		if !ok {
			item = obj{}
			paths[path] = item
		}
		item[method] = op
	}

	for _, e := range c.Endpoints {
		var params []any
		for _, name := range config.PathParams(e.Path) {
			pat, ok := e.Patterns[name]
			if !ok {
				pat = config.DefaultParamPattern
			}
			params = append(params, pathParameter(name, pat, MaxParamBytes))
		}
		secret := "the default bearer secret"
		if e.SecretEnv != "" && e.SecretEnv != c.SecretEnv {
			secret = "its own bearer secret"
		}
		op := obj{
			"summary":     "Queue a message for machine " + e.Message.Machine,
			"description": fmt.Sprintf("Runs `decree emit --machine %s`, with the body on stdin. Authenticated with %s.", e.Message.Machine, secret),
			"security":    bearer,
			"responses": obj{
				"201": jsonResponse("The message is queued.", ref("Message")),
				"400": errorResponse("A parameter outside its pattern, a body that breaks the endpoint's body rule, or a message decree rejected."),
				"401": errorResponse("A missing or wrong bearer secret."),
				"413": errorResponse(fmt.Sprintf("A body larger than %d bytes.", c.Limits.MaxBodyBytes)),
				"429": rateLimited,
				"500": errorResponse("decree could not queue the message."),
			},
		}
		if params != nil {
			op["parameters"] = params
		}
		switch e.Body {
		case config.BodyRequired:
			op["requestBody"] = textBody(true, fmt.Sprintf("The message body, opaque text of at most %d bytes; it must not be blank.", c.Limits.MaxBodyBytes))
		case config.BodyOptional:
			op["requestBody"] = textBody(false, fmt.Sprintf("The message body, opaque text of at most %d bytes.", c.Limits.MaxBodyBytes))
		}
		add(e.Path, "post", op)
	}

	for _, rt := range c.BuiltinRoutes() {
		switch rt.Path {
		case config.HealthPath:
			add(rt.Path, "get", obj{
				"summary":  "The daemon's and the config's health",
				"security": []any{},
				"responses": obj{
					"200": jsonResponse("The daemon runs, or is disabled, and the config loaded.", ref("Health")),
					"503": jsonResponse("The daemon is enabled but not running, or the last config reload failed.", ref("Health")),
				},
			})
		case config.StatusPath:
			add(rt.Path, "get", obj{
				"summary":     "A run's status",
				"description": "Runs `decree status <id> --format json`. Authenticated with the default bearer secret.",
				"security":    bearer,
				"parameters":  []any{pathParameter("id", config.RunIDPattern, 0)},
				"responses": obj{
					"200": jsonResponse("decree's document of the run, unchanged.", ref("Run")),
					"400": errorResponse("The id is outside its pattern."),
					"401": errorResponse("A missing or wrong bearer secret."),
					"404": errorResponse("decree knows no such run."),
					"429": rateLimited,
					"500": errorResponse("decree could not read the run."),
				},
			})
		case config.RepliesPath:
			add(rt.Path, "post", obj{
				"summary":     "Reply to a run waiting for a person",
				"description": "Runs `decree event <wait_id> <event> [-m=<note>] --format json`. Authenticated with the default bearer secret.",
				"security":    bearer,
				"parameters": []any{
					pathParameter("wait_id", config.RunIDPattern, 0),
					pathParameter("event", config.RunIDPattern, 0),
				},
				"requestBody": textBody(false, fmt.Sprintf("The optional note, passed unchanged, of at most %d bytes.", c.Limits.MaxBodyBytes)),
				"responses": obj{
					"201": jsonResponse("The reply is queued.", ref("Reply")),
					"400": errorResponse("A parameter outside its pattern, or a note holding a NUL byte."),
					"401": errorResponse("A missing or wrong bearer secret."),
					"409": errorResponse("The run is not waiting, or does not accept the event."),
					"413": errorResponse(fmt.Sprintf("A note larger than %d bytes.", c.Limits.MaxBodyBytes)),
					"429": rateLimited,
					"500": errorResponse("decree could not queue the reply."),
				},
			})
		case config.OpenAPIPath:
			add(rt.Path, "get", obj{
				"summary":  "This document",
				"security": []any{},
				"responses": obj{
					"200": jsonResponse("The OpenAPI document of the loaded config.", obj{"type": "object"}),
				},
			})
		}
	}

	doc := document{
		OpenAPI: OpenAPIVersion,
		Info: obj{
			"title":       "decree-api",
			"version":     "1",
			"description": "The HTTP front door of a decree project: each endpoint queues a decree message.",
		},
		Paths: paths,
		Components: obj{
			"securitySchemes": obj{
				"bearer": obj{"type": "http", "scheme": "bearer"},
			},
			"schemas": schemas,
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("openapi document: %w", err)
	}
	return buf.Bytes(), nil
}

// document is the top level of the OpenAPI document, in the usual order.
type document struct {
	OpenAPI    string `json:"openapi"`
	Info       obj    `json:"info"`
	Paths      obj    `json:"paths"`
	Components obj    `json:"components"`
}

// bearer is the security requirement of an authenticated operation.
var bearer = []any{obj{"bearer": []any{}}}

// rateLimited is the 429 response.
var rateLimited = obj{
	"description": "A rate budget is exhausted.",
	"headers": obj{
		"Retry-After": obj{
			"description": "Seconds until the budget's window ends.",
			"schema":      obj{"type": "integer"},
		},
	},
	"content": obj{"application/json": obj{"schema": ref("Error")}},
}

// schemas are the documents the responses reference.
var schemas = obj{
	"Error": obj{
		"type":                 "object",
		"required":             []any{"error"},
		"additionalProperties": false,
		"properties":           obj{"error": obj{"type": "string"}},
	},
	"Message": obj{
		"type":                 "object",
		"required":             []any{"id", "path", "machine"},
		"additionalProperties": false,
		"properties": obj{
			"id":      obj{"type": "string", "description": "The message id, also its run id once decree claims it."},
			"path":    obj{"type": "string", "description": "The queued file, relative to the project: .decree/inbox/<id>.md."},
			"machine": obj{"type": "string", "description": "The machine the message names."},
		},
	},
	"Reply": obj{
		"type":                 "object",
		"required":             []any{"id", "path"},
		"additionalProperties": false,
		"properties": obj{
			"id":   obj{"type": "string", "description": "The reply's message id."},
			"path": obj{"type": "string", "description": "The queued file, relative to the project: .decree/inbox/<id>.md."},
		},
	},
	"Run": obj{
		"type":        "object",
		"description": "`decree status <id> --format json`, described by decree's .decree/schema/v1/cli/status.schema.json.",
		"required":    []any{"id", "machine", "status", "state", "events"},
		"properties": obj{
			"id":      obj{"type": "string"},
			"machine": obj{"type": "string"},
			"status":  obj{"enum": []any{"active", "waiting", "pending", "interrupted", "finished"}},
			"state":   obj{"type": []any{"string", "null"}},
			"events":  obj{"type": "array", "items": obj{"type": "object"}},
		},
	},
	"Health": obj{
		"type":     "object",
		"required": []any{"ok", "daemon", "config"},
		"properties": obj{
			"ok": obj{"type": "boolean"},
			"daemon": obj{
				"type":     "object",
				"required": []any{"enabled", "running", "pid", "restarts", "since"},
				"properties": obj{
					"enabled":  obj{"type": "boolean"},
					"running":  obj{"type": "boolean"},
					"pid":      obj{"type": []any{"integer", "null"}},
					"restarts": obj{"type": "integer"},
					"since":    obj{"type": []any{"string", "null"}, "format": "date-time"},
				},
			},
			"config": obj{
				"type":     "object",
				"required": []any{"loaded_at", "error"},
				"properties": obj{
					"loaded_at": obj{"type": "string", "format": "date-time"},
					"error":     obj{"type": []any{"string", "null"}},
				},
			},
		},
	},
}

// pathParameter is a path parameter matching pattern, anchored as
// decree-api matches it, of at most maxLength bytes when that is not 0.
func pathParameter(name, pattern string, maxLength int) obj {
	schema := obj{"type": "string", "pattern": "^(?:" + pattern + ")$"}
	if maxLength > 0 {
		schema["maxLength"] = maxLength
	}
	return obj{"name": name, "in": "path", "required": true, "schema": schema}
}

func textBody(required bool, description string) obj {
	return obj{
		"required":    required,
		"description": description,
		"content":     obj{"text/plain": obj{"schema": obj{"type": "string"}}},
	}
}

func jsonResponse(description string, schema obj) obj {
	return obj{
		"description": description,
		"content":     obj{"application/json": obj{"schema": schema}},
	}
}

func errorResponse(description string) obj {
	return jsonResponse(description, ref("Error"))
}

func ref(schema string) obj {
	return obj{"$ref": "#/components/schemas/" + schema}
}

// serveOpenAPI is GET /openapi.json (SPEC.md §7): no authentication, and
// the document of the config this Server was built from, so it follows
// reloads.
func (s *Server) serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	logged(r).route = config.OpenAPIPath
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	w.Write(s.openapi)
}
