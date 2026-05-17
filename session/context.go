package session

import "context"

// timezoneKey is an unexported zero-size type used as a context key.
// Struct-based (not string) because TimezoneKey is exported: exporting a
// string-keyed var is a footgun — any package can shadow it with any string
// value. With an unexported struct type, external packages can read
// TimezoneKey but cannot construct a timezoneKey{} value themselves, so the
// key is effectively immutable from outside this package.
type timezoneKey struct{}

// TimezoneKey is the context key for an IANA timezone string (e.g.
// "Asia/Jakarta"). Exported so session implementations (zep, etc.) can read
// timezone from context without importing identity or any caller-specific
// package.
var TimezoneKey = timezoneKey{}

// TimezoneFromContext returns the IANA timezone string stored under TimezoneKey.
// Returns false if the value is absent or empty — never returns an empty
// string with ok=true.
func TimezoneFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(TimezoneKey).(string)
	return v, ok && v != ""
}
