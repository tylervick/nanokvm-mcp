package apicheck

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// repoRoot is where our own source lives relative to this package's directory,
// which is what `go test` uses as the working directory.
const repoRoot = "../.."

// typeRef names one side of a shape row: a Go source file and a type in it.
type typeRef struct {
	path string
	name string
}

func (t typeRef) empty() bool { return t.name == "" }
func (t typeRef) String() string {
	if t.empty() {
		return "-"
	}
	return t.path + "#" + t.name
}

// shapeRow is one line of shapes.txt: what we put on a route in one direction,
// and what upstream declares for it.
type shapeRow struct {
	line     int
	route    string
	method   string
	dir      direction
	upstream typeRef
	ours     typeRef
	marker   string // "" for a diffable row; otherwise passthrough / untyped / none
	reason   string // why there is nothing to diff
}

// diffable reports whether the row names a type on both sides.
func (r shapeRow) diffable() bool { return !r.ours.empty() }

func (r shapeRow) String() string {
	return fmt.Sprintf("shapes.txt:%d %s %s %s", r.line, r.method, r.route, r.dir)
}

// markers are the ways a row can say "nothing to diff here". Each needs a
// reason: an exemption without one is indistinguishable from an oversight.
var markers = map[string]string{
	"passthrough": "upstream declares a type; we hand the decoded map to the MCP client",
	"untyped":     "no struct on either side",
	"none":        "the direction carries no payload",
}

func markerHelp() string {
	names := make([]string, 0, len(markers))
	for name := range markers {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		names[i] = fmt.Sprintf("%s (%s)", name, markers[name])
	}
	return strings.Join(names, ", ")
}

func loadShapeTable(file string) ([]shapeRow, error) {
	f, err := os.Open(file) //nolint:gosec // G304: a checked-in table beside this test.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var rows []shapeRow
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		row, err := parseShapeRow(line, n)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", file, n, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s has no rows", file)
	}
	return rows, nil
}

// loadRoutes reads routes.txt, the list apicheck proves still exists upstream.
func loadRoutes(file string) ([]string, error) {
	f, err := os.Open(file) //nolint:gosec // G304: a checked-in table beside this test.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if route := strings.TrimSpace(sc.Text()); route != "" {
			out = append(out, route)
		}
	}
	return out, sc.Err()
}

func parseShapeRow(line string, n int) (shapeRow, error) {
	rest := line
	next := func() string {
		rest = strings.TrimLeft(rest, " \t")
		i := strings.IndexAny(rest, " \t")
		if i < 0 {
			tok := rest
			rest = ""
			return tok
		}
		tok := rest[:i]
		rest = rest[i:]
		return tok
	}
	row := shapeRow{line: n, route: next(), method: next()}
	dir := next()
	upstream := next()
	// The last column runs to end of line so a marker's reason can be a sentence.
	ours := strings.TrimSpace(rest)

	if row.route == "" || row.method == "" || dir == "" || upstream == "" || ours == "" {
		return row, fmt.Errorf("want 5 columns: <route> <METHOD> <dir> <upstream> <ours>")
	}
	if !strings.HasPrefix(row.route, "/api/") {
		return row, fmt.Errorf("route %q should start with /api/", row.route)
	}
	if row.method != "GET" && row.method != "POST" {
		return row, fmt.Errorf("method %q: only the GET and POST routes we call are modelled", row.method)
	}
	switch direction(dir) {
	case request, response:
		row.dir = direction(dir)
	default:
		return row, fmt.Errorf("direction %q: want %s or %s", dir, request, response)
	}

	if upstream != "-" {
		ref, err := parseTypeRef(upstream)
		if err != nil {
			return row, fmt.Errorf("upstream: %w", err)
		}
		row.upstream = ref
	}

	if marker, reason, ok := strings.Cut(ours, ":"); ok && !strings.Contains(marker, "#") {
		if _, known := markers[marker]; !known {
			return row, fmt.Errorf("unknown marker %q: want one of %s", marker, markerHelp())
		}
		if strings.TrimSpace(reason) == "" {
			return row, fmt.Errorf("marker %q needs a reason; an exemption without one reads as an oversight", marker)
		}
		row.marker, row.reason = marker, strings.TrimSpace(reason)
		return row, nil
	}

	ref, err := parseTypeRef(ours)
	if err != nil {
		return row, fmt.Errorf("ours: %w", err)
	}
	row.ours = ref
	if row.upstream.empty() {
		return row, fmt.Errorf("ours names %s but upstream is `-`; there is nothing to diff against, "+
			"so this needs an untyped: or passthrough: marker instead", row.ours.name)
	}
	return row, nil
}

func parseTypeRef(s string) (typeRef, error) {
	file, name, ok := strings.Cut(s, "#")
	if !ok || file == "" || name == "" {
		return typeRef{}, fmt.Errorf("want <path>#<TypeName>, got %q", s)
	}
	if !strings.HasSuffix(file, ".go") {
		return typeRef{}, fmt.Errorf("%q is not a .go file", file)
	}
	return typeRef{path: file, name: name}, nil
}

// ourFields resolves our side of a row. The whole package is parsed so a field
// type declared in a sibling file resolves, and the row's file is then held to
// actually declaring the type -- a rename that moves it elsewhere is drift the
// table should record.
func ourFields(ref typeRef) ([]jsonField, error) {
	dir, file := path.Split(ref.path)
	dir = strings.TrimSuffix(dir, "/")
	p, err := parseGoDir(filepath.Join(repoRoot, filepath.FromSlash(dir)))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", dir, err)
	}
	fields, declaredIn, err := p.structOf(ref.name)
	if err != nil {
		return nil, err
	}
	if declaredIn != file {
		return nil, fmt.Errorf("%s is declared in %s/%s, not %s; update the table", ref.name, dir, declaredIn, ref.path)
	}
	return fields, nil
}
