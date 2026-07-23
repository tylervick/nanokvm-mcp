package apicheck

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Pinned upstream ref; bump deliberately when re-vendoring route knowledge.
const ref = "main"

var routerFiles = []string{
	"server/router/auth.go", "server/router/vm.go", "server/router/hid.go",
	"server/router/storage.go", "server/router/stream.go", "server/router/picoclaw.go",
}

func fetch(t *testing.T, path string) string {
	url := "https://raw.githubusercontent.com/sipeed/NanoKVM/" + ref + "/" + path
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("offline or upstream unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("fetch %s: HTTP %d", path, resp.StatusCode)
	}
	buf := new(strings.Builder)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		buf.WriteString(sc.Text())
		buf.WriteByte('\n')
	}
	return buf.String()
}

func TestRequiredRoutesExistUpstream(t *testing.T) {
	if os.Getenv("APICHECK_OFFLINE") == "1" {
		t.Skip("APICHECK_OFFLINE set")
	}
	corpusByFile := map[string]string{}
	corpus := ""
	for _, f := range routerFiles {
		content := fetch(t, f)
		corpusByFile[f] = content
		corpus += content
	}
	f, err := os.Open("routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		route := strings.TrimSpace(sc.Text())
		if route == "" {
			continue
		}
		isPicoclaw := strings.HasPrefix(route, "/api/picoclaw")

		// Most routes are registered inside a `r.Group("/api")`, so the
		// string literal in the source is just the suffix after "/api"
		// (e.g. `api.GET("/vm/gpio", ...)`). A few (e.g. the auth login
		// route) are registered directly on the top-level engine with
		// the full "/api/..." path still in the literal. Accept either
		// form.
		//
		// The two picoclaw routes are a further special case: upstream
		// registers them via path constants that are relative to the
		// picoclaw group's own base path ("/api/picoclaw"), e.g.
		// `picoclawScreenshotPath = "/screenshot"` used as
		// `localAPI.GET(picoclawScreenshotPath, ...)`. So for those we
		// match on the suffix after "/api/picoclaw" instead. The suffix
		// alone (e.g. "/screenshot") is short and generic, so we scope
		// the search to picoclaw.go's own content only -- searching the
		// combined corpus could let a coincidental match in an unrelated
		// router file mask an actual removal of the route.
		var found bool
		if isPicoclaw {
			needle := strings.TrimPrefix(route, "/api/picoclaw")
			found = strings.Contains(corpusByFile["server/router/picoclaw.go"], "\""+needle+"\"")
		} else {
			fullNeedle := "\"" + route + "\""
			suffixNeedle := "\"" + strings.TrimPrefix(route, "/api") + "\""
			found = strings.Contains(corpus, fullNeedle) || strings.Contains(corpus, suffixNeedle)
		}

		if !found {
			if isPicoclaw {
				t.Errorf("INTERNAL route missing upstream (unstable API): %s", route)
			} else {
				t.Errorf("PUBLIC route we depend on is missing upstream: %s", route)
			}
		}
	}
}
