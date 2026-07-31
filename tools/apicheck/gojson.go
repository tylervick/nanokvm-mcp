package apicheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// This file models what encoding/json sees in a Go struct declaration, reading
// the declaration as source rather than as a compiled type. That is what lets
// apicheck compare our structs against the firmware's: upstream's
// request/response types are plain Go source on raw.githubusercontent.com, and
// nothing has to build for go/parser to read them.
//
// Everything here is deliberately loud. A construct the model does not cover is
// an error, never a silent pass -- the whole point of the check is that a shape
// we cannot verify must not look verified.

// kind is a field's JSON type. JSON has one number type, so int/uint/float all
// collapse to kindNumber; the distinction never reaches the wire.
type kind string

const (
	kindString kind = "string"
	kindNumber kind = "number"
	kindBool   kind = "bool"
	kindArray  kind = "array"
	kindObject kind = "object"
	// kindAny is a shape encoding/json will accept anything into (`any`,
	// `interface{}`), or one whose own JSON methods hide its Go shape.
	kindAny kind = "any"
)

// jsonField is one struct field as encoding/json sees it.
type jsonField struct {
	// name is the field's effective JSON name: the json tag's name if it sets
	// one, otherwise the Go field name. Untagged names still match
	// case-insensitively on decode -- see diffFields.
	name      string
	tagged    bool // the json tag named this field explicitly
	kind      kind
	omitEmpty bool
	// required records upstream's `validate:"required"`, which its handlers
	// enforce on request bodies via proto.ParseFormRequest.
	required bool
}

func (f jsonField) String() string { return fmt.Sprintf("%s(%s)", f.name, f.kind) }

// gopkg is a parsed set of Go files that resolve type names among themselves.
type gopkg struct {
	files map[string]*ast.File
}

func parseGoFiles(srcs map[string]string) (*gopkg, error) {
	p := &gopkg{files: make(map[string]*ast.File, len(srcs))}
	fset := token.NewFileSet()
	for path, src := range srcs {
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		p.files[path] = f
	}
	return p, nil
}

// parseGoDir parses one package's non-test sources off disk. Our own types are
// checked whole-package so a field type declared in a sibling file resolves;
// upstream is limited to the files the shape table names, and an unresolvable
// type there is reported so the table can name the missing file.
func parseGoDir(dir string) (*gopkg, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	srcs := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304: dir comes from the checked-in shape table.
		if err != nil {
			return nil, err
		}
		srcs[name] = string(b)
	}
	return parseGoFiles(srcs)
}

// typeSpec finds a declared type and the file declaring it.
func (p *gopkg) typeSpec(name string) (*ast.TypeSpec, string, bool) {
	// Iterate in a fixed order so a duplicate name (which cannot happen in a
	// valid package) still yields a stable answer.
	paths := make([]string, 0, len(p.files))
	for path := range p.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		for _, decl := range p.files[path].Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == name {
					return ts, path, true
				}
			}
		}
	}
	return nil, "", false
}

// hasJSONMethods reports whether the named type declares MarshalJSON or
// UnmarshalJSON. Such a type puts something other than its Go shape on the
// wire, so its declaration says nothing about the payload.
func (p *gopkg) hasJSONMethods(name string) bool {
	for _, f := range p.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			if fn.Name.Name != "MarshalJSON" && fn.Name.Name != "UnmarshalJSON" {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == name {
				return true
			}
		}
	}
	return false
}

// structOf returns the JSON-visible fields of typeName and the file declaring it.
func (p *gopkg) structOf(typeName string) ([]jsonField, string, error) {
	ts, file, ok := p.typeSpec(typeName)
	if !ok {
		return nil, "", fmt.Errorf("type %s is not declared in the parsed sources", typeName)
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		k, err := p.kindOf(ts.Type, 0)
		if err != nil {
			return nil, file, fmt.Errorf("type %s is not a struct: %w", typeName, err)
		}
		return nil, file, fmt.Errorf("type %s is not a struct, it is a %s", typeName, k)
	}

	var out []jsonField
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			return nil, file, fmt.Errorf("type %s has an embedded field; promoted fields are not modelled, "+
				"declare the fields explicitly or exclude the route from the shape table", typeName)
		}
		var tag reflect.StructTag
		if f.Tag != nil {
			tag = reflect.StructTag(strings.Trim(f.Tag.Value, "`"))
		}
		for _, ident := range f.Names {
			if !ident.IsExported() {
				continue // encoding/json never touches unexported fields
			}
			name, tagged, omitEmpty, skip := jsonName(ident.Name, tag)
			if skip {
				continue
			}
			k, err := p.kindOf(f.Type, 0)
			if err != nil {
				return nil, file, fmt.Errorf("type %s field %s: %w", typeName, ident.Name, err)
			}
			out = append(out, jsonField{
				name:      name,
				tagged:    tagged,
				kind:      k,
				omitEmpty: omitEmpty,
				required:  hasValidateRequired(tag),
			})
		}
	}
	return out, file, nil
}

// jsonName applies encoding/json's tag rules to one field.
func jsonName(goName string, tag reflect.StructTag) (name string, tagged, omitEmpty, skip bool) {
	v, ok := tag.Lookup("json")
	if !ok {
		return goName, false, false, false
	}
	if v == "-" {
		return "", false, false, true
	}
	parts := strings.Split(v, ",")
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitEmpty = true
		}
	}
	if parts[0] == "" {
		// `json:",omitempty"` sets options without naming the field.
		return goName, false, omitEmpty, false
	}
	return parts[0], true, omitEmpty, false
}

// hasValidateRequired reads go-playground/validator's tag, which upstream
// applies to every request body it binds.
func hasValidateRequired(tag reflect.StructTag) bool {
	for _, opt := range strings.Split(tag.Get("validate"), ",") {
		if strings.TrimSpace(opt) == "required" {
			return true
		}
	}
	return false
}

// maxResolveDepth bounds named-type chasing. Go forbids type-definition cycles,
// so this only ever trips on something the model does not understand.
const maxResolveDepth = 16

var basicKinds = map[string]kind{
	"string": kindString, "bool": kindBool,
	"int": kindNumber, "int8": kindNumber, "int16": kindNumber, "int32": kindNumber, "int64": kindNumber,
	"uint": kindNumber, "uint8": kindNumber, "uint16": kindNumber, "uint32": kindNumber, "uint64": kindNumber,
	"uintptr": kindNumber, "float32": kindNumber, "float64": kindNumber,
	"byte": kindNumber, "rune": kindNumber,
	"any": kindAny,
}

func (p *gopkg) kindOf(expr ast.Expr, depth int) (kind, error) {
	if depth > maxResolveDepth {
		return "", fmt.Errorf("gave up resolving the type after %d levels", maxResolveDepth)
	}
	switch t := expr.(type) {
	case *ast.StarExpr:
		// A pointer is the same JSON shape; it only adds null.
		return p.kindOf(t.X, depth+1)
	case *ast.ArrayType:
		// encoding/json writes []byte as a base64 string, not an array.
		if id, ok := t.Elt.(*ast.Ident); ok && (id.Name == "byte" || id.Name == "uint8") {
			return kindString, nil
		}
		return kindArray, nil
	case *ast.MapType, *ast.StructType:
		return kindObject, nil
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return kindAny, nil
		}
		return "", fmt.Errorf("non-empty interface fields are not modelled")
	case *ast.Ident:
		if k, ok := basicKinds[t.Name]; ok {
			return k, nil
		}
		if p.hasJSONMethods(t.Name) {
			// Its own JSON methods decide the wire shape; the declaration
			// does not. Upstream's HotkeyKeys is declared []string but also
			// unmarshals from a comma-separated string.
			return kindAny, nil
		}
		ts, _, ok := p.typeSpec(t.Name)
		if !ok {
			return "", fmt.Errorf("cannot resolve type %s: add the file declaring it to the shape table", t.Name)
		}
		return p.kindOf(ts.Type, depth+1)
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok {
			return "", fmt.Errorf("cannot resolve type %s.%s: it is declared in another package", pkg.Name, t.Sel.Name)
		}
		return "", fmt.Errorf("cannot resolve a qualified type reference")
	default:
		return "", fmt.Errorf("unsupported field type %T", expr)
	}
}

// direction says which side encodes. It decides whether upstream's validator
// tags apply: they gate request bodies only.
type direction string

const (
	request  direction = "req" // we encode, upstream decodes and validates
	response direction = "rsp" // upstream encodes, we decode
)

// diffFields reports every way our struct disagrees with upstream's.
//
// Matching is case-insensitive in both directions because encoding/json's
// decoder falls back to a case-insensitive field-name match. Upstream leans on
// that for the request types it left untagged (SetGpioReq, LoginReq), so a
// case-sensitive differ would fail routes that work.
//
// Upstream fields we do not read are not reported: reading a subset of a
// response is normal and safe.
func diffFields(dir direction, upstream, ours []jsonField) []string {
	var problems []string

	byName := func(fs []jsonField, name string) (jsonField, bool) {
		for _, f := range fs {
			if strings.EqualFold(f.name, name) {
				return f, true
			}
		}
		return jsonField{}, false
	}

	verb := map[direction]string{request: "we send", response: "we read"}[dir]
	for _, f := range ours {
		up, ok := byName(upstream, f.name)
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"%s %q, which upstream does not declare (upstream has: %s)", verb, f.name, names(upstream)))
			continue
		}
		if f.kind != up.kind && f.kind != kindAny && up.kind != kindAny {
			problems = append(problems, fmt.Sprintf(
				"%q is %s upstream but %s here", up.name, up.kind, f.kind))
		}
		if dir == request && up.required && f.omitEmpty {
			problems = append(problems, fmt.Sprintf(
				"%q is omitempty here but upstream validates it as required, so an empty value would be dropped and rejected", f.name))
		}
	}

	if dir == request {
		for _, up := range upstream {
			if !up.required {
				continue
			}
			if _, ok := byName(ours, up.name); !ok {
				problems = append(problems, fmt.Sprintf(
					"upstream requires %q but we never send it", up.name))
			}
		}
	}
	return problems
}

func names(fs []jsonField) string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.String())
	}
	if len(out) == 0 {
		return "(no fields)"
	}
	return strings.Join(out, ", ")
}
