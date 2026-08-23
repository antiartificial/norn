package model

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// ParseInfraSpecDocument strictly decodes one InfraSpec document. File-based
// discovery remains backwards compatible, while uploaded and CI validation
// rejects misspelled/unknown fields and multi-document YAML.
func ParseInfraSpecDocument(document []byte) (*InfraSpec, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	decoder.KnownFields(true)
	var spec InfraSpec
	if err := decoder.Decode(&spec); err != nil {
		return nil, fmt.Errorf("decode infraspec: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode infraspec: multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("decode infraspec: %w", err)
	}
	applyDefaults(&spec)
	return &spec, nil
}
