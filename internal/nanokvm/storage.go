package nanokvm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// imagesRsp is the `data` of GET /api/storage/image (upstream
// proto.GetImagesRsp). The payload is {"files": [...]}, not a bare array.
type imagesRsp struct {
	Files []string `json:"files"`
}

// mountImageReq is the body of POST /api/storage/image/mount (upstream
// proto.MountImageReq). An empty File is not an omission: it is how the
// firmware is told to unmount.
type mountImageReq struct {
	File  string `json:"file"`
	Cdrom bool   `json:"cdrom"`
}

// ListImages returns the disk image paths the firmware offers for mounting.
//
// A payload we cannot read is an error here, not an empty list: "this device
// has no images" is itself a valid answer, and the caller has no way to tell
// the two apart otherwise.
func (c *Client) ListImages(ctx context.Context) ([]string, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/storage/image", nil)
	if err != nil {
		return nil, err
	}
	var rsp imagesRsp
	if err := json.Unmarshal(raw, &rsp); err != nil {
		return nil, fmt.Errorf("nanokvm: /api/storage/image: unexpected data shape: %w", err)
	}
	return rsp.Files, nil
}

func (c *Client) MountImage(ctx context.Context, file string, cdrom bool) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/storage/image/mount",
		mountImageReq{File: file, Cdrom: cdrom})
	return err
}

func (c *Client) UnmountImage(ctx context.Context) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/storage/image/mount", mountImageReq{})
	return err
}

func (c *Client) MountedImage(ctx context.Context) (map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/storage/image/mounted", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	logUnmarshal("/api/storage/image/mounted", json.Unmarshal(raw, &m))
	return m, nil
}
