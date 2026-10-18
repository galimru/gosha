package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

var secretRef = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// FieldError names the file and field a config problem is in.
type FieldError struct {
	File  string
	Field string
	Err   error
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %v", e.File, e.Err)
	}
	return fmt.Sprintf("%s: %s: %v", e.File, e.Field, e.Err)
}

func (e *FieldError) Unwrap() error { return e.Err }

// decode parses YAML into out (a pointer to a struct), following DESIGN §5.4:
// literal values in `secret:"true"` fields are rejected, ${VAR} is expanded
// after parsing and only inside string scalars, unknown fields are errors,
// and empty string fields with a `default:"…"` tag get their (expanded)
// default.
func decode(file string, data []byte, s *Secrets, out any) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return &FieldError{File: file, Err: err}
	}
	if root.Kind != 0 {
		if err := walk(file, &root, reflect.TypeOf(out).Elem(), "", false, s); err != nil {
			return err
		}
		buf, err := yaml.Marshal(&root)
		if err != nil {
			return &FieldError{File: file, Err: err}
		}
		dec := yaml.NewDecoder(bytes.NewReader(buf))
		dec.KnownFields(true)
		if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return &FieldError{File: file, Err: err}
		}
	}
	return applyDefaults(file, reflect.ValueOf(out).Elem(), "", s)
}

func walk(file string, n *yaml.Node, t reflect.Type, path string, secret bool, s *Secrets) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := walk(file, c, t, path, secret, s); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i].Value, n.Content[i+1]
			var ft reflect.Type
			sec := false
			if t != nil && t.Kind() == reflect.Struct {
				if f, ok := fieldByYAML(t, key); ok {
					ft, sec = f.Type, f.Tag.Get("secret") == "true"
				}
			} else if t != nil && t.Kind() == reflect.Map {
				ft = t.Elem()
			}
			if err := walk(file, val, ft, join(path, key), sec, s); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		var et reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			et = t.Elem()
		}
		for i, c := range n.Content {
			if err := walk(file, c, et, path+"["+strconv.Itoa(i)+"]", false, s); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if secret && !(n.ShortTag() == "!!str" && secretRef.MatchString(n.Value)) {
			return &FieldError{File: file, Field: path, Err: errors.New("secret fields must reference a variable like ${NAME}, never a literal value")}
		}
		if n.ShortTag() != "!!str" {
			return nil
		}
		v, err := Expand(n.Value, s)
		if err != nil {
			return &FieldError{File: file, Field: path, Err: err}
		}
		if v == n.Value {
			return nil
		}
		n.Value = v
		// Keep it a string after re-marshalling, whatever it now looks like.
		n.Tag, n.Style = "!!str", yaml.DoubleQuotedStyle
	case yaml.AliasNode:
		return &FieldError{File: file, Field: path, Err: errors.New("YAML aliases are not supported")}
	}
	return nil
}

func applyDefaults(file string, v reflect.Value, path string, s *Secrets) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f, fv := t.Field(i), v.Field(i)
		name := yamlName(f)
		if name == "-" || !f.IsExported() {
			continue
		}
		switch {
		case f.Type.Kind() == reflect.Struct && f.Type.PkgPath() != "time":
			if err := applyDefaults(file, fv, join(path, name), s); err != nil {
				return err
			}
		case f.Type.Kind() == reflect.String && fv.String() == "":
			def, ok := f.Tag.Lookup("default")
			if !ok {
				continue
			}
			val, err := Expand(def, s)
			if err != nil {
				return &FieldError{File: file, Field: join(path, name), Err: fmt.Errorf("%w (the default when the field is omitted)", err)}
			}
			fv.SetString(val)
		}
	}
	return nil
}

// ErrMissingVar is wrapped by Expand when a variable is unset or empty.
var ErrMissingVar = errors.New("variable is not set")

// Expand replaces every ${NAME} in v. A bare '$' is left alone and $${
// stands for a literal ${. Unset or empty variables are errors.
func Expand(v string, s *Secrets) (string, error) {
	if !strings.Contains(v, "${") {
		return v, nil
	}
	var b strings.Builder
	for i := 0; i < len(v); {
		switch {
		case strings.HasPrefix(v[i:], "$${"):
			b.WriteString("${")
			i += 3
		case strings.HasPrefix(v[i:], "${"):
			end := strings.IndexByte(v[i:], '}')
			if end < 0 {
				return "", errors.New("unterminated ${")
			}
			name := v[i+2 : i+end]
			if !envKey.MatchString(name) {
				return "", fmt.Errorf("invalid variable name %q", name)
			}
			val, ok := s.Lookup(name)
			if !ok {
				return "", fmt.Errorf("%w: ${%s}", ErrMissingVar, name)
			}
			b.WriteString(val)
			i += end + 1
		default:
			b.WriteByte(v[i])
			i++
		}
	}
	return b.String(), nil
}

func fieldByYAML(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); yamlName(f) == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func yamlName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	if name == "" {
		return strings.ToLower(f.Name)
	}
	return name
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
