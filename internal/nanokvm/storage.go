package nanokvm

import (
	"context"
	"encoding/json"
	"net/http"
)

func (c *Client) ListImages(ctx context.Context) ([]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/storage/image", nil)
	if err != nil {
		return nil, err
	}
	var out []any
	logUnmarshal("/api/storage/image", json.Unmarshal(raw, &out))
	return out, nil
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
