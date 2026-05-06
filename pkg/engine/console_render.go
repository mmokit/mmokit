package engine

import (
	"fmt"
	"reflect"
	"strings"
)

// resultRenderers maps result types (by reflect.Type) to custom renderers.
// Registered via registerResultRenderer; checked first in renderResult.
var resultRenderers = map[reflect.Type]func(any) string{}

// registerResultRenderer registers a custom text renderer for a given result type.
// proto is a zero value of the type (e.g. configGetResult{}).
func registerResultRenderer(proto any, fn func(any) string) {
	resultRenderers[reflect.TypeOf(proto)] = fn
}

// renderResult formats a typed result struct into human-readable console text.
// If a custom renderer is registered for the type, it is used first.
// If the result contains a slice field tagged cmd:"table", that slice is rendered
// as a Table. Otherwise each exported field is rendered as "Field: Value".
func renderResult(v any) string {
	if v == nil {
		return ""
	}
	if fn, ok := resultRenderers[reflect.TypeOf(v)]; ok {
		return fn(v)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return fmt.Sprintf("  %v\n", v)
	}
	rt := rv.Type()

	// Pre-formatted output convention: a single exported `Output string` field
	// holds text that is already formatted for display. Print it raw rather
	// than prefixing it with "Output: " — the prefix is noise and misaligns
	// the first line of any tabular output the handler built.
	{
		exportedIdx := -1
		for i := range rt.NumField() {
			if !rt.Field(i).IsExported() {
				continue
			}
			if exportedIdx >= 0 {
				exportedIdx = -1
				break
			}
			exportedIdx = i
		}
		if exportedIdx >= 0 {
			f := rt.Field(exportedIdx)
			if f.Name == "Output" && f.Type.Kind() == reflect.String {
				return rv.Field(exportedIdx).String()
			}
		}
	}

	// Check for a table-tagged slice field.
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("cmd")
		if !containsTagFlag(tag, "table") {
			continue
		}
		fv := rv.Field(i)
		if fv.Kind() != reflect.Slice {
			continue
		}
		return renderSliceAsTable(fv)
	}

	// Fallback: render as key: value lines.
	var b strings.Builder
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		fmt.Fprintf(&b, "  %s: %v\n", f.Name, rv.Field(i).Interface())
	}
	return b.String()
}

// renderSliceAsTable renders a []SomeStruct as a Table using field names as headers.
func renderSliceAsTable(sv reflect.Value) string {
	if sv.Len() == 0 {
		return "  (empty)\n"
	}
	elemType := sv.Type().Elem()
	if elemType.Kind() == reflect.Pointer {
		elemType = elemType.Elem()
	}
	if elemType.Kind() != reflect.Struct {
		// Flat slice: just list values.
		var b strings.Builder
		for i := range sv.Len() {
			fmt.Fprintf(&b, "  %v\n", sv.Index(i).Interface())
		}
		return b.String()
	}

	// Collect headers from exported fields.
	var headers []string
	for i := range elemType.NumField() {
		if elemType.Field(i).IsExported() {
			headers = append(headers, elemType.Field(i).Name)
		}
	}
	t := NewTable(headers...)
	for i := range sv.Len() {
		elem := sv.Index(i)
		if elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		row := make([]any, len(headers))
		for j, h := range headers {
			row[j] = elem.FieldByName(h).Interface()
		}
		t.Row(row...)
	}
	return t.String()
}

// containsTagFlag checks whether tag contains a bare flag word.
func containsTagFlag(tag, flag string) bool {
	for _, part := range strings.Split(tag, ",") {
		if strings.TrimSpace(part) == flag {
			return true
		}
	}
	return false
}
