package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"

	"go.yaml.in/yaml/v3"
)

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

// decode parses YAML into out (a pointer to a struct). Unknown fields are
// errors, and empty string fields with a `default:"…"` tag get their
// default. Config files hold no secrets (DESIGN §5.4).
func decode(file string, data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return &FieldError{File: file, Err: err}
	}
	applyDefaults(reflect.ValueOf(out).Elem())
	return nil
}

func applyDefaults(v reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f, fv := t.Field(i), v.Field(i)
		if !f.IsExported() {
			continue
		}
		switch {
		case f.Type.Kind() == reflect.Struct && f.Type.PkgPath() != "time":
			applyDefaults(fv)
		case f.Type.Kind() == reflect.String && fv.String() == "":
			if def, ok := f.Tag.Lookup("default"); ok {
				fv.SetString(def)
			}
		}
	}
}
