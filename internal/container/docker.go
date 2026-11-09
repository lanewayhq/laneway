package container

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// APIVersion is the Docker Engine API version every request asks for.
// Docker 29 accepts 1.40 to 1.55; Podman 6's compatible API accepts up to
// 1.44.
const APIVersion = "v1.44"

// StopTimeout is how long a container gets to stop after SIGTERM before the
// engine kills it.
const StopTimeout = 10 * time.Second

// Docker is the container engine behind a Docker Engine API socket: Docker's
// own or rootless Podman's.
type Docker struct {
	// Socket is the path of the API's Unix socket.
	Socket string

	once sync.Once
	http *http.Client
}

// NewDocker returns the engine whose API listens on the Unix socket at path.
func NewDocker(socket string) *Docker {
	return &Docker{Socket: socket}
}

func (e *Docker) client() *http.Client {
	e.once.Do(func() {
		e.http = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", e.Socket)
			},
		}}
	})
	return e.http
}

// apiError is a non-2xx answer from the engine.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("container engine: %s (HTTP %d)", e.message, e.status)
}

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == status
}

// do sends one request. A non-nil out is decoded from the JSON response;
// otherwise the body is drained, which for an image pull waits for the pull.
func (e *Docker) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := "http://engine/" + APIVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var msg struct{ Message string }
		if json.Unmarshal(data, &msg) != nil || msg.Message == "" {
			msg.Message = strings.TrimSpace(string(data))
		}
		return &apiError{status: resp.StatusCode, message: msg.Message}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// Ping checks that the engine answers.
func (e *Docker) Ping(ctx context.Context) error {
	return e.do(ctx, http.MethodGet, "/_ping", nil, nil, nil)
}

func (e *Docker) Pull(ctx context.Context, image string) error {
	err := e.do(ctx, http.MethodGet, "/images/"+image+"/json", nil, nil, nil)
	if !isStatus(err, http.StatusNotFound) {
		return err
	}
	return e.pull(ctx, image)
}

// pull pulls image. The engine streams progress as JSON lines and reports a
// failed pull in the stream, after a 200.
func (e *Docker) pull(ctx context.Context, image string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://engine/"+APIVersion+"/images/create?"+url.Values{"fromImage": {image}}.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("pulling %s: %s", image, strings.TrimSpace(string(data)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var msg struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Error != "" {
			return fmt.Errorf("pulling %s: %s", image, msg.Error)
		}
	}
	return sc.Err()
}

// inspected is the part of a container inspection Laneway reads.
type inspected struct {
	State struct {
		Running bool
	}
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIp   string
			HostPort string
		}
	}
}

func (e *Docker) inspect(ctx context.Context, name string) (inspected, error) {
	var c inspected
	err := e.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, nil, &c)
	return c, err
}

// hostAddr is where port is published, from an inspection.
func hostAddr(c inspected, port int) (string, bool) {
	for _, b := range c.NetworkSettings.Ports[strconv.Itoa(port)+"/tcp"] {
		if b.HostPort != "" {
			host := b.HostIp
			if host == "" || host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			return net.JoinHostPort(host, b.HostPort), true
		}
	}
	return "", false
}

func (e *Docker) Running(ctx context.Context, name string, port int) (string, bool, error) {
	c, err := e.inspect(ctx, name)
	if isStatus(err, http.StatusNotFound) {
		return "", false, nil
	}
	if err != nil || !c.State.Running {
		return "", false, err
	}
	addr, _ := hostAddr(c, port)
	return addr, true, nil
}

func (e *Docker) Start(ctx context.Context, spec Spec) (string, error) {
	c, err := e.inspect(ctx, spec.Name)
	if isStatus(err, http.StatusNotFound) {
		if err := e.Pull(ctx, spec.Image); err != nil {
			return "", err
		}
		if err := e.create(ctx, spec); err != nil && !isStatus(err, http.StatusConflict) {
			return "", fmt.Errorf("creating container %s: %w", spec.Name, err)
		}
		c.State.Running = false
	} else if err != nil {
		return "", err
	}
	if !c.State.Running {
		err := e.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(spec.Name)+"/start", nil, nil, nil)
		if err != nil {
			return "", fmt.Errorf("starting container %s: %w", spec.Name, err)
		}
	}
	// The published port can change on every start (Docker picks a new
	// one), so it is read after starting.
	if c, err = e.inspect(ctx, spec.Name); err != nil {
		return "", err
	}
	addr, ok := hostAddr(c, spec.Port)
	if !ok {
		return "", fmt.Errorf("container %s doesn't publish port %d", spec.Name, spec.Port)
	}
	return addr, nil
}

func (e *Docker) create(ctx context.Context, spec Spec) error {
	// Loopback only, on ports the engine picks: Laneway owns the Service's
	// real port and forwards to this one.
	bindings, exposed := map[string]any{}, map[string]any{}
	for _, p := range append([]int{spec.Port}, spec.ExtraPorts...) {
		port := strconv.Itoa(p) + "/tcp"
		bindings[port] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": ""}}
		exposed[port] = map[string]any{}
	}
	host := map[string]any{"PortBindings": bindings}
	if spec.Volume != "" {
		host["Mounts"] = []map[string]string{{"Type": "volume", "Source": spec.Volume, "Target": spec.VolumeTarget}}
	}
	body := map[string]any{
		"Image":        spec.Image,
		"Env":          spec.Env,
		"Labels":       spec.Labels,
		"ExposedPorts": exposed,
		"HostConfig":   host,
	}
	if len(spec.Cmd) > 0 {
		body["Cmd"] = spec.Cmd
	}
	return e.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {spec.Name}}, body, nil)
}

func (e *Docker) Stop(ctx context.Context, name string) error {
	err := e.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/stop",
		url.Values{"t": {strconv.Itoa(int(StopTimeout.Seconds()))}}, nil, nil)
	if isStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// Remove deletes the named container, stopping it first, and its named
// volume (if any). Laneway itself never removes Services; tests clean up with
// it.
func (e *Docker) Remove(ctx context.Context, name, volume string) error {
	err := e.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(name), url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return err
	}
	if volume == "" {
		return nil
	}
	err = e.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(volume), url.Values{"force": {"1"}}, nil, nil)
	if isStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// Exec creates an exec instance in the container, starts it attached and
// reads its output, then asks the engine how it exited. Without a TTY the
// engine sends stdout and stderr multiplexed on one stream (demux).
func (e *Docker) Exec(ctx context.Context, name string, cmd []string) (Output, error) {
	var created struct{ Id string }
	err := e.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/exec", nil,
		map[string]any{"Cmd": cmd, "AttachStdout": true, "AttachStderr": true}, &created)
	if err != nil {
		return Output{}, fmt.Errorf("running %s in container %s: %w", cmd[0], name, err)
	}
	body, err := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return Output{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://engine/"+APIVersion+"/exec/"+url.PathEscape(created.Id)+"/start", bytes.NewReader(body))
	if err != nil {
		return Output{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client().Do(req)
	if err != nil {
		return Output{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return Output{}, fmt.Errorf("running %s in container %s: %w", cmd[0], name,
			&apiError{status: resp.StatusCode, message: strings.TrimSpace(string(data))})
	}
	var out Output
	if err := demux(resp.Body, &out); err != nil {
		return Output{}, fmt.Errorf("reading the output of %s in container %s: %w", cmd[0], name, err)
	}
	// The stream ends when the command exits, but the engine may record
	// its exit code a moment later.
	for {
		var inspected struct {
			Running  bool
			ExitCode int
		}
		if err := e.do(ctx, http.MethodGet, "/exec/"+url.PathEscape(created.Id)+"/json", nil, nil, &inspected); err != nil {
			return Output{}, err
		}
		if !inspected.Running {
			out.ExitCode = inspected.ExitCode
			return out, nil
		}
		select {
		case <-ctx.Done():
			return Output{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// demux splits the engine's multiplexed stream into out's Stdout and
// Stderr: frames of an 8-byte header (stream, three zero bytes, big-endian
// length) and that many bytes.
func demux(r io.Reader, out *Output) error {
	br := bufio.NewReader(r)
	var head [8]byte
	for {
		if _, err := io.ReadFull(br, head[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		n := int64(binary.BigEndian.Uint32(head[4:]))
		var dst *[]byte
		switch head[0] {
		case 1:
			dst = &out.Stdout
		case 2:
			dst = &out.Stderr
		default:
			if _, err := io.CopyN(io.Discard, br, n); err != nil {
				return err
			}
			continue
		}
		var buf bytes.Buffer
		if _, err := io.CopyN(&buf, br, n); err != nil {
			return err
		}
		*dst = append(*dst, buf.Bytes()...)
	}
}
