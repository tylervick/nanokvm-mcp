package nanokvm

import (
	"context"
	"testing"
)

func TestStorageRoundtrip(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/storage/image", []any{"/data/ubuntu.iso"})
	f.on("/api/storage/image/mount", map[string]any{})
	f.on("/api/storage/image/mounted", map[string]any{"file": "/data/ubuntu.iso"})
	c := newTestClient(f)

	imgs, err := c.ListImages(context.Background())
	if err != nil || len(imgs) != 1 {
		t.Fatalf("list: %v %v", imgs, err)
	}
	if err := c.MountImage(context.Background(), "/data/ubuntu.iso", true); err != nil {
		t.Fatal(err)
	}
	m, err := c.MountedImage(context.Background())
	if err != nil || m["file"] != "/data/ubuntu.iso" {
		t.Fatalf("mounted: %v %v", m, err)
	}
	if err := c.UnmountImage(context.Background()); err != nil {
		t.Fatal(err)
	}
}
