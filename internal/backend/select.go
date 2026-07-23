package backend

import (
	"context"
	"log"
	"net/http"
)

type Deps struct {
	BaseURL   string
	TokenPath string
	SessionID string
	HTTP      *http.Client
	Fallback  KVMBackend
	Probe     bool
}

// Select returns the picoclaw backend when its token file is present and a probe
// screenshot succeeds; otherwise the provided fallback. The decision is logged.
func Select(ctx context.Context, d Deps) (KVMBackend, error) {
	token, err := ReadInternalToken(d.TokenPath)
	if err != nil {
		log.Printf("backend: picoclaw token unavailable (%v); using %s", err, d.Fallback.Name())
		return d.Fallback, nil
	}
	p := NewPicoclaw(d.BaseURL, token, d.SessionID, d.HTTP)
	if d.Probe {
		if _, err := p.Screenshot(ctx, ScreenshotOpts{Width: 64}); err != nil {
			log.Printf("backend: picoclaw probe failed (%v); using %s", err, d.Fallback.Name())
			return d.Fallback, nil
		}
	}
	log.Printf("backend: using picoclaw")
	return p, nil
}
