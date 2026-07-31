package apicheck

import (
	"os"
	"path"
	"sort"
	"strings"
	"testing"
)

// upstreamURL points at the source a failure is about. It lives here, beside
// the pinned `ref` and the fetch that uses it, so shapes.go stays free of
// anything declared in a test file.
func upstreamURL(p string) string {
	return "https://github.com/sipeed/NanoKVM/blob/" + ref + "/" + p
}

func mustRow(t *testing.T, line string) shapeRow {
	t.Helper()
	r, err := parseShapeRow(line, 1)
	if err != nil {
		t.Fatalf("parseShapeRow(%q): %v", line, err)
	}
	return r
}

func TestParseShapeRowReadsATypePair(t *testing.T) {
	r := mustRow(t, "/api/vm/gpio  POST  req  server/proto/vm.go#SetGpioReq  internal/nanokvm/vm.go#setGpioReq")
	if r.route != "/api/vm/gpio" || r.method != "POST" || r.dir != request {
		t.Fatalf("got route=%s method=%s dir=%s", r.route, r.method, r.dir)
	}
	if r.upstream.path != "server/proto/vm.go" || r.upstream.name != "SetGpioReq" {
		t.Errorf("upstream = %+v", r.upstream)
	}
	if r.ours.path != "internal/nanokvm/vm.go" || r.ours.name != "setGpioReq" {
		t.Errorf("ours = %+v", r.ours)
	}
	if !r.diffable() {
		t.Error("a row naming both types should be diffable")
	}
}

func TestParseShapeRowKeepsTheWholeReason(t *testing.T) {
	r := mustRow(t, "/api/vm/info GET rsp server/proto/vm.go#GetInfoRsp passthrough: handed to the MCP client unread")
	if r.marker != "passthrough" {
		t.Fatalf("marker = %q", r.marker)
	}
	if r.reason != "handed to the MCP client unread" {
		t.Errorf("reason = %q, want the full sentence", r.reason)
	}
	if r.diffable() {
		t.Error("a marker row must not be diffed")
	}
	// The upstream type is still recorded: it says what we chose not to read.
	if r.upstream.name != "GetInfoRsp" {
		t.Errorf("upstream = %+v, want GetInfoRsp recorded alongside the marker", r.upstream)
	}
}

func TestParseShapeRowRequiresAReasonForEveryMarker(t *testing.T) {
	for _, line := range []string{
		"/api/hid/reset POST req - none:",
		"/api/hid/reset POST req - none",
		"/api/stream/mjpeg GET rsp - untyped:   ",
	} {
		if _, err := parseShapeRow(line, 1); err == nil {
			t.Errorf("parseShapeRow(%q) accepted a marker with no reason", line)
		}
	}
}

func TestParseShapeRowRejectsAnUnknownMarker(t *testing.T) {
	if _, err := parseShapeRow("/api/vm/info GET rsp - skip: because", 1); err == nil {
		t.Error("an unrecognised marker should not silently exempt a route")
	}
}

func TestParseShapeRowRejectsAMissingUpstreamTypeOnADiffableRow(t *testing.T) {
	if _, err := parseShapeRow("/api/vm/gpio POST req - internal/nanokvm/vm.go#setGpioReq", 1); err == nil {
		t.Error("naming our type with no upstream type to diff it against should be an error")
	}
}

func TestParseShapeRowRejectsMalformedRows(t *testing.T) {
	for _, line := range []string{
		"/api/vm/gpio POST req",
		"/api/vm/gpio PATCH req server/proto/vm.go#SetGpioReq internal/nanokvm/vm.go#setGpioReq",
		"/api/vm/gpio POST sideways server/proto/vm.go#SetGpioReq internal/nanokvm/vm.go#setGpioReq",
		"/api/vm/gpio POST req server/proto/vm.go internal/nanokvm/vm.go#setGpioReq",
	} {
		if _, err := parseShapeRow(line, 1); err == nil {
			t.Errorf("parseShapeRow(%q) should have failed", line)
		}
	}
}

// The two files are one contract in two halves. A route in only one of them is
// a route whose shape nobody decided about.
func TestShapeTableCoversExactlyTheRoutesWeCall(t *testing.T) {
	rows, err := loadShapeTable("shapes.txt")
	if err != nil {
		t.Fatal(err)
	}
	shaped := map[string]bool{}
	for _, r := range rows {
		shaped[r.route] = true
	}

	routes, err := loadRoutes("routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, r := range routes {
		listed[r] = true
		if !shaped[r] {
			t.Errorf("%s is in routes.txt with no row in shapes.txt: say what travels over it, "+
				"or mark it passthrough/untyped/none with a reason", r)
		}
	}
	for r := range shaped {
		if !listed[r] {
			t.Errorf("%s has a shape row but is not in routes.txt, so nothing checks it still exists", r)
		}
	}
}

// Offline half of the check: our side of every row must resolve, in the file
// the table names. This catches a rename on our side without touching the
// network, so it fails the same way for a contributor with no connectivity.
func TestOurShapesResolveWhereTheTableSaysTheyDo(t *testing.T) {
	rows, err := loadShapeTable("shapes.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if !r.diffable() {
			continue
		}
		fields, err := ourFields(r.ours)
		if err != nil {
			t.Errorf("%s: %v", r, err)
			continue
		}
		if len(fields) == 0 {
			t.Errorf("%s: %s has no JSON-visible fields; a shape row over an empty struct checks nothing", r, r.ours)
		}
	}
}

func TestUpstreamShapesMatchOurs(t *testing.T) {
	if os.Getenv("APICHECK_OFFLINE") == "1" {
		t.Skip("APICHECK_OFFLINE set")
	}
	rows, err := loadShapeTable("shapes.txt")
	if err != nil {
		t.Fatal(err)
	}

	// Upstream types resolve against the other files the table names from the
	// same package, so fetch once per directory and parse them together.
	byDir := map[string]map[string]string{}
	for _, r := range rows {
		if r.upstream.empty() {
			continue
		}
		dir, file := path.Split(r.upstream.path)
		dir = strings.TrimSuffix(dir, "/")
		if byDir[dir] == nil {
			byDir[dir] = map[string]string{}
		}
		if _, done := byDir[dir][file]; !done {
			byDir[dir][file] = fetch(t, r.upstream.path)
		}
	}
	pkgs := map[string]*gopkg{}
	for dir, srcs := range byDir {
		p, err := parseGoFiles(srcs)
		if err != nil {
			t.Fatalf("parse upstream %s: %v", dir, err)
		}
		pkgs[dir] = p
	}

	for _, r := range rows {
		if !r.diffable() {
			continue
		}
		dir, file := path.Split(r.upstream.path)
		dir = strings.TrimSuffix(dir, "/")
		up, declaredIn, err := pkgs[dir].structOf(r.upstream.name)
		if err != nil {
			t.Errorf("%s: upstream %s: %v", r, r.upstream, err)
			continue
		}
		if declaredIn != file {
			t.Errorf("%s: upstream %s moved to %s/%s; update the table", r, r.upstream, dir, declaredIn)
		}
		ours, err := ourFields(r.ours)
		if err != nil {
			t.Errorf("%s: %v", r, err)
			continue
		}
		for _, problem := range diffFields(r.dir, up, ours) {
			t.Errorf("%s\n  %s vs %s: %s\n  upstream: %s", r, r.ours, r.upstream, problem, upstreamURL(r.upstream.path))
		}
	}
}

// A sanity check on the table's own reach: if every row were a marker, the
// online check would pass while diffing nothing at all.
func TestShapeTableActuallyDiffsSomething(t *testing.T) {
	rows, err := loadShapeTable("shapes.txt")
	if err != nil {
		t.Fatal(err)
	}
	var diffable, markers []string
	for _, r := range rows {
		if r.diffable() {
			diffable = append(diffable, r.route)
		} else {
			markers = append(markers, r.route+" ("+r.marker+")")
		}
	}
	if len(diffable) == 0 {
		t.Fatal("no row in shapes.txt names a type pair, so nothing is compared")
	}
	sort.Strings(markers)
	t.Logf("%d rows diffed, %d exempt: %s", len(diffable), len(markers), strings.Join(markers, ", "))
}
