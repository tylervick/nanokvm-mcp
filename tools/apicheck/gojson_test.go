package apicheck

import (
	"strings"
	"testing"
)

// src wraps declarations in a minimal package so the tests read as Go. Struct
// tags are written with ~ in place of the backtick a raw string cannot hold.
func src(decls string) map[string]string {
	return files(map[string]string{"a.go": decls})
}

func files(decls map[string]string) map[string]string {
	out := make(map[string]string, len(decls))
	for name, src := range decls {
		out[name] = "package p\n" + strings.ReplaceAll(src, "~", "`")
	}
	return out
}

func fieldsOf(t *testing.T, srcs map[string]string, typeName string) []jsonField {
	t.Helper()
	p, err := parseGoFiles(srcs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	f, _, err := p.structOf(typeName)
	if err != nil {
		t.Fatalf("structOf(%s): %v", typeName, err)
	}
	return f
}

func find(t *testing.T, fs []jsonField, name string) jsonField {
	t.Helper()
	for _, f := range fs {
		if f.name == name {
			return f
		}
	}
	t.Fatalf("no field named %q in %v", name, fs)
	return jsonField{}
}

func TestStructOfReadsJSONTagNames(t *testing.T) {
	fs := fieldsOf(t, src(`
type T struct {
	File  string ~json:"file"~
	Cdrom bool   ~json:"cdrom"~
}`), "T")
	if len(fs) != 2 {
		t.Fatalf("want 2 fields, got %d: %v", len(fs), fs)
	}
	if f := find(t, fs, "file"); f.kind != kindString || !f.tagged {
		t.Errorf("file: got kind=%s tagged=%v, want string/true", f.kind, f.tagged)
	}
	if f := find(t, fs, "cdrom"); f.kind != kindBool {
		t.Errorf("cdrom: got kind=%s, want bool", f.kind)
	}
}

// Upstream's SetGpioReq carries no json tags and leans on encoding/json's
// case-insensitive fallback. An untagged field's name is its Go name.
func TestStructOfFallsBackToGoFieldNameWhenUntagged(t *testing.T) {
	fs := fieldsOf(t, src(`
type T struct {
	Type     string ~validate:"required"~
	Duration uint   ~validate:"omitempty"~
}`), "T")
	tt := find(t, fs, "Type")
	if tt.tagged {
		t.Error("Type: tagged=true, want false")
	}
	if !tt.required {
		t.Error(`Type: required=false, want true (validate:"required")`)
	}
	if d := find(t, fs, "Duration"); d.kind != kindNumber || d.required {
		t.Errorf("Duration: got kind=%s required=%v, want number/false", d.kind, d.required)
	}
}

func TestStructOfSkipsUnexportedAndDashTaggedFields(t *testing.T) {
	fs := fieldsOf(t, src(`
type T struct {
	Kept       string ~json:"kept"~
	Hidden     string ~json:"-"~
	unexported string
}`), "T")
	if len(fs) != 1 || fs[0].name != "kept" {
		t.Fatalf("want only the kept field, got %v", fs)
	}
}

func TestStructOfRecordsOmitEmpty(t *testing.T) {
	fs := fieldsOf(t, src(`
type T struct {
	A string ~json:"a,omitempty"~
	B string ~json:"b"~
}`), "T")
	if !find(t, fs, "a").omitEmpty {
		t.Error("a: omitEmpty=false, want true")
	}
	if find(t, fs, "b").omitEmpty {
		t.Error("b: omitEmpty=true, want false")
	}
}

// A tag that sets only options names nothing, so the field keeps its Go name.
func TestStructOfKeepsGoNameWhenTagOnlySetsOptions(t *testing.T) {
	fs := fieldsOf(t, src(`
type T struct {
	Amount int ~json:",omitempty"~
}`), "T")
	f := find(t, fs, "Amount")
	if f.tagged {
		t.Error("Amount: tagged=true, want false (the tag names no field)")
	}
	if !f.omitEmpty {
		t.Error("Amount: omitEmpty=false, want true")
	}
}

func TestStructOfDereferencesPointers(t *testing.T) {
	fs := fieldsOf(t, src(`
type T struct {
	X *float64 ~json:"x"~
}`), "T")
	if k := find(t, fs, "x").kind; k != kindNumber {
		t.Errorf("x: got kind=%s, want number", k)
	}
}

// encoding/json writes []byte as a base64 string, not an array.
func TestStructOfTreatsByteSliceAsString(t *testing.T) {
	fs := fieldsOf(t, src(`
type T struct {
	Blob []byte   ~json:"blob"~
	List []string ~json:"list"~
}`), "T")
	if k := find(t, fs, "blob").kind; k != kindString {
		t.Errorf("blob: got kind=%s, want string", k)
	}
	if k := find(t, fs, "list").kind; k != kindArray {
		t.Errorf("list: got kind=%s, want array", k)
	}
}

func TestStructOfMapsCompositeKinds(t *testing.T) {
	fs := fieldsOf(t, src(`
type T struct {
	M map[string]string ~json:"m"~
	S struct{ A int }   ~json:"s"~
	I any               ~json:"i"~
	E interface{}       ~json:"e"~
}`), "T")
	for name, want := range map[string]kind{"m": kindObject, "s": kindObject, "i": kindAny, "e": kindAny} {
		if k := find(t, fs, name).kind; k != want {
			t.Errorf("%s: got kind=%s, want %s", name, k, want)
		}
	}
}

func TestStructOfResolvesNamedTypesToTheirUnderlyingKind(t *testing.T) {
	fs := fieldsOf(t, files(map[string]string{
		"a.go": `
type T struct {
	K Keys   ~json:"k"~
	P *Point ~json:"p"~
}`,
		"b.go": `
type Keys []string
type Point struct{ X int }`,
	}), "T")
	if k := find(t, fs, "k").kind; k != kindArray {
		t.Errorf("k: got kind=%s, want array (Keys is []string)", k)
	}
	if k := find(t, fs, "p").kind; k != kindObject {
		t.Errorf("p: got kind=%s, want object", k)
	}
}

// A type with its own JSON methods puts nothing of its Go shape on the wire.
// Upstream's HotkeyKeys is declared []string but unmarshals from a
// comma-separated string too, so inferring "array" would be a lie.
func TestStructOfTreatsCustomJSONMethodsAsOpaque(t *testing.T) {
	fs := fieldsOf(t, files(map[string]string{
		"a.go": `
type T struct {
	K Keys ~json:"k"~
}`,
		"b.go": `
type Keys []string

func (k *Keys) UnmarshalJSON(b []byte) error { return nil }`,
	}), "T")
	if k := find(t, fs, "k").kind; k != kindAny {
		t.Errorf("k: got kind=%s, want any (Keys has UnmarshalJSON)", k)
	}
}

func TestStructOfErrorsOnUnresolvableNamedType(t *testing.T) {
	p, err := parseGoFiles(src(`
type T struct {
	K other.Thing ~json:"k"~
}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err = p.structOf("T"); err == nil {
		t.Fatal("want an error naming the unresolvable type, got nil")
	} else if !strings.Contains(err.Error(), "other.Thing") {
		t.Errorf("error should name the type it could not resolve, got: %v", err)
	}
}

func TestStructOfErrorsOnEmbeddedField(t *testing.T) {
	p, err := parseGoFiles(src(`
type Base struct{ A int }

type T struct {
	Base
	B int ~json:"b"~
}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := p.structOf("T"); err == nil {
		t.Fatal("want an error: embedded fields promote their own fields and are not modelled")
	}
}

func TestStructOfErrorsOnUnknownType(t *testing.T) {
	p, err := parseGoFiles(src("type T struct{}"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := p.structOf("Nope"); err == nil {
		t.Fatal("want an error for a type that is not declared")
	}
}

// The pre-#31 ListImages decoded into a bare []string. Naming a non-struct as
// one side of a shape row has to fail loudly, not silently compare nothing.
func TestStructOfErrorsWhenTypeIsNotAStruct(t *testing.T) {
	p, err := parseGoFiles(src("type T []string"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := p.structOf("T"); err == nil {
		t.Fatal("want an error for a non-struct type")
	} else if !strings.Contains(err.Error(), string(kindArray)) {
		t.Errorf("error should say what kind it found instead, got: %v", err)
	}
}

// A type that encodes itself does not describe its payload through its fields,
// so diffing those fields would compare things that never reach the wire. That
// is the silent pass this package exists to refuse: naming such a type in the
// shape table has to fail, and the row has to use a marker instead.
func TestStructOfRejectsATypeThatEncodesItself(t *testing.T) {
	p, err := parseGoFiles(src(`
type T struct {
	A string ~json:"a"~
}

func (t T) MarshalJSON() ([]byte, error) { return nil, nil }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := p.structOf("T"); err == nil {
		t.Fatal("want an error: T's own MarshalJSON decides the payload, not its fields")
	}
}

func TestStructOfReportsTheDeclaringFile(t *testing.T) {
	p, err := parseGoFiles(files(map[string]string{
		"a.go": "type A struct{}",
		"b.go": "type B struct{}",
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, file, err := p.structOf("B"); err != nil || file != "b.go" {
		t.Errorf("got file=%q err=%v, want b.go/nil", file, err)
	}
}

// ---- diff ----

func TestDiffFlagsAFieldWeReadThatUpstreamDoesNotSend(t *testing.T) {
	up := []jsonField{{name: "pwr", kind: kindBool, tagged: true}}
	ours := []jsonField{
		{name: "pwr", kind: kindBool, tagged: true},
		{name: "hdd_available", kind: kindBool, tagged: true},
	}
	problems := diffFields(response, up, ours)
	if len(problems) != 1 || !strings.Contains(problems[0], "hdd_available") {
		t.Fatalf("want one problem naming hdd_available, got %v", problems)
	}
}

func TestDiffFlagsAKindMismatch(t *testing.T) {
	up := []jsonField{{name: "files", kind: kindArray, tagged: true}}
	ours := []jsonField{{name: "files", kind: kindString, tagged: true}}
	problems := diffFields(response, up, ours)
	if len(problems) != 1 || !strings.Contains(problems[0], "files") {
		t.Fatalf("want one problem naming files, got %v", problems)
	}
}

// Upstream's SetGpioReq has no json tags; gin binds JSON with encoding/json,
// which falls back to a case-insensitive field-name match. "type" reaches
// `Type` and the route works, so this must not be reported.
func TestDiffAcceptsCaseInsensitiveMatchAgainstUntaggedUpstreamFields(t *testing.T) {
	up := []jsonField{
		{name: "Type", kind: kindString, required: true},
		{name: "Duration", kind: kindNumber},
	}
	ours := []jsonField{
		{name: "type", kind: kindString, tagged: true},
		{name: "duration", kind: kindNumber, tagged: true},
	}
	if problems := diffFields(request, up, ours); len(problems) != 0 {
		t.Fatalf("want no problems, got %v", problems)
	}
}

func TestDiffFlagsARequiredUpstreamFieldWeNeverSend(t *testing.T) {
	up := []jsonField{
		{name: "content", kind: kindString, required: true},
		{name: "langue", kind: kindString},
	}
	ours := []jsonField{{name: "langue", kind: kindString, tagged: true}}
	problems := diffFields(request, up, ours)
	if len(problems) != 1 || !strings.Contains(problems[0], "content") {
		t.Fatalf("want one problem naming content, got %v", problems)
	}
}

func TestDiffFlagsOmitEmptyOnAFieldUpstreamRequires(t *testing.T) {
	up := []jsonField{{name: "content", kind: kindString, required: true}}
	ours := []jsonField{{name: "content", kind: kindString, tagged: true, omitEmpty: true}}
	problems := diffFields(request, up, ours)
	if len(problems) != 1 || !strings.Contains(problems[0], "omitempty") {
		t.Fatalf("want one problem about omitempty, got %v", problems)
	}
}

// A required upstream field is only required of a request. Responses carry no
// validator, so the same tag must not fire in that direction.
func TestDiffIgnoresRequiredTagsOnResponses(t *testing.T) {
	up := []jsonField{
		{name: "file", kind: kindString, tagged: true, required: true},
		{name: "extra", kind: kindString, tagged: true},
	}
	ours := []jsonField{{name: "extra", kind: kindString, tagged: true}}
	if problems := diffFields(response, up, ours); len(problems) != 0 {
		t.Fatalf("want no problems, got %v", problems)
	}
}

func TestDiffTreatsAnyAsCompatibleWithEveryKind(t *testing.T) {
	up := []jsonField{{name: "keys", kind: kindAny, tagged: true}}
	ours := []jsonField{{name: "keys", kind: kindArray, tagged: true}}
	if problems := diffFields(request, up, ours); len(problems) != 0 {
		t.Fatalf("want no problems, got %v", problems)
	}
}

func TestDiffIgnoresUpstreamFieldsWeDoNotRead(t *testing.T) {
	up := []jsonField{
		{name: "pwr", kind: kindBool, tagged: true},
		{name: "hdd", kind: kindBool, tagged: true},
	}
	ours := []jsonField{{name: "pwr", kind: kindBool, tagged: true}}
	if problems := diffFields(response, up, ours); len(problems) != 0 {
		t.Fatalf("want no problems, got %v", problems)
	}
}

// ---- parsing our own tree ----

func TestParseGoDirResolvesTypesAcrossThePackage(t *testing.T) {
	p, err := parseGoDir("../../internal/backend")
	if err != nil {
		t.Fatalf("parseGoDir: %v", err)
	}
	fs, file, err := p.structOf("Action")
	if err != nil {
		t.Fatalf("structOf(Action): %v", err)
	}
	if file != "backend.go" {
		t.Errorf("Action declared in %q, want backend.go", file)
	}
	// `From *Point` only resolves if the whole package was parsed.
	if k := find(t, fs, "from").kind; k != kindObject {
		t.Errorf("from: got kind=%s, want object", k)
	}
}
