package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

// Overlay lays a settings document, as Admin sends it, over s. A section or
// field the document leaves out keeps what s has: an older dashboard, a curl
// call that sets one switch, a field the page has never heard of. A list or a
// map the document carries replaces the one in s as a whole, built from
// nothing.
//
// That last part is what encoding/json does not do on its own. Decoding into
// a value that is already filled decodes the n-th element of a list over the
// n-th element there, so the fields an element leaves out (omitempty ones
// such as an MCP server's auth or description) stay from whatever stood at
// that place before, and a map is merged into instead of replaced. A server
// deleted in Admin handed its token to the one that moved into its place, and
// a header cleared in Admin never went. The lists and maps of s are shared
// with the live settings (Server.Settings is a shallow copy), so writing into
// them would also change those before the document was even validated;
// Overlay never writes into them.
func (s *Settings) Overlay(data []byte) error {
	return overlayStruct(reflect.ValueOf(s).Elem(), data)
}

// overlayStruct lays the JSON object data over the struct dst field by field:
// a struct field it names is overlaid in turn, every other field it names is
// decoded from nothing. JSON null leaves dst as it is, as encoding/json does
// for a struct.
func overlayStruct(dst reflect.Value, data []byte) error {
	if isNull(data) {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		// Not an object: encoding/json says why, in its own words.
		return json.Unmarshal(data, dst.Addr().Interface())
	}
	fields := jsonFields(dst)
	for key, raw := range object {
		field, ok := fields[key]
		if !ok {
			// encoding/json matches a key without regard to case when no
			// field has it exactly.
			for name, f := range fields {
				if strings.EqualFold(name, key) {
					field, ok = f, true
					break
				}
			}
		}
		if !ok {
			continue
		}
		if err := overlayField(field, raw); err != nil {
			return err
		}
	}
	return nil
}

var unmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

// overlayField sets one field from its JSON value.
func overlayField(field reflect.Value, raw json.RawMessage) error {
	if field.Kind() == reflect.Struct && !reflect.PointerTo(field.Type()).Implements(unmarshalerType) {
		return overlayStruct(field, raw)
	}
	if isNull(raw) {
		switch field.Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface:
			field.Set(reflect.Zero(field.Type()))
		}
		// Null leaves any other value as it is, as in encoding/json.
		return nil
	}
	fresh := reflect.New(field.Type())
	if err := json.Unmarshal(raw, fresh.Interface()); err != nil {
		return err
	}
	field.Set(fresh.Elem())
	return nil
}

// jsonFields is the settable fields of the struct v by their JSON names, with
// the fields of an embedded struct (Common) under their own names, as
// encoding/json promotes them.
func jsonFields(v reflect.Value) map[string]reflect.Value {
	out := map[string]reflect.Value{}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			for k, fv := range jsonFields(v.Field(i)) {
				if _, taken := out[k]; !taken {
					out[k] = fv
				}
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = v.Field(i)
	}
	return out
}

func isNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}
