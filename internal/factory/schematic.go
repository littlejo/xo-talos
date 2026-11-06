package factory

import (
	"errors"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Schematic is an Image Factory schematic. It is kept as a generic document
// so every field the factory supports (extraKernelArgs, meta, overlay, ...)
// passes through untouched; the factory itself validates it.
type Schematic map[string]any

// ParseSchematic parses a schematic YAML document. An empty document gives an
// empty schematic.
func ParseSchematic(data []byte) (Schematic, error) {
	// Decode into a plain map: yaml.v3 gives nested mappings the type of the
	// target map, and the accessors below expect map[string]any.
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse schematic: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return Schematic(m), nil
}

// YAML returns the schematic as YAML.
func (s Schematic) YAML() ([]byte, error) {
	return yaml.Marshal(map[string]any(s))
}

// Extensions returns customization.systemExtensions.officialExtensions.
func (s Schematic) Extensions() ([]string, error) {
	sys, err := s.systemExtensions(false)
	if err != nil || sys == nil {
		return nil, err
	}
	raw, ok := sys["officialExtensions"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("schematic: customization.systemExtensions.officialExtensions must be a list")
	}
	exts := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("schematic: official extension %v is not a string", item)
		}
		exts = append(exts, name)
	}
	return exts, nil
}

// AddExtensions appends the official extensions that are not already listed.
func (s Schematic) AddExtensions(names ...string) error {
	exts, err := s.Extensions()
	if err != nil {
		return err
	}
	for _, name := range names {
		if !slices.Contains(exts, name) {
			exts = append(exts, name)
		}
	}
	sys, err := s.systemExtensions(true)
	if err != nil {
		return err
	}
	list := make([]any, len(exts))
	for i, e := range exts {
		list[i] = e
	}
	sys["officialExtensions"] = list
	return nil
}

// Has reports whether customization.<key> is set.
func (s Schematic) Has(key string) bool {
	custom, ok := s["customization"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = custom[key]
	return ok
}

// systemExtensions returns customization.systemExtensions, creating the
// intermediate maps when create is true.
func (s Schematic) systemExtensions(create bool) (map[string]any, error) {
	custom, err := child(s, "customization", "customization", create)
	if err != nil || custom == nil {
		return nil, err
	}
	return child(custom, "systemExtensions", "customization.systemExtensions", create)
}

func child(parent map[string]any, key, path string, create bool) (map[string]any, error) {
	raw, ok := parent[key]
	if !ok || raw == nil {
		if !create {
			return nil, nil
		}
		m := map[string]any{}
		parent[key] = m
		return m, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schematic: %s must be a mapping", path)
	}
	return m, nil
}
