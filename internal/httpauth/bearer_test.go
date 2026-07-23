package httpauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearer(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := Bearer("secret", ok)

	cases := []struct {
		hdr  string
		want int
	}{
		{"Bearer secret", 200},
		{"Bearer wrong", 401},
		{"", 401},
		{"secret", 401},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/", nil)
		if c.hdr != "" {
			r.Header.Set("Authorization", c.hdr)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("hdr %q: want %d got %d", c.hdr, c.want, w.Code)
		}
	}
}
