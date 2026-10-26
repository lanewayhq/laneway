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

// WorktreeReady finds the Worktree containing req.Dir, begins (or with
// req.Retry, retries) its setup and returns its state. With req.Wait it
// returns once the Worktree is Ready or its setup failed: check
// Worktree.Setup.State.
func (c *Client) WorktreeReady(ctx context.Context, req api.ReadyRequest) (api.WorktreeInfo, error) {
	var info api.WorktreeInfo
	err := c.do(ctx, http.MethodPost, "/v1/worktree/ready", req, &info)
	return info, err
}

// AddAlias adds the Alias req.Name to the Worktree containing req.Dir.
func (c *Client) AddAlias(ctx context.Context, req api.AliasRequest) (api.AliasResponse, error) {
	var res api.AliasResponse
	err := c.do(ctx, http.MethodPost, "/v1/aliases", req, &res)
	return res, err
}

// RemoveAlias removes the Alias name from the Worktree containing dir.
func (c *Client) RemoveAlias(ctx context.Context, dir, name string) (api.AliasResponse, error) {
	var res api.AliasResponse
	err := c.do(ctx, http.MethodDelete, "/v1/aliases/"+url.PathEscape(name)+"?dir="+url.QueryEscape(dir), nil, &res)
	return res, err
}

// Daemon describes the running daemon.
func (c *Client) Daemon(ctx context.Context) (api.DaemonInfo, error) {
	var info api.DaemonInfo
	err := c.do(ctx, http.MethodGet, "/v1/daemon", nil, &info)
	return info, err
}

// Control acts on a Worktree or one of its Processes: action is "up", "down"
// or "restart". It returns the Worktree afterwards.
func (c *Client) Control(ctx context.Context, action string, req api.ControlRequest) (api.Worktree, error) {
	var w api.Worktree
	err := c.do(ctx, http.MethodPost, "/v1/worktree/"+action, req, &w)
	return w, err
}

// LogsRequest picks which Process output Logs returns.
type LogsRequest struct {
	// Dir is an absolute path inside the Worktree whose output is wanted.
	Dir string
	// Process names one Process; empty means all of the Worktree's.
	Process string
	// Follow keeps reading new output until ctx is done or the daemon stops.
	Follow bool
}

// Logs calls fn with each line of Process output, oldest first.
func (c *Client) Logs(ctx context.Context, req LogsRequest, fn func(api.LogLine) error) error {
	q := url.Values{"dir": {req.Dir}}
	if req.Process != "" {
		q.Set("process", req.Process)
	}
	if req.Follow {
		q.Set("follow", "true")
	}
	resp, err := c.send(ctx, http.MethodGet, "/v1/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var line api.LogLine
		err := dec.Decode(&line)
		if errors.Is(err, io.EOF) || (err != nil && ctx.Err() != nil) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(line); err != nil {
			return err
		}
	}
}

// do sends one request and decodes the response into out.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// send sends one request and returns the response if it is a 2xx. Anything
// else is returned as an *api.Error.
func (c *Client) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	// The host is ignored: every request goes to the socket.
	req, err := http.NewRequestWithContext(ctx, method, "http://portby"+path, reqBody)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("%w (no daemon listening on %s)", ErrDaemonNotRunning, c.socket)
		}
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		var e api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || e.Error.Message == "" {
			return nil, fmt.Errorf("daemon returned %s", resp.Status)
		}
		return nil, &e.Error
	}
	return resp, nil
}
