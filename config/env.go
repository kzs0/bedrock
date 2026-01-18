package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// loadField loads a value from an environment variable into a field.
func loadField(v reflect.Value, envName string, t tag) error {
	envVal := os.Getenv(envName)

	if envVal == "" {
		if t.Default != "" {
			envVal = t.Default
		} else if t.Required {
			return fmt.Errorf("required environment variable %s not set", envName)
		} else if t.NotEmpty {
			return fmt.Errorf("environment variable %s must not be empty", envName)
		} else {
			return nil // No value, no default, not required
		}
	}

	return setValue(v, envVal)
}

// setValue sets a reflect.Value from a string.
func setValue(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
		return nil

	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("invalid bool: %w", err)
		}
		v.SetBool(b)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Handle time.Duration specially
		if v.Type() == reflect.TypeOf(time.Duration(0)) {
			d, err := time.ParseDuration(s)
			if err != nil {
				return fmt.Errorf("invalid duration: %w", err)
			}
			v.SetInt(int64(d))
			return nil
		}
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int: %w", err)
		}
		v.SetInt(i)
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid uint: %w", err)
		}
		v.SetUint(u)
		return nil

	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("invalid float: %w", err)
		}
		v.SetFloat(f)
		return nil

	case reflect.Slice:
		return setSlice(v, s)

	case reflect.Map:
		return setMap(v, s)

	default:
		return fmt.Errorf("unsupported type: %s", v.Type())
	}
}

// setSlice sets a slice value from a comma-separated string.
func setSlice(v reflect.Value, s string) error {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	slice := reflect.MakeSlice(v.Type(), len(parts), len(parts))

	for i, part := range parts {
		part = strings.TrimSpace(part)
		if err := setValue(slice.Index(i), part); err != nil {
			return fmt.Errorf("element %d: %w", i, err)
		}
	}

	v.Set(slice)
	return nil
}

// setMap sets a map value from a comma-separated key=value string.
func setMap(v reflect.Value, s string) error {
	if s == "" {
		return nil
	}

	m := reflect.MakeMap(v.Type())

	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("invalid map entry: %s", part)
		}

		key := reflect.New(v.Type().Key()).Elem()
		if err := setValue(key, strings.TrimSpace(kv[0])); err != nil {
			return fmt.Errorf("map key: %w", err)
		}

		val := reflect.New(v.Type().Elem()).Elem()
		if err := setValue(val, strings.TrimSpace(kv[1])); err != nil {
			return fmt.Errorf("map value: %w", err)
		}

		m.SetMapIndex(key, val)
	}

	v.Set(m)
	return nil
}
