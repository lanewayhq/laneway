// Package client is a Go client for the daemon's JSON API over its Unix
// socket. The CLI uses nothing else to talk to the daemon.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"

	"github.com/mrsafalpiya/portby.dev/internal/api"
)

// ErrDaemonNotRunning is returned when nothing is listening on the socket.
var ErrDaemonNotRunning = errors.New("the Portby daemon is not running")

// Client talks to one daemon.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a client for the daemon listening on socket.
func New(socket string) *Client {
	return &Client{
		socket: socket,
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		}},
	}
}

// Projects lists every Linked Project.
func (c *Client) Projects(ctx context.Context) ([]api.Project, error) {
	var list api.ProjectList
	if err := c.do(ctx, http.MethodGet, "/v1/projects", nil, &list); err != nil {
		return nil, err
	}
	return list.Projects, nil
}

// Link registers the repository containing req.Dir as a Project.
func (c *Client) Link(ctx context.Context, req api.LinkRequest) (api.LinkResponse, error) {
	var res api.LinkResponse
	err := c.do(ctx, http.MethodPost, "/v1/projects", req, &res)
	return res, err
}

// Worktree returns the Worktree containing dir, which must be absolute.
func (c *Client) Worktree(ctx context.Context, dir string) (api.WorktreeInfo, error) {
	var info api.WorktreeInfo
	err := c.do(ctx, http.MethodGet, "/v1/worktree?dir="+url.QueryEscape(dir), nil, &info)
	return info, err
}

// Daemon describes the running daemon.
func (c *Client) Daemon(ctx context.Context) (api.DaemonInfo, error) {
	var info api.DaemonInfo
	err := c.do(ctx, http.MethodGet, "/v1/daemon", nil, &info)
	return info, err
}

// do sends one request. A non-2xx response is returned as an *api.Error.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}
	// The host is ignored: every request goes to the socket.
	req, err := http.NewRequestWithContext(ctx, method, "http://portby"+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("%w (no daemon listening on %s)", ErrDaemonNotRunning, c.socket)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || e.Error.Message == "" {
			return fmt.Errorf("daemon returned %s", resp.Status)
		}
		return &e.Error
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
