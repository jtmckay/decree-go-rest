package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// OpenAPIVersion is the version of the OpenAPI document served at
// /openapi.json.
const OpenAPIVersion = "3.1.0"

// obj is a JSON object of the OpenAPI document. encoding/json writes its
// keys sorted, so the document is the same for the same config.
type obj = map[string]any

// openAPI is the OpenAPI 3.1 document of c (SPEC.md §7): every configured
// endpoint, described by its action, with its path parameters and their
// patterns, the bearer scheme, request bodies as text/plain and the
// responses of SPEC.md §4; then /healthz and /openapi.json.
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
			params = append(params, pathParameter(name, e.ParamPattern(name), MaxParamBytes))
		}
		var op obj
		if e.Action == config.ActionEvent {
			op = eventOperation(c, e)
		} else {
			op = emitOperation(c, e)
		}
		if params != nil {
			op["parameters"] = params
		}
		add(e.Path, "post", op)
	}

	add(config.HealthPath, "get", obj{
		"summary":  "The daemon's and the config's health",
		"security": []any{},
		"responses": obj{
			"200": jsonResponse("The daemon runs, or is disabled, and the config loaded.", ref("Health")),
			"503": jsonResponse("The daemon is enabled but not running, or the last config reload failed.", ref("Health")),
		},
	})
	add(config.OpenAPIPath, "get", obj{
		"summary":  "This document",
		"security": []any{},
		"responses": obj{
			"200": jsonResponse("The OpenAPI document of the loaded config.", obj{"type": "object"}),
		},
	})

	doc := document{
		OpenAPI: OpenAPIVersion,
		Info: obj{
			"title":       "decree-go-rest",
			"version":     "1",
			"description": "The HTTP front door of a decree project: each endpoint queues a decree message, or replies to a run waiting for a person.",
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

// emitOperation is the operation of an emit endpoint: SPEC.md §4.
func emitOperation(c *config.Config, e config.Endpoint) obj {
	machine := ""
	if e.Message != nil {
		machine = e.Message.Machine
	}
	op := obj{
		"summary":     "Queue a message for machine " + machine,
		"description": fmt.Sprintf("Runs `decree emit --machine %s`, with the body on stdin. Authenticated with %s.", machine, secretName(c, e.SecretEnv)),
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
	switch e.Body {
	case config.BodyRequired:
		op["requestBody"] = textBody(true, fmt.Sprintf("The message body, opaque text of at most %d bytes; it must not be blank.", c.Limits.MaxBodyBytes))
	case config.BodyOptional:
		op["requestBody"] = textBody(false, fmt.Sprintf("The message body, opaque text of at most %d bytes.", c.Limits.MaxBodyBytes))
	}
	return op
}

// eventOperation is the operation of an event endpoint: it replies to a
// run waiting in a person state, with the body as the note.
func eventOperation(c *config.Config, e config.Endpoint) obj {
	var to, event string
	if e.Reply != nil {
		to, event = e.Reply.To, e.Reply.Event
	}
	op := obj{
		"summary":     "Reply " + event + " to a run waiting for a person",
		"description": fmt.Sprintf("Runs `decree event %s %s [-m=<note>] --format json`, with the body as the note. Authenticated with %s.", to, event, secretName(c, e.SecretEnv)),
		"security":    bearer,
		"responses": obj{
			"201": jsonResponse("The reply is queued.", ref("Reply")),
			"400": errorResponse("A parameter outside its pattern, a note that breaks the endpoint's body rule, or a note holding a NUL byte."),
			"401": errorResponse("A missing or wrong bearer secret."),
			"409": errorResponse("The run is not waiting, or does not accept the event."),
			"413": errorResponse(fmt.Sprintf("A note larger than %d bytes.", c.Limits.MaxBodyBytes)),
			"429": rateLimited,
			"500": errorResponse("decree could not queue the reply."),
		},
	}
	switch e.Body {
	case config.BodyRequired:
		op["requestBody"] = textBody(true, fmt.Sprintf("The note, passed unchanged, of at most %d bytes; it must not be blank.", c.Limits.MaxBodyBytes))
	case config.BodyOptional:
		op["requestBody"] = textBody(false, fmt.Sprintf("The optional note, passed unchanged, of at most %d bytes.", c.Limits.MaxBodyBytes))
	}
	return op
}

// secretName says which bearer secret an operation whose secret_env is env
// takes, without naming the variable.
func secretName(c *config.Config, env string) string {
	if env != "" && env != c.SecretEnv {
		return "its own bearer secret"
	}
	return "the default bearer secret"
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
		"required":             []any{"id", "path", "to", "event"},
		"additionalProperties": false,
		"properties": obj{
			"id":    obj{"type": "string", "description": "The reply's message id."},
			"path":  obj{"type": "string", "description": "The queued file, relative to the project: .decree/inbox/<id>.md."},
			"to":    obj{"type": "string", "description": "The wait id of the run replied to."},
			"event": obj{"type": "string", "description": "The event sent."},
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
// decree-go-rest matches it, of at most maxLength bytes when that is not 0.
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

// serveOpenAPI is GET /openapi.json (SPEC.md §7), when config.OpenAPIEnv
// is true: no authentication, and the document of the config this Server
// was built from, so it follows reloads.
func (s *Server) serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	logged(r).route = config.OpenAPIPath
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	w.Write(s.openapi)
}
