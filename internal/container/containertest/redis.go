package containertest

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/container"
)

// Redis is a Server speaking a little of Redis's protocol: AUTH, PING, ECHO,
// SET, GET and QUIT, with keys kept in the container's Volume. It
// requires AUTH when the container's command has `--requirepass <password>`,
// as the real server does.
func Redis(ln net.Listener, spec container.Spec, data *Volume) {
	password := ""
	if i := slices.Index(spec.Cmd, "--requirepass"); i >= 0 && i+1 < len(spec.Cmd) {
		password = spec.Cmd[i+1]
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveRedis(conn, password, data)
	}
}

func serveRedis(conn net.Conn, password string, data *Volume) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	authed := password == ""
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		if len(args) == 0 {
			continue
		}
		cmd := strings.ToUpper(args[0])
		var reply string
		switch {
		case cmd == "QUIT":
			io.WriteString(conn, "+OK\r\n")
			return
		case cmd == "AUTH":
			if len(args) >= 2 && args[len(args)-1] == password {
				authed = true
				reply = "+OK"
			} else {
				reply = "-WRONGPASS invalid username-password pair or user is disabled."
			}
		case !authed:
			reply = "-NOAUTH Authentication required."
		case cmd == "PING":
			reply = "+PONG"
		case cmd == "ECHO" && len(args) == 2:
			reply = bulk(args[1])
		case cmd == "SET" && len(args) >= 3:
			data.Set(args[1], args[2])
			reply = "+OK"
		case cmd == "GET" && len(args) == 2:
			if v, ok := data.Get(args[1]); ok {
				reply = bulk(v)
			} else {
				reply = "$-1"
			}
		default:
			reply = fmt.Sprintf("-ERR unknown command '%s'", args[0])
		}
		if _, err := io.WriteString(conn, reply+"\r\n"); err != nil {
			return
		}
	}
}

func bulk(s string) string { return "$" + strconv.Itoa(len(s)) + "\r\n" + s }

// readCommand reads a RESP array of bulk strings, or an inline command (a
// line of words, as `PING\r\n`).
func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return strings.Fields(line), nil
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for range n {
		head, err := readLine(r)
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimPrefix(head, "$"))
		if err != nil || !strings.HasPrefix(head, "$") {
			return nil, fmt.Errorf("bad bulk string header %q", head)
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}
