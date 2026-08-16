package env

import (
	"fmt"
	"reflect"
	"strings"
)

// tag represents parsed struct tag options.
type tag struct {
	Name     string
	Default  string
	Required bool
	NotEmpty bool
	Skip     bool
}

// parseTag parses the env struct tag.
func parseTag(field reflect.StructField) (tag, error) {
	envTag, present := field.Tag.Lookup("env")
	if !present {
		return tag{}, nil
	}
	if envTag == "-" {
		return tag{Skip: true}, nil
	}

	parts := strings.Split(envTag, ",")
	if parts[0] == "" {
		return tag{}, fmt.Errorf("env tag name must not be empty")
	}
	if parts[0] == "-" {
		return tag{}, fmt.Errorf("env tag %q cannot have options", parts[0])
	}
	t := tag{
		Name: parts[0],
	}

	for _, part := range parts[1:] {
		switch part {
		case "required":
			t.Required = true
		case "notEmpty":
			t.NotEmpty = true
		default:
			return tag{}, fmt.Errorf("unknown env tag option %q", part)
		}
	}

	if defaultVal := field.Tag.Get("envDefault"); defaultVal != "" {
		t.Default = defaultVal
	}

	return t, nil
}
