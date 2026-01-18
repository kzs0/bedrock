package config

import (
	"fmt"
	"reflect"
)

// Parse parses configuration from environment variables into a struct.
// The struct type T should have fields tagged with `env` tags.
func Parse[T any]() (T, error) {
	return ParseWithPrefix[T]("")
}

// ParseWithPrefix parses configuration with a prefix added to all env vars.
func ParseWithPrefix[T any](prefix string) (T, error) {
	var cfg T
	err := parseStruct(reflect.ValueOf(&cfg).Elem(), prefix)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// From validates an existing configuration struct.
// This is useful when configuration comes from sources other than env vars
// (e.g., Vault, config files).
func From[T any](cfg T) (T, error) {
	v := reflect.ValueOf(&cfg).Elem()
	if err := validateStruct(v, ""); err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// parseStruct parses environment variables into a struct.
func parseStruct(v reflect.Value, prefix string) error {
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldVal := v.Field(i)

		if !fieldVal.CanSet() {
			continue
		}

		// Handle nested structs
		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeOf(struct{}{}) {
			nestedPrefix := prefix
			if prefixTag := field.Tag.Get("envPrefix"); prefixTag != "" {
				nestedPrefix = prefix + prefixTag
			}
			if err := parseStruct(fieldVal, nestedPrefix); err != nil {
				return err
			}
			continue
		}

		// Parse env tag
		tag, err := parseTag(field)
		if err != nil {
			return fmt.Errorf("field %s: %w", field.Name, err)
		}

		if tag.Name == "" {
			continue // No env tag
		}

		envName := prefix + tag.Name
		if err := loadField(fieldVal, envName, tag); err != nil {
			return fmt.Errorf("field %s: %w", field.Name, err)
		}
	}

	return nil
}

// validateStruct validates required fields in a struct.
func validateStruct(v reflect.Value, prefix string) error {
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldVal := v.Field(i)

		// Handle nested structs
		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeOf(struct{}{}) {
			nestedPrefix := prefix
			if prefixTag := field.Tag.Get("envPrefix"); prefixTag != "" {
				nestedPrefix = prefix + prefixTag
			}
			if err := validateStruct(fieldVal, nestedPrefix); err != nil {
				return err
			}
			continue
		}

		tag, err := parseTag(field)
		if err != nil {
			return fmt.Errorf("field %s: %w", field.Name, err)
		}

		if tag.Required && isZero(fieldVal) {
			return fmt.Errorf("field %s: required but not set", field.Name)
		}

		if tag.NotEmpty && isEmptyString(fieldVal) {
			return fmt.Errorf("field %s: must not be empty", field.Name)
		}
	}

	return nil
}

// isZero checks if a value is its zero value.
func isZero(v reflect.Value) bool {
	return v.IsZero()
}

// isEmptyString checks if a value is an empty string.
func isEmptyString(v reflect.Value) bool {
	if v.Kind() == reflect.String {
		return v.String() == ""
	}
	return false
}
