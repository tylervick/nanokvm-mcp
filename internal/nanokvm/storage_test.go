package nanokvm

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// mountHandler mirrors upstream's /api/storage/image/mount
// (server/service/storage/image.go), which binds proto.MountImageReq. Both
// fields are `validate:"omitempty"`, and an empty File is not an error: it is
// how the firmware is told to unmount.
func mountHandler(unmounted *bool) func(*http.Request) (any, int) {
	return func(r *http.Request) (any, int) {
		var req struct {
			File  string `json:"file"`
			Cdrom bool   `json:"cdrom"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, -1 // "invalid arguments"
		}
		if unmounted != nil {
			*unmounted = req.File == ""
		}
		return nil, 0
	}
}

func TestMountImageSendsTheFieldsUpstreamBinds(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.onRequest("/api/storage/image/mount", mountHandler(nil))
	c := newTestClient(f)

	if err := c.MountImage(context.Background(), "/data/ubuntu.iso", true); err != nil {
		t.Fatalf("MountImage: %v", err)
	}
	body := f.body(t, "/api/storage/image/mount")
	if body["file"] != "/data/ubuntu.iso" {
		t.Errorf("file = %v, want /data/ubuntu.iso", body["file"])
	}
	if body["cdrom"] != true {
		t.Errorf("cdrom = %v, want true", body["cdrom"])
	}
}

// Unmount is the same route with an empty file; upstream branches on
// `req.File == ""` rather than on a separate endpoint.
func TestUnmountImageLeavesTheFileEmpty(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	var unmounted bool
	f.onRequest("/api/storage/image/mount", mountHandler(&unmounted))
	c := newTestClient(f)

	if err := c.UnmountImage(context.Background()); err != nil {
		t.Fatalf("UnmountImage: %v", err)
	}
	if !unmounted {
		t.Error("the firmware read a non-empty file and would have mounted instead of unmounting")
	}
}

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
