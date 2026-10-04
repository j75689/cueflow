package connector

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type FieldType string

const (
	FieldString     FieldType = "string"
	FieldPassword   FieldType = "password"
	FieldText       FieldType = "text"
	FieldNumber     FieldType = "number"
	FieldBool       FieldType = "bool"
	FieldSelect     FieldType = "select"
	FieldStringList FieldType = "string_list"
	FieldKV         FieldType = "kv"
	FieldJSON       FieldType = "json"
)

// Field describes one configuration input. The UI renders forms from these.
type Field struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Secret      bool      `json:"secret"`
	Default     any       `json:"default"`
	Options     []string  `json:"options"`
	Placeholder string    `json:"placeholder,omitempty"`
	Help        string    `json:"help,omitempty"`
}

// Spec is a connector type's descriptor.
type Spec struct {
	Type        string  `json:"type"`
	Kind        Kind    `json:"kind"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Fields      []Field `json:"fields"`
}

func (s Spec) field(key string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// SecretKeys lists the keys of secret fields.
func (s Spec) SecretKeys() []string {
	var out []string
	for _, f := range s.Fields {
		if f.Secret {
			out = append(out, f.Key)
		}
	}
	return out
}

// Split separates user-submitted settings into plain config and secrets. Unknown keys are dropped.
func (s Spec) Split(all map[string]any) (config, secrets map[string]any) {
	config, secrets = map[string]any{}, map[string]any{}
	for k, v := range all {
		f, ok := s.field(k)
		if !ok {
			continue
		}
		if f.Secret {
			secrets[k] = v
		} else {
			config[k] = v
		}
	}
	return config, secrets
}

// WithDefaults returns a copy of cfg with defaults filled in for missing or empty fields.
func (s Spec) WithDefaults(cfg Config) Config {
	out := Config{}
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range s.Fields {
		if f.Default == nil {
			continue
		}
		if v, ok := out[f.Key]; !ok || v == nil || v == "" {
			out[f.Key] = f.Default
		}
	}
	return out
}

// Validate checks required fields and value types.
func (s Spec) Validate(cfg Config) error {
	for _, f := range s.Fields {
		v, present := cfg[f.Key]
		if !present || v == nil || v == "" {
			if f.Required {
				return fmt.Errorf("%s: %q is required", s.Name, f.Label)
			}
			continue
		}
		switch f.Type {
		case FieldNumber:
			if _, err := toFloat(v); err != nil {
				return fmt.Errorf("%s: %q must be a number", s.Name, f.Label)
			}
		case FieldBool:
			if _, err := toBool(v); err != nil {
				return fmt.Errorf("%s: %q must be true or false", s.Name, f.Label)
			}
		case FieldSelect:
			str := fmt.Sprint(v)
			ok := false
			for _, o := range f.Options {
				if o == str {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("%s: %q must be one of %s", s.Name, f.Label, strings.Join(f.Options, ", "))
			}
		case FieldStringList:
			if _, ok := toStringList(v); !ok {
				return fmt.Errorf("%s: %q must be a list of strings", s.Name, f.Label)
			}
		case FieldKV:
			if _, ok := toKV(v); !ok {
				return fmt.Errorf("%s: %q must be an object of strings", s.Name, f.Label)
			}
		}
	}
	return nil
}

// Config is a connector's merged settings (plain config plus decrypted secrets).
type Config map[string]any

func (c Config) String(key string) string {
	v, ok := c[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return fmt.Sprint(v)
}

func (c Config) Int(key string, def int) int {
	if f, err := toFloat(c[key]); err == nil {
		return int(f)
	}
	return def
}

func (c Config) Bool(key string) bool {
	b, _ := toBool(c[key])
	return b
}

func (c Config) StringList(key string) []string {
	l, _ := toStringList(c[key])
	return l
}

func (c Config) KV(key string) map[string]string {
	m, _ := toKV(c[key])
	return m
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		return strconv.ParseFloat(strings.TrimSpace(n), 64)
	}
	return 0, fmt.Errorf("not a number")
}

func toBool(v any) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		return strconv.ParseBool(strings.TrimSpace(b))
	case nil:
		return false, nil
	}
	return false, fmt.Errorf("not a bool")
}

func toStringList(v any) ([]string, bool) {
	switch l := v.(type) {
	case nil:
		return nil, true
	case []string:
		return l, true
	case []any:
		out := make([]string, 0, len(l))
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, true
	case string:
		// Accept newline/comma separated text from simple clients.
		var out []string
		for _, s := range strings.FieldsFunc(l, func(r rune) bool { return r == '\n' || r == ',' }) {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, false
}

func toKV(v any) (map[string]string, bool) {
	switch m := v.(type) {
	case nil:
		return map[string]string{}, true
	case map[string]string:
		return m, true
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, e := range m {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out[k] = s
		}
		return out, true
	}
	return nil, false
}
