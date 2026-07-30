package nanokvm

import (
	"context"
	"testing"
)

func TestListImagesReadsTheFilesEnvelope(t *testing.T) {
	// Upstream answers with proto.GetImagesRsp, so data is {"files": [...]},
	// not a bare array. Decoding it as an array yields an empty list and no
	// error, which reads as "this device has no images" — and mounting one
	// needs the path this call is supposed to supply.
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/storage/image", map[string]any{"files": []string{"/data/ubuntu.iso", "/data/win.img"}})
	c := newTestClient(f)

	imgs, err := c.ListImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 {
		t.Fatalf("got %d images from a 2-image response: %v", len(imgs), imgs)
	}
	if imgs[0] != "/data/ubuntu.iso" || imgs[1] != "/data/win.img" {
		t.Errorf("got %v, want [/data/ubuntu.iso /data/win.img]", imgs)
	}
}

func TestListImagesEmptyDeviceIsNotAnError(t *testing.T) {
	// A device with no images serves {"files": null}. That is a real answer,
	// not a failure.
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/storage/image", map[string]any{"files": nil})
	c := newTestClient(f)

	imgs, err := c.ListImages(context.Background())
	if err != nil {
		t.Fatalf("an image-less device is not an error: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("got %v, want no images", imgs)
	}
}

func TestListImagesRejectsAnUnreadableShape(t *testing.T) {
	// "No images" is a valid answer, so a payload we cannot read must not
	// masquerade as one. Tolerating it is exactly how the bare-array decode
	// stayed invisible.
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/storage/image", []string{"/data/ubuntu.iso"})
	c := newTestClient(f)

	if _, err := c.ListImages(context.Background()); err == nil {
		t.Error("an unreadable payload reported success with an empty list")
	}
}

func TestStorageRoundtrip(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/storage/image", map[string]any{"files": []string{"/data/ubuntu.iso"}})
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
