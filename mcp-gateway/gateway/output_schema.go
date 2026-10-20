package gateway

import (
	"context"
	"encoding/json"
	"log"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// outputSchema retains reflection for adapter records while using the generated
// wire vocabulary for enums. Their Go integer representation is not their JSON
// representation. Currently Prepare choices are the only typed tool output
// containing a generated enum; other native results use generated JSON codecs.
func outputSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[wire.InboxReach](): enumOutputSchema(wire.InboxReachValues()),
	}})
	if err != nil {
		panic(err) // A static adapter declaration must have its exact schema.
	}
	return schema
}

func enumOutputSchema[T interface{ WireName() (string, bool) }](values []T) *jsonschema.Schema {
	// ForOptions adds null to pointer types, retaining Enum unchanged. Including
	// it here admits nil pointers; Type still refuses null for enum values.
	words := make([]any, 1, len(values)+1)
	for _, value := range values {
		word, ok := value.WireName()
		if !ok {
			panic("generated enum member has no wire name")
		}
		words = append(words, word)
	}
	return &jsonschema.Schema{Type: "string", Enum: words}
}

// openOutputSchemas lists every tool's output schema open to fields a later
// OpenAbstractions adds. A host keeps the schema it listed for the whole
// session and checks each result against it. A session that runs across an
// OA update then receives results with fields its listed schema did not name
// (oa_models_list gained setup_needed in R32), and a closed schema fails the
// call on the host. Input schemas stay exact.
func openOutputSchemas(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, request)
		if err != nil || method != "tools/list" {
			return result, err
		}
		list, ok := result.(*mcp.ListToolsResult)
		if !ok {
			return result, err
		}
		copy := *list
		copy.Tools = make([]*mcp.Tool, 0, len(list.Tools))
		for _, tool := range list.Tools {
			if tool == nil || tool.OutputSchema == nil {
				copy.Tools = append(copy.Tools, tool)
				continue
			}
			opened, err := openSchema(tool.OutputSchema)
			if err != nil {
				log.Printf("gateway: %s output schema kept as listed: %v", tool.Name, err)
				copy.Tools = append(copy.Tools, tool)
				continue
			}
			t := *tool
			t.OutputSchema = opened
			copy.Tools = append(copy.Tools, &t)
		}
		return &copy, nil
	}
}

// openSchema is schema with every additionalProperties that refuses unnamed
// fields removed, read from its JSON form so the server's own copy stays as
// declared.
func openSchema(schema any) (any, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	openSchemaNode(tree)
	return tree, nil
}

func openSchemaNode(node any) {
	switch v := node.(type) {
	case map[string]any:
		if closed, ok := v["additionalProperties"]; ok && refusesAll(closed) {
			delete(v, "additionalProperties")
		}
		if closed, ok := v["unevaluatedProperties"]; ok && refusesAll(closed) {
			delete(v, "unevaluatedProperties")
		}
		for _, child := range v {
			openSchemaNode(child)
		}
	case []any:
		for _, child := range v {
			openSchemaNode(child)
		}
	}
}

// refusesAll reports a schema that admits no value: false or {"not":{}}.
func refusesAll(schema any) bool {
	switch v := schema.(type) {
	case bool:
		return !v
	case map[string]any:
		not, ok := v["not"].(map[string]any)
		return ok && len(v) == 1 && len(not) == 0
	}
	return false
}
