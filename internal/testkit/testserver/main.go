// Command testserver is the tiny HTTP server tests declare as a Process. It
// listens on 127.0.0.1 at --port, or at $PORT when --port is not given (after
// waiting --delay, to stand in for a slow-booting app), and
// answers every request with a JSON description of itself and the request.
// Requests to /ws are upgraded to a WebSocket that echoes back every byte it
// receives.
//
// With --exit-when it exits with status 1 once the given file exists
// (checked from when it starts listening), like an app that crashes once it
// is up. The test decides when, so the crash can't race the test's requests.
//
// With --ready-file it creates the given file once it is listening, so a
// test can tell the moment it accepts connections.
//
// With --bind-only it stands in for an app that never finishes booting: it
// binds the port, so nothing else can take it, but never listens, so
// connections are refused.
//
// It exits when the process that started it (the daemon) is gone, so a test
// binary that is killed (a timeout, Ctrl-C) doesn't leave it running. Start
// it with `exec` for that to work.
package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Response is what the server answers with. Keep it in step with
// testkit.ServerResponse, which tests decode it into.
type Response struct {
	Name           string `json:"name"`
	ListenPort     string `json:"listenPort"`
	EnvPort        string `json:"envPort"`
	Host           string `json:"host"`
	Path           string `json:"path"`
	ForwardedProto string `json:"forwardedProto"`
}

func main() {
	port := flag.String("port", os.Getenv("PORT"), "port to listen on (default $PORT)")
	name := flag.String("name", "testserver", "name to report in responses")
	delay := flag.Duration("delay", 0, "how long to wait before listening")
	bindOnly := flag.Bool("bind-only", false, "bind the port but never listen")
	exitWhen := flag.String("exit-when", "", "once listening, exit with status 1 as soon as this file exists")
	readyFile := flag.String("ready-file", "", "create this file once listening")
	flag.Parse()
	go exitWhenOrphaned()
	if *port == "" {
		fmt.Fprintln(os.Stderr, "testserver: no --port and no $PORT")
		os.Exit(2)
	}
	if *bindOnly {
		if err := bind(*port); err != nil {
			fmt.Fprintln(os.Stderr, "testserver:", err)
			os.Exit(1)
		}
		select {}
	}
	time.Sleep(*delay)
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", *port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "testserver:", err)
		os.Exit(1)
	}
	_, listenPort, _ := net.SplitHostPort(ln.Addr().String())
	if *exitWhen != "" {
		go func() {
			for {
				if _, err := os.Stat(*exitWhen); err == nil {
					fmt.Fprintln(os.Stderr, "testserver: exiting as asked")
					os.Exit(1)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	if *readyFile != "" {
		if err := os.WriteFile(*readyFile, nil, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "testserver:", err)
			os.Exit(1)
		}
	}
	err = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			echoWebSocket(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{
			Name:           *name,
			ListenPort:     listenPort,
			EnvPort:        os.Getenv("PORT"),
			Host:           r.Host,
			Path:           r.URL.Path,
			ForwardedProto: r.Header.Get("X-Forwarded-Proto"),
		})
	}))
	fmt.Fprintln(os.Stderr, "testserver:", err)
	os.Exit(1)
}

// exitWhenOrphaned exits once the process that started testserver has
// exited, which shows as testserver getting a new parent.
func exitWhenOrphaned() {
	parent := os.Getppid()
	for {
		time.Sleep(100 * time.Millisecond)
		if os.Getppid() != parent {
			fmt.Fprintln(os.Stderr, "testserver: whatever started me has exited")
			os.Exit(1)
		}
	}
}

// bind binds a TCP socket to 127.0.0.1:port without listening on it.
func bind(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	// Like net.Listen, so a connection in TIME_WAIT from an earlier server
	// on this port doesn't stop it.
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		return err
	}
	return syscall.Bind(fd, &syscall.SockaddrInet4{Port: n, Addr: [4]byte{127, 0, 0, 1}})
}

// echoWebSocket completes the WebSocket handshake and then echoes raw bytes.
// It doesn't parse frames: tests only need to see that the upgraded
// connection carries data both ways through the proxy.
func echoWebSocket(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected a WebSocket upgrade", http.StatusBadRequest)
		return
	}
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	rw.Flush()
	io.Copy(conn, rw)
}
