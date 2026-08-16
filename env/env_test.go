package env

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSetMap(t *testing.T) {
	m := reflect.New(reflect.TypeOf(map[string]string{})).Elem()
	err := setMap(m, "key1=val1,key2=val2")
	if err != nil {
		t.Fatalf("setMap error: %v", err)
	}
	if m.Len() != 2 {
		t.Errorf("expected 2 entries, got %d", m.Len())
	}
	if m.MapIndex(reflect.ValueOf("key1")).String() != "val1" {
		t.Error("expected key1=val1")
	}
	if m.MapIndex(reflect.ValueOf("key2")).String() != "val2" {
		t.Error("expected key2=val2")
	}
}

func TestSetMap_Empty(t *testing.T) {
	m := reflect.New(reflect.TypeOf(map[string]string{})).Elem()
	err := setMap(m, "")
	if err != nil {
		t.Fatalf("setMap error: %v", err)
	}
	if m.Len() != 0 {
		t.Errorf("expected 0 entries for empty string, got %d", m.Len())
	}
}

func TestSetMap_InvalidEntry(t *testing.T) {
	m := reflect.New(reflect.TypeOf(map[string]string{})).Elem()
	err := setMap(m, "noequalssign")
	if err == nil {
		t.Error("expected error for invalid map entry")
	}
}

func TestIsEmptyString(t *testing.T) {
	// String type
	v := reflect.ValueOf("")
	if !isEmptyString(v) {
		t.Error("expected true for empty string")
	}
	v = reflect.ValueOf("notempty")
	if isEmptyString(v) {
		t.Error("expected false for non-empty string")
	}
	// Non-string type
	v = reflect.ValueOf(42)
	if isEmptyString(v) {
		t.Error("expected false for non-string type")
	}
}

func TestSetValue_Uint(t *testing.T) {
	v := reflect.New(reflect.TypeOf(uint(0))).Elem()
	if err := setValue(v, "42"); err != nil {
		t.Fatalf("setValue uint error: %v", err)
	}
	if v.Uint() != 42 {
		t.Errorf("expected 42, got %d", v.Uint())
	}
}

func TestSetValue_Float(t *testing.T) {
	v := reflect.New(reflect.TypeOf(float64(0))).Elem()
	if err := setValue(v, "3.14"); err != nil {
		t.Fatalf("setValue float error: %v", err)
	}
	if v.Float() != 3.14 {
		t.Errorf("expected 3.14, got %f", v.Float())
	}
}

func TestSetValue_UnsupportedType(t *testing.T) {
	type custom struct{}
	v := reflect.New(reflect.TypeOf(custom{})).Elem()
	err := setValue(v, "test")
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestSetValue_InvalidBool(t *testing.T) {
	v := reflect.New(reflect.TypeOf(false)).Elem()
	err := setValue(v, "notabool")
	if err == nil {
		t.Error("expected error for invalid bool")
	}
}

func TestSetValue_InvalidInt(t *testing.T) {
	v := reflect.New(reflect.TypeOf(0)).Elem()
	err := setValue(v, "notanint")
	if err == nil {
		t.Error("expected error for invalid int")
	}
}

func TestSetValue_InvalidUint(t *testing.T) {
	v := reflect.New(reflect.TypeOf(uint(0))).Elem()
	err := setValue(v, "notauint")
	if err == nil {
		t.Error("expected error for invalid uint")
	}
}

func TestSetValue_InvalidFloat(t *testing.T) {
	v := reflect.New(reflect.TypeOf(float64(0))).Elem()
	err := setValue(v, "notafloat")
	if err == nil {
		t.Error("expected error for invalid float")
	}
}

func TestSetValue_RejectsNumericOverflowAtTargetWidth(t *testing.T) {
	tests := []struct {
		name   string
		target any
		input  string
		want   string
	}{
		{name: "int8 high", target: new(int8), input: "128", want: "value out of range"},
		{name: "int8 low", target: new(int8), input: "-129", want: "value out of range"},
		{name: "uint8 high", target: new(uint8), input: "256", want: "value out of range"},
		{name: "uint8 negative", target: new(uint8), input: "-1", want: "invalid syntax"},
		{name: "float32 high", target: new(float32), input: "3.5e38", want: "value out of range"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := reflect.ValueOf(tt.target).Elem()
			if err := setValue(value, tt.input); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("setValue(%s, %q) error = %v, want range error", value.Type(), tt.input, err)
			}
			if !value.IsZero() {
				t.Fatalf("failed conversion mutated %s to %v", value.Type(), value.Interface())
			}
		})
	}
}

func TestSetValue_NamedNumericTypesUseUnderlyingWidth(t *testing.T) {
	type smallInt int8
	type smallFloat float32

	var i smallInt
	if err := setValue(reflect.ValueOf(&i).Elem(), "127"); err != nil || i != 127 {
		t.Fatalf("named int8 conversion = %d, %v", i, err)
	}
	if err := setValue(reflect.ValueOf(&i).Elem(), "128"); err == nil {
		t.Fatal("named int8 overflow was accepted")
	}

	var f smallFloat
	if err := setValue(reflect.ValueOf(&f).Elem(), "3.5e38"); err == nil {
		t.Fatal("named float32 overflow was accepted")
	}
}

func TestSetValue_UintptrAndDurationAlias(t *testing.T) {
	var pointer uintptr
	if err := setValue(reflect.ValueOf(&pointer).Elem(), "42"); err != nil || pointer != 42 {
		t.Fatalf("uintptr conversion = %d, %v", pointer, err)
	}

	type durationAlias = time.Duration
	var duration durationAlias
	if err := setValue(reflect.ValueOf(&duration).Elem(), "1.5s"); err != nil || duration != 1500*time.Millisecond {
		t.Fatalf("duration alias conversion = %s, %v", duration, err)
	}
}

func TestSetValue_ErrorPreservesExistingTarget(t *testing.T) {
	value := int8(7)
	if err := setValue(reflect.ValueOf(&value).Elem(), "128"); err == nil {
		t.Fatal("int8 overflow was accepted")
	}
	if value != 7 {
		t.Fatalf("failed conversion changed target to %d", value)
	}
}

func TestParseTagRejectsUnknownOption(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,required,unknown"`
	}
	field, _ := reflect.TypeOf(config{}).FieldByName("Value")
	if _, err := parseTag(field); err == nil || !strings.Contains(err.Error(), `unknown env tag option "unknown"`) {
		t.Fatalf("parseTag error = %v, want unknown option", err)
	}
	if _, err := Parse[config](); err == nil || !strings.Contains(err.Error(), `field Value: unknown env tag option "unknown"`) {
		t.Fatalf("Parse error = %v, want field-scoped unknown option", err)
	}
}

func TestParseTagRejectsMissingNameAndEmptyOptions(t *testing.T) {
	tests := []struct {
		name string
		tag  reflect.StructTag
		want string
	}{
		{name: "explicit empty tag", tag: `env:""`, want: "env tag name must not be empty"},
		{name: "options without name", tag: `env:",required"`, want: "env tag name must not be empty"},
		{name: "trailing empty option", tag: `env:"VALUE,"`, want: `unknown env tag option ""`},
		{name: "skip marker with option", tag: `env:"-,required"`, want: `env tag "-" cannot have options`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field := reflect.StructField{Name: "Value", Type: reflect.TypeOf(""), Tag: tt.tag}
			if _, err := parseTag(field); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseTag error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseTagAbsentAndSkipMarker(t *testing.T) {
	untagged := reflect.StructField{Name: "Untagged", Type: reflect.TypeOf("")}
	if got, err := parseTag(untagged); err != nil || got != (tag{}) {
		t.Fatalf("parseTag(untagged) = %#v, %v; want zero tag", got, err)
	}
	skipped := reflect.StructField{Name: "Skipped", Type: reflect.TypeOf(""), Tag: `env:"-"`}
	if got, err := parseTag(skipped); err != nil || !got.Skip {
		t.Fatalf("parseTag(skipped) = %#v, %v; want skip tag", got, err)
	}
}

func TestNestedStructTagsAreValidatedBeforeDescent(t *testing.T) {
	type nested struct {
		Value string `env:"NESTED_VALUE"`
	}
	type invalidConfig struct {
		Nested nested `env:"NESTED,unknown"`
	}

	if _, err := Parse[invalidConfig](); err == nil || !strings.Contains(err.Error(), `field Nested: unknown env tag option "unknown"`) {
		t.Fatalf("Parse error = %v, want nested field tag error", err)
	}
	if _, err := From(invalidConfig{}); err == nil || !strings.Contains(err.Error(), `field Nested: unknown env tag option "unknown"`) {
		t.Fatalf("From error = %v, want nested field tag error", err)
	}
}

func TestNestedStructSkipMarkerSkipsEntireSubtree(t *testing.T) {
	type nested struct {
		Required string `env:"SKIPPED_REQUIRED,required"`
	}
	type config struct {
		Nested nested `env:"-"`
	}

	t.Setenv("SKIPPED_REQUIRED", "must-not-load")
	parsed, err := Parse[config]()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Nested.Required != "" {
		t.Fatalf("skipped subtree loaded value %q", parsed.Nested.Required)
	}
	if _, err := From(config{}); err != nil {
		t.Fatalf("From validated skipped subtree: %v", err)
	}
}

func TestLoadField_Required(t *testing.T) {
	// Register cleanup, then unset for the test
	t.Setenv("TEST_REQUIRED_FIELD", "")
	_ = os.Unsetenv("TEST_REQUIRED_FIELD")
	v := reflect.New(reflect.TypeOf("")).Elem()
	err := loadField(v, "TEST_REQUIRED_FIELD", tag{Required: true})
	if err == nil {
		t.Error("expected error for required unset field")
	}
}

func TestLoadField_NotEmpty(t *testing.T) {
	t.Setenv("TEST_NOTEMPTY_FIELD", "")
	_ = os.Unsetenv("TEST_NOTEMPTY_FIELD")
	v := reflect.New(reflect.TypeOf("")).Elem()
	err := loadField(v, "TEST_NOTEMPTY_FIELD", tag{NotEmpty: true})
	if err == nil {
		t.Error("expected error for not-empty unset field")
	}
}

func TestLoadField_Default(t *testing.T) {
	t.Setenv("TEST_DEFAULT_FIELD", "")
	_ = os.Unsetenv("TEST_DEFAULT_FIELD")
	v := reflect.New(reflect.TypeOf("")).Elem()
	err := loadField(v, "TEST_DEFAULT_FIELD", tag{Default: "default_val"})
	if err != nil {
		t.Fatalf("loadField error: %v", err)
	}
	if v.String() != "default_val" {
		t.Errorf("expected 'default_val', got %q", v.String())
	}
}

func TestLoadField_EnvVar(t *testing.T) {
	t.Setenv("TEST_ENV_VAR", "hello")

	v := reflect.New(reflect.TypeOf("")).Elem()
	err := loadField(v, "TEST_ENV_VAR", tag{})
	if err != nil {
		t.Fatalf("loadField error: %v", err)
	}
	if v.String() != "hello" {
		t.Errorf("expected 'hello', got %q", v.String())
	}
}

func TestValidateStruct(t *testing.T) {
	type Config struct {
		Name    string `env:"NAME,required"`
		Port    int    `env:"PORT" envDefault:"8080"`
		Verbose bool   `env:"VERBOSE"`
	}

	// Required field not set (zero value)
	cfg := Config{}
	v := reflect.ValueOf(&cfg).Elem()
	err := validateStruct(v, "")
	if err == nil {
		t.Error("expected error for required field not set")
	}

	// Required field set
	cfg.Name = "test"
	v = reflect.ValueOf(&cfg).Elem()
	err = validateStruct(v, "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFrom_Validates(t *testing.T) {
	type Config struct {
		Name string `env:"NAME,required"`
	}

	// Empty required field
	cfg, err := From(Config{})
	if err == nil {
		t.Error("expected validation error")
	}
	_ = cfg

	// Valid
	cfg2, err := From(Config{Name: "test"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cfg2.Name != "test" {
		t.Errorf("expected 'test', got %q", cfg2.Name)
	}
}

func TestSetMap_IntMap(t *testing.T) {
	m := reflect.New(reflect.TypeOf(map[string]int{})).Elem()
	err := setMap(m, "a=1,b=2")
	if err != nil {
		t.Fatalf("setMap error: %v", err)
	}
	if m.Len() != 2 {
		t.Errorf("expected 2 entries, got %d", m.Len())
	}
}
