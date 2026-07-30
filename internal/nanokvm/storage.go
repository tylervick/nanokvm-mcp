package nanokvm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ListImages returns the disk image paths the firmware offers for mounting.
//
// Upstream answers with proto.GetImagesRsp, so the payload is {"files": [...]}
// rather than a bare array. A payload we cannot read is an error here, not an
// empty list: "this device has no images" is itself a valid answer, and the
// caller has no way to tell the two apart otherwise.
func (c *Client) ListImages(ctx context.Context) ([]string, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/storage/image", nil)
	if err != nil {
		return nil, err
	}
	var rsp struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &rsp); err != nil {
		return nil, fmt.Errorf("nanokvm: /api/storage/image: unexpected data shape: %w", err)
	}
	return rsp.Files, nil
}

func (c *Client) MountImage(ctx context.Context, file string, cdrom bool) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/storage/image/mount",
		map[string]any{"file": file, "cdrom": cdrom})
	return err
}

func (c *Client) UnmountImage(ctx context.Context) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/storage/image/mount", map[string]any{})
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
