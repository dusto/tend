package dispatch

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/dusto/tend/schemas"
)

// Validator checks inbound method params against the generated JSON Schemas in
// the schemas package. It is optional: a Mux without one does no validation.
type Validator struct {
	params map[string]*jsonschema.Schema // method name -> params schema
}

// NewValidator compiles the params schema for every method under schemas/methods.
func NewValidator() (*Validator, error) {
	entries, err := schemas.FS.ReadDir("methods")
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	urls := make(map[string]string) // method -> resource url
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".params.json") {
			continue
		}
		url := "methods/" + name
		b, err := schemas.FS.ReadFile(url)
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
		if err != nil {
			return nil, err
		}
		if err := c.AddResource(url, doc); err != nil {
			return nil, err
		}
		urls[strings.TrimSuffix(name, ".params.json")] = url
	}

	v := &Validator{params: make(map[string]*jsonschema.Schema, len(urls))}
	for method, url := range urls {
		sch, err := c.Compile(url)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", url, err)
		}
		v.params[method] = sch
	}
	return v, nil
}

// ValidateParams checks raw params for method against its schema. Methods with
// no params schema pass. Absent params are treated as an empty object.
func (v *Validator) ValidateParams(method string, raw json.RawMessage) error {
	sch, ok := v.params[method]
	if !ok {
		return nil
	}
	data := []byte(raw)
	if len(data) == 0 {
		data = []byte("{}")
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}
