package containertest

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/mail"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/container"
)

// mailbox is the Volume list Mailpit keeps messages in.
const mailbox = "messages"

// MailpitSMTP is a Server speaking enough SMTP to accept mail as Mailpit
// does (any login or none), keeping each message in the container's Volume.
func MailpitSMTP(ln net.Listener, spec container.Spec, data *Volume) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveSMTP(conn, data)
	}
}

func serveSMTP(conn net.Conn, data *Volume) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(s string) bool {
		_, err := io.WriteString(conn, s+"\r\n")
		return err == nil
	}
	if !reply("220 fake Mailpit ESMTP Service ready") {
		return
	}
	for {
		line, err := readLine(r)
		if err != nil {
			return
		}
		verb, _, _ := strings.Cut(strings.ToUpper(line), " ")
		var ok bool
		switch verb {
		case "EHLO":
			ok = reply("250-fake Mailpit\r\n250-AUTH PLAIN LOGIN\r\n250 8BITMIME")
		case "HELO":
			ok = reply("250 fake Mailpit")
		case "AUTH":
			ok = reply("235 2.7.0 Authentication successful")
		case "MAIL", "RCPT", "RSET", "NOOP":
			ok = reply("250 2.0.0 OK")
		case "DATA":
			if !reply("354 Start mail input; end with <CR><LF>.<CR><LF>") {
				return
			}
			var msg strings.Builder
			for {
				l, err := readLine(r)
				if err != nil {
					return
				}
				if l == "." {
					break
				}
				msg.WriteString(strings.TrimPrefix(l, ".") + "\r\n")
			}
			data.Append(mailbox, msg.String())
			ok = reply("250 2.0.0 OK queued")
		case "QUIT":
			reply("221 2.0.0 Bye")
			return
		default:
			ok = reply("502 5.5.1 Command not implemented")
		}
		if !ok {
			return
		}
	}
}

// MailpitWeb is a Server for Mailpit's inbox: a page at / and the messages
// in the container's Volume at /api/v1/messages, newest first, with their
// subjects, in the shape of Mailpit's API.
func MailpitWeb(ln net.Listener, spec container.Spec, data *Volume) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<!doctype html><title>Mailpit</title><h1>Mailpit</h1>\n")
	})
	mux.HandleFunc("GET /api/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		type message struct {
			Subject string
		}
		list := struct {
			Total    int       `json:"total"`
			Messages []message `json:"messages"`
		}{Messages: []message{}}
		stored := data.List(mailbox)
		for _, raw := range slices.Backward(stored) {
			m, err := mail.ReadMessage(strings.NewReader(raw))
			if err != nil {
				continue
			}
			list.Messages = append(list.Messages, message{Subject: m.Header.Get("Subject")})
		}
		list.Total = len(list.Messages)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})
	http.Serve(ln, mux)
}
