package env

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func FuzzSetValueScalars(f *testing.F) {
	f.Add(uint8(0), "hello")
	f.Add(uint8(1), "true")
	f.Add(uint8(1), "not-a-bool")
	f.Add(uint8(2), "-9223372036854775808")
	f.Add(uint8(2), "9223372036854775808")
	f.Add(uint8(3), "18446744073709551615")
	f.Add(uint8(3), "-1")
	f.Add(uint8(4), "NaN")
	f.Add(uint8(4), "1.7976931348623157e+308")
	f.Add(uint8(5), "1h2m3.5s")
	f.Add(uint8(5), "9223372036854775808ns")

	f.Fuzz(func(t *testing.T, kind uint8, input string) {
		if len(input) > 256 {
			return
		}

		switch kind % 6 {
		case 0:
			var got string
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			if err != nil || got != input {
				t.Fatalf("string conversion = %q, %v; want %q", got, err, input)
			}
		case 1:
			var got bool
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := strconv.ParseBool(input)
			checkConversion(t, got, err, want, wantErr)
		case 2:
			var got int64
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := strconv.ParseInt(input, 10, 64)
			checkConversion(t, got, err, want, wantErr)
		case 3:
			var got uint64
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := strconv.ParseUint(input, 10, 64)
			checkConversion(t, got, err, want, wantErr)
		case 4:
			var got float64
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := strconv.ParseFloat(input, 64)
			if (err == nil) != (wantErr == nil) {
				t.Fatalf("float conversion error = %v, want %v", err, wantErr)
			}
			if err == nil && math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("float conversion = %v, want %v", got, want)
			}
		case 5:
			var got time.Duration
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := time.ParseDuration(input)
			checkConversion(t, got, err, want, wantErr)
		}
	})
}

func FuzzSetValueCollections(f *testing.F) {
	f.Add(uint8(0), "one, two,,three")
	f.Add(uint8(1), "1,-2,9223372036854775807")
	f.Add(uint8(1), "1,invalid,3")
	f.Add(uint8(2), "a=one,b=two,a=last")
	f.Add(uint8(2), "missing-equals")
	f.Add(uint8(3), "one=1,two=-2")
	f.Add(uint8(3), "one=invalid")

	f.Fuzz(func(t *testing.T, kind uint8, input string) {
		if len(input) > 512 {
			return
		}

		switch kind % 4 {
		case 0:
			var got []string
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want := splitAndTrim(input)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("[]string conversion = %#v, %v; want %#v", got, err, want)
			}
		case 1:
			var got []int64
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			parts := splitAndTrim(input)
			var want []int64
			var wantErr error
			if input != "" {
				want = make([]int64, len(parts))
				for i, part := range parts {
					want[i], wantErr = strconv.ParseInt(part, 10, 64)
					if wantErr != nil {
						break
					}
				}
			}
			checkCollectionConversion(t, got, err, want, wantErr)
		case 2:
			var got map[string]string
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := parseStringMap(input)
			checkCollectionConversion(t, got, err, want, wantErr)
		case 3:
			var got map[string]int64
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := parseIntMap(input)
			checkCollectionConversion(t, got, err, want, wantErr)
		}
	})
}

func FuzzSetValueNarrowNumbers(f *testing.F) {
	f.Add(uint8(0), "127")
	f.Add(uint8(0), "128")
	f.Add(uint8(1), "255")
	f.Add(uint8(1), "256")
	f.Add(uint8(2), "3.4028235e38")
	f.Add(uint8(2), "3.5e38")

	f.Fuzz(func(t *testing.T, kind uint8, input string) {
		if len(input) > 256 {
			return
		}
		switch kind % 3 {
		case 0:
			var got int8
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := strconv.ParseInt(input, 10, 8)
			checkConversion(t, got, err, int8(want), wantErr)
		case 1:
			var got uint8
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := strconv.ParseUint(input, 10, 8)
			checkConversion(t, got, err, uint8(want), wantErr)
		case 2:
			var got float32
			err := setValue(reflect.ValueOf(&got).Elem(), input)
			want, wantErr := strconv.ParseFloat(input, 32)
			if (err == nil) != (wantErr == nil) {
				t.Fatalf("float32 conversion error = %v, want %v", err, wantErr)
			}
			if err == nil && math.Float32bits(got) != math.Float32bits(float32(want)) {
				t.Fatalf("float32 conversion = %v, want %v", got, float32(want))
			}
		}
	})
}

func checkConversion[T comparable](t *testing.T, got T, gotErr error, want T, wantErr error) {
	t.Helper()
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("conversion error = %v, want %v", gotErr, wantErr)
	}
	if gotErr == nil && got != want {
		t.Fatalf("conversion = %v, want %v", got, want)
	}
}

func checkCollectionConversion[T any](t *testing.T, got T, gotErr error, want T, wantErr error) {
	t.Helper()
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("conversion error = %v, want %v", gotErr, wantErr)
	}
	if gotErr == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("conversion = %#v, want %#v", got, want)
	}
	if gotErr != nil && !reflect.ValueOf(got).IsNil() {
		t.Fatalf("failed conversion partially mutated target: %#v", got)
	}
}

func splitAndTrim(input string) []string {
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parseStringMap(input string) (map[string]string, error) {
	if input == "" {
		return nil, nil
	}
	result := make(map[string]string)
	for _, part := range strings.Split(input, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return nil, strconv.ErrSyntax
		}
		result[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return result, nil
}

func parseIntMap(input string) (map[string]int64, error) {
	if input == "" {
		return nil, nil
	}
	result := make(map[string]int64)
	for _, part := range strings.Split(input, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return nil, strconv.ErrSyntax
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, err
		}
		result[strings.TrimSpace(key)] = parsed
	}
	return result, nil
}
