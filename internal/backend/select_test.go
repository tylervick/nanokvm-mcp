package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type stubBackend struct{ name string }

func (s stubBackend) Name() string                                             { return s.name }
func (s stubBackend) Screenshot(context.Context, ScreenshotOpts) (Shot, error) { return Shot{}, nil }
func (s stubBackend) Input(context.Context, []Action) error                    { return nil }

func TestSelectPrefersPicoclawWhenProbeSucceeds(t *testing.T) {
	srv, _ := fakePicoclaw(t, "tok")
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "tok")
	os.WriteFile(tokFile, []byte("tok\n"), 0o600)

	b, err := Select(context.Background(), Deps{
		BaseURL: srv.URL, TokenPath: tokFile, SessionID: "s", HTTP: srv.Client(),
		Fallback: stubBackend{"public"}, Probe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name() != "picoclaw" {
		t.Errorf("want picoclaw, got %s", b.Name())
	}
}

func TestSelectFallsBackWhenNoToken(t *testing.T) {
	b, err := Select(context.Background(), Deps{
		BaseURL: "http://127.0.0.1:0", TokenPath: "/nonexistent",
		Fallback: stubBackend{"public"}, Probe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name() != "public" {
		t.Errorf("want public fallback, got %s", b.Name())
	}
}
