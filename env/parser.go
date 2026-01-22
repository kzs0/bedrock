package env

import (
	"reflect"
	"strings"
)

// tag represents parsed struct tag options.
type tag struct {
	Name     string
	Default  string
	Required bool
	NotEmpty bool
}

// parseTag parses the env struct tag.
func parseTag(field reflect.StructField) (tag, error) {
	envTag := field.Tag.Get("env")
	if envTag == "" || envTag == "-" {
		return tag{}, nil
	}

	parts := strings.Split(envTag, ",")
	t := tag{
		Name: parts[0],
	}

	for _, part := range parts[1:] {
		switch part {
		case "required":
			t.Required = true
		case "notEmpty":
			t.NotEmpty = true
		}
	}

	if defaultVal := field.Tag.Get("envDefault"); defaultVal != "" {
		t.Default = defaultVal
	}

	return t, nil
}
