package client

import (
	"context"
	"net/http"

	"github.com/lanewayhq/laneway/internal/api"
)

// UpdatePlugins updates installed Catalog Plugins (with req.Preview, only
// finds the updates).
func (c *Client) UpdatePlugins(ctx context.Context, req api.UpdatePluginsRequest) (api.UpdatePluginsResponse, error) {
	var res api.UpdatePluginsResponse
	err := c.do(ctx, http.MethodPost, "/v1/plugins/update", req, &res)
	return res, err
}
