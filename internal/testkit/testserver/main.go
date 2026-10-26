// Command testserver is the tiny HTTP server tests declare as a Process. It
// listens on 127.0.0.1 at --port, or at $PORT when --port is not given, and
// answers every request with a JSON description of itself and the request.
// Requests to /ws are upgraded to a WebSocket that echoes back every byte it
// receives.
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
	"strings"
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
	flag.Parse()
	if *port == "" {
		fmt.Fprintln(os.Stderr, "testserver: no --port and no $PORT")
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", *port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "testserver:", err)
		os.Exit(1)
	}
	_, listenPort, _ := net.SplitHostPort(ln.Addr().String())
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
