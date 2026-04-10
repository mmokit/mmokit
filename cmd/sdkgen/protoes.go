package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ProtoMessage holds parsed field info from a .d.ts file.
type ProtoMessage struct {
	FullName   string       // e.g. "basicpb.BasicLoginMsg"
	TypeName   string       // e.g. "BasicLoginMsg"
	SchemaName string       // e.g. "BasicLoginMsgSchema"
	Package    string       // e.g. "basicpb"
	ImportPath string       // e.g. "basicpb/basic_pb.js"
	Fields     []ProtoField // ordered fields
}

type ProtoField struct {
	Name string // camelCase JS name, e.g. "targetX"
	Type string // TS type, e.g. "number", "string", "boolean"
}

// MessageDB maps full proto name → ProtoMessage.
type MessageDB map[string]*ProtoMessage

// parseProtoES walks the proto-es gen directory, parsing all .d.ts files
// to extract message type info.
func parseProtoES(root string) MessageDB {
	db := make(MessageDB)
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "_pb.d.ts") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// Compute import path relative to gen/es, using .js extension.
		rel, _ := filepath.Rel(root, path)
		importPath := strings.TrimSuffix(rel, ".d.ts") + ".js"

		parseDTS(string(data), importPath, db)
		return nil
	})
	return db
}

// Regex patterns for parsing protobuf-es .d.ts files.
var (
	// Matches: export declare type BasicLoginMsg = Message<"basicpb.BasicLoginMsg"> & {
	reMsgStart = regexp.MustCompile(`export declare type (\w+) = Message<"([^"]+)"> & \{`)
	// Matches scalar field:   targetX: number;
	reField = regexp.MustCompile(`^\s+(\w+): (number|string|boolean|bigint);`)
	// Matches enum-typed field:   slot: EquipSlot;
	// (Treat unknown single-identifier types as numbers — proto enums map to
	// numeric values in @bufbuild/protobuf.)
	reEnumField = regexp.MustCompile(`^\s+(\w+): ([A-Z]\w*);`)
	// Closing brace
	reMsgEnd = regexp.MustCompile(`^\};`)
)

func parseDTS(content string, importPath string, db MessageDB) {
	lines := strings.Split(content, "\n")
	pkg := ""
	// Extract package from import path: "basicpb/basic_pb.js" → "basicpb"
	if idx := strings.Index(importPath, "/"); idx >= 0 {
		pkg = importPath[:idx]
	}

	var current *ProtoMessage
	for _, line := range lines {
		if current == nil {
			if m := reMsgStart.FindStringSubmatch(line); m != nil {
				current = &ProtoMessage{
					TypeName:   m[1],
					FullName:   m[2],
					SchemaName: m[1] + "Schema",
					Package:    pkg,
					ImportPath: importPath,
				}
			}
		} else {
			if reMsgEnd.MatchString(line) {
				db[current.FullName] = current
				current = nil
			} else if m := reField.FindStringSubmatch(line); m != nil {
				current.Fields = append(current.Fields, ProtoField{
					Name: m[1],
					Type: m[2],
				})
			} else if m := reEnumField.FindStringSubmatch(line); m != nil {
				// Proto enum — emit as number on the wire.
				current.Fields = append(current.Fields, ProtoField{
					Name: m[1],
					Type: "number",
				})
			}
		}
	}
}
