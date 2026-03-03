package env

import (
	"os"
	"reflect"
	"testing"
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
