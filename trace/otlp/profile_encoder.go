package otlp

import (
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal"
)

// EncodeProfile encodes a raw pprof profile byte slice into an OTLP Profiles
// protobuf request envelope (opentelemetry-proto profiles/v1development).
//
// The pprof bytes are embedded verbatim inside a profile_container.raw_profile
// field, which is how Grafana Alloy / Pyroscope-compatible OTLP backends accept
// pprof data without requiring a full pprof→OTLP translation.
//
// Wire format (field numbers from opentelemetry-proto profiles/v1development/profiles.proto):
//
//	ExportProfilesServiceRequest (1: resource_profiles)
//	  ResourceProfiles
//	    Resource (1: attributes)
//	    ScopeProfiles (2: scope_profiles)
//	      Profile (2: profiles)
//	        profile_id (2: bytes, 16 random bytes)
//	        start_time_unix_nano (3: fixed64)
//	        end_time_unix_nano (4: fixed64)
//	        attribute (9: KeyValue repeated — profile-level labels)
//	        profile_container (15: ProfileContainer)
//	          raw_profile (3: bytes — verbatim pprof bytes)
func EncodeProfile(service string, resource attr.Set, profileType string, rawPprof []byte, start, end time.Time) []byte {
	// Estimate capacity: resource overhead + pprof payload.
	est := 128 + len(service) + len(rawPprof)
	resource.Range(func(a attr.Attr) bool {
		est += len(a.Key) + 32
		return true
	})

	var b protoBuf
	b.data = make([]byte, 0, est)

	// ExportProfilesServiceRequest.resource_profiles (field 1)
	off := b.beginMsg(1)
	appendProfileResourceProfiles(&b, service, resource, profileType, rawPprof, start, end)
	b.endMsg(off)

	return b.data
}

func appendProfileResourceProfiles(b *protoBuf, service string, resource attr.Set, profileType string, rawPprof []byte, start, end time.Time) {
	// ResourceProfiles.resource (field 1)
	resOff := b.beginMsg(1)
	// Resource.attributes (field 1): service.name first
	b.appendKV(1, "service.name", attr.StringValue(service))
	resource.Range(func(a attr.Attr) bool {
		b.appendKV(1, a.Key, a.Value)
		return true
	})
	b.endMsg(resOff)

	// ResourceProfiles.scope_profiles (field 2)
	spOff := b.beginMsg(2)
	appendProfileScopeProfiles(b, profileType, rawPprof, start, end)
	b.endMsg(spOff)
}

func appendProfileScopeProfiles(b *protoBuf, profileType string, rawPprof []byte, start, end time.Time) {
	// ScopeProfiles.profiles (field 2)
	pOff := b.beginMsg(2)
	appendProfileMsg(b, profileType, rawPprof, start, end)
	b.endMsg(pOff)
}

func appendProfileMsg(b *protoBuf, profileType string, rawPprof []byte, start, end time.Time) {
	// Profile.profile_id (field 2): 16 random bytes
	id := internal.NewTraceID() // reuse 16-byte random ID generation
	b.appendBytes(2, id[:])

	// Profile.start_time_unix_nano (field 3)
	b.appendFixed64(3, uint64(start.UnixNano()))

	// Profile.end_time_unix_nano (field 4)
	b.appendFixed64(4, uint64(end.UnixNano()))

	// Profile.attribute (field 9): profile type label
	b.appendKV(9, "profile.type", attr.StringValue(profileType))

	// Profile.profile_container (field 15)
	pcOff := b.beginMsg(15)
	// ProfileContainer.raw_profile (field 3): verbatim pprof bytes
	b.appendBytes(3, rawPprof)
	b.endMsg(pcOff)
}
