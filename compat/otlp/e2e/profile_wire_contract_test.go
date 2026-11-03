package e2e

import (
	"encoding/binary"
	"strings"
	"testing"
)

func profileWireInteger(number int, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(number<<3)), value)
}

func profileWireMessage(number int, raw []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(number<<3|2))
	out = binary.AppendUvarint(out, uint64(len(raw)))
	return append(out, raw...)
}

func profileWireJoin(parts ...[]byte) []byte {
	var raw []byte
	for _, part := range parts {
		raw = append(raw, part...)
	}
	return raw
}

func profileWireDocument(sample []byte, records ...[]byte) []byte {
	raw := profileWireMessage(1, profileWireJoin(profileWireInteger(1, 1), profileWireInteger(2, 2)))
	raw = append(raw, profileWireMessage(2, sample)...)
	for _, s := range []string{"", "samples", "count", "label", "text", "bytes", "wire.workload", "/wire/source.go", "wire-build"} {
		raw = append(raw, profileWireMessage(6, []byte(s))...)
	}
	return profileWireJoin(append([][]byte{raw}, records...)...)
}

func TestProfileWireMalformedInputsAndReferences(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "privacy"})
	value := profileWireInteger(2, 7)
	function := profileWireMessage(5, profileWireInteger(1, 1))
	mapping := profileWireMessage(3, profileWireInteger(1, 1))
	location := profileWireMessage(4, profileWireInteger(1, 1))
	for name, raw := range map[string][]byte{
		"tag_zero":                     {0},
		"tag_overflow":                 binary.AppendUvarint(nil, uint64(1<<29)<<3),
		"truncated_tag":                {0x80},
		"truncated_scalar":             {8, 0x80},
		"short_fixed64":                {9, 1, 2},
		"short_fixed32":                {13, 1, 2},
		"truncated_length":             {10, 0x80},
		"length_exceeds_input":         {10, 8, 1},
		"unsupported_group_wire":       {11},
		"string_wrong_wire":            profileWireInteger(6, 1),
		"string_invalid_utf8":          profileWireMessage(6, []byte{0xff}),
		"child_wrong_wire":             profileWireInteger(1, 1),
		"known_scalar_wrong_wire":      profileWireMessage(1, profileWireMessage(1, nil)),
		"repeated_wrong_wire":          profileWireMessage(2, []byte{13, 0, 0, 0, 0}),
		"packed_truncated":             profileWireMessage(2, profileWireMessage(2, []byte{0x80})),
		"missing_empty_string":         profileWireMessage(6, []byte("first")),
		"missing_sample_types":         profileWireMessage(6, nil),
		"sample_dimension_mismatch":    profileWireDocument(nil),
		"sample_location_missing":      profileWireDocument(profileWireJoin(value, profileWireInteger(1, 1))),
		"zero_function_id":             profileWireDocument(value, profileWireMessage(5, nil)),
		"duplicate_function_id":        profileWireDocument(value, function, function),
		"zero_mapping_id":              profileWireDocument(value, profileWireMessage(3, nil)),
		"duplicate_mapping_id":         profileWireDocument(value, mapping, mapping),
		"zero_location_id":             profileWireDocument(value, profileWireMessage(4, nil)),
		"duplicate_location_id":        profileWireDocument(value, location, location),
		"location_mapping_missing":     profileWireDocument(value, profileWireMessage(4, profileWireJoin(profileWireInteger(1, 1), profileWireInteger(2, 7)))),
		"location_function_missing":    profileWireDocument(value, profileWireMessage(4, profileWireJoin(profileWireInteger(1, 1), profileWireMessage(4, profileWireInteger(1, 7))))),
		"function_string_out_of_range": profileWireDocument(value, profileWireMessage(5, profileWireJoin(profileWireInteger(1, 1), profileWireInteger(2, 99)))),
		"mapping_build_out_of_range":   profileWireDocument(value, profileWireMessage(3, profileWireJoin(profileWireInteger(1, 1), profileWireInteger(6, 99)))),
		"comment_string_out_of_range":  profileWireDocument(value, profileWireInteger(13, 99)),
		"ambiguous_label_value":        profileWireDocument(profileWireJoin(value, profileWireMessage(3, profileWireJoin(profileWireInteger(1, 3), profileWireInteger(2, 4), profileWireInteger(3, 1))))),
	} {
		t.Run(name, func(t *testing.T) {
			body := contractProfilePost(t, app, "", raw, 400)
			if !strings.Contains(string(body), "invalid") {
				t.Fatalf("malformed profile omitted validation error: %s", body)
			}
		})
	}
	decoded := contractDecodeProfile(t, contractProfilePost(t, app, "", profileWireDocument(value), 200))
	profiles := decoded.ResourceProfiles[0].ScopeProfiles[0].Profiles
	if len(profiles) != 1 || len(profiles[0].Samples) != 1 || len(profiles[0].Samples[0].Values) != 1 || profiles[0].Samples[0].Values[0] != 7 {
		t.Fatalf("malformed input poisoned later conversion: %v", profiles)
	}
	app.Stop(t)
}

func TestProfileWirePackedUnpackedAndUnknownFields(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "privacy"})
	for _, packed := range []bool{false, true} {
		name := "unpacked"
		if packed {
			name = "packed"
		}
		t.Run(name, func(t *testing.T) {
			var sample []byte
			if packed {
				sample = profileWireJoin(profileWireMessage(1, []byte{1}), profileWireMessage(2, []byte{7}))
			} else {
				sample = profileWireJoin(profileWireInteger(1, 1), profileWireInteger(2, 7))
			}
			sample = append(sample, profileWireMessage(3, profileWireInteger(1, 3))...)
			function := profileWireMessage(5, profileWireJoin(profileWireInteger(1, 1), profileWireInteger(2, 6), profileWireInteger(3, 6), profileWireInteger(4, 7), profileWireInteger(5, 10)))
			mapping := profileWireMessage(3, profileWireJoin(profileWireInteger(1, 1), profileWireInteger(2, 100), profileWireInteger(3, 200), profileWireInteger(4, 5), profileWireInteger(5, 7), profileWireInteger(6, 8), profileWireInteger(7, 1), profileWireInteger(8, 1), profileWireInteger(9, 1), profileWireInteger(10, 1)))
			location := profileWireMessage(4, profileWireJoin(profileWireInteger(1, 1), profileWireInteger(2, 1), profileWireInteger(3, 123), profileWireInteger(5, 1), profileWireMessage(4, profileWireJoin(profileWireInteger(1, 1), profileWireInteger(2, 11), profileWireInteger(3, 1)))))
			unknown := profileWireJoin(profileWireInteger(99, 1), []byte{0xa1, 6, 0, 0, 0, 0, 0, 0, 0, 0}, []byte{0xa5, 6, 0, 0, 0, 0}, profileWireMessage(102, []byte("ignored")))
			raw := profileWireDocument(sample, function, mapping, location, profileWireInteger(7, 3), profileWireInteger(8, 3), profileWireInteger(14, 1), profileWireInteger(13, 4), unknown)
			decoded := contractDecodeProfile(t, contractProfilePost(t, app, "", raw, 200))
			profiles := decoded.ResourceProfiles[0].ScopeProfiles[0].Profiles
			if len(profiles) != 1 || len(profiles[0].Samples) != 1 || len(profiles[0].Samples[0].Values) != 1 || profiles[0].Samples[0].Values[0] != 7 || len(profiles[0].Samples[0].AttributeIndices) != 0 || !strings.Contains(strings.Join(decoded.Dictionary.StringTable, "\n"), "wire.workload") {
				t.Fatalf("wire compatibility changed decoded content: %v", decoded)
			}
			stackIndex := profiles[0].Samples[0].StackIndex
			if stackIndex <= 0 || int(stackIndex) >= len(decoded.Dictionary.StackTable) {
				t.Fatalf("stack reference missing: %d", stackIndex)
			}
			locations := decoded.Dictionary.StackTable[stackIndex].LocationIndices
			if len(locations) != 1 || locations[0] <= 0 || int(locations[0]) >= len(decoded.Dictionary.LocationTable) {
				t.Fatalf("location references: %v", locations)
			}
			locationValue := decoded.Dictionary.LocationTable[locations[0]]
			if locationValue.Address != 123 || len(locationValue.Lines) != 1 || locationValue.Lines[0].Line != 11 || locationValue.Lines[0].FunctionIndex <= 0 || int(locationValue.Lines[0].FunctionIndex) >= len(decoded.Dictionary.FunctionTable) || locationValue.MappingIndex <= 0 || int(locationValue.MappingIndex) >= len(decoded.Dictionary.MappingTable) {
				t.Fatalf("resolved location: %v", locationValue)
			}
			functionValue := decoded.Dictionary.FunctionTable[locationValue.Lines[0].FunctionIndex]
			if profileString(t, decoded.Dictionary, functionValue.NameStrindex) != "wire.workload" || functionValue.StartLine != 10 {
				t.Fatalf("resolved function: %v", functionValue)
			}
			mappingValue := decoded.Dictionary.MappingTable[locationValue.MappingIndex]
			if mappingValue.MemoryStart != 100 || mappingValue.MemoryLimit != 200 || mappingValue.FileOffset != 5 || profileString(t, decoded.Dictionary, mappingValue.FilenameStrindex) != "source.go" {
				t.Fatalf("resolved mapping: %v", mappingValue)
			}
		})
	}
	app.Stop(t)
}
