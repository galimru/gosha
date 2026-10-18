package config

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Effective renders a parsed config as YAML, every field shown with its
// default filled in, in the order of the config file's documentation.
// Durations are written as they're configured ("30m", "1h30m").
func Effective(v any) ([]byte, error) {
	n, err := node(reflect.ValueOf(v))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	return []byte(b.String()), enc.Close()
}

var durationType = reflect.TypeFor[time.Duration]()

func node(v reflect.Value) (*yaml.Node, error) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
		}
		v = v.Elem()
	}
	if v.Type() == durationType {
		return &yaml.Node{Kind: yaml.ScalarNode, Value: FormatDuration(time.Duration(v.Int()))}, nil
	}
	switch v.Kind() {
	case reflect.Struct:
		m := &yaml.Node{Kind: yaml.MappingNode}
		for i := range v.NumField() {
			f := v.Type().Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "-" || !f.IsExported() {
				continue
			}
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			val, err := node(v.Field(i))
			if err != nil {
				return nil, err
			}
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: name}, val)
		}
		return m, nil
	case reflect.Slice:
		s := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for i := range v.Len() {
			val, err := node(v.Index(i))
			if err != nil {
				return nil, err
			}
			s.Content = append(s.Content, val)
		}
		return s, nil
	}
	var n yaml.Node
	if err := n.Encode(v.Interface()); err != nil {
		return nil, fmt.Errorf("render %s: %w", v.Type(), err)
	}
	return &n, nil
}

// FormatDuration writes d without zero units: 30m, 1h, 1h30m, 45s.
func FormatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
