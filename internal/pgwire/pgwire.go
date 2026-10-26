// Package pgwire is a small Postgres client: just enough of the frontend/
// backend protocol (v3) for Portby to manage databases in its Postgres
// Service. It logs in (cleartext, MD5 or SCRAM-SHA-256 passwords) and runs
// simple queries, whose results come back as text.
//
// It is written here rather than taken from a driver for the same reason the
// container engine client is: Portby needs a handful of statements, and a
// driver would bring in far more than that.
package pgwire

import (
	"bufio"
	"cmp"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Config says where to connect and as whom.
type Config struct {
	// Addr is the server's host:port.
	Addr     string
	User     string
	Password string
	// Database is the database to connect to.
	Database string
}

// Error is an error the server reported (an ErrorResponse).
type Error struct {
	Severity string // ERROR, FATAL, …
	Code     string // SQLSTATE, such as 55006
	Message  string
	Detail   string
}

func (e *Error) Error() string {
	msg := e.Message
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	return fmt.Sprintf("%s: %s (SQLSTATE %s)", strings.ToLower(e.Severity), msg, e.Code)
}

// SQLSTATE codes Portby acts on.
const (
	// ObjectInUse: CREATE DATABASE … TEMPLATE while others are connected
	// to the template, among others.
	ObjectInUse = "55006"
	// CannotConnectNow: the server is starting up.
	CannotConnectNow = "57P03"
	// AdminShutdown: the connection was closed by pg_terminate_backend
	// (or the server shutting down).
	AdminShutdown = "57P01"
)

// HasCode reports whether err is an *Error with the SQLSTATE code.
func HasCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Conn is a connection to a database. It runs one query at a time.
type Conn struct {
	cfg    Config
	conn   net.Conn
	r      *bufio.Reader
	pid    uint32 // the backend's key, for cancelling a query
	secret uint32
}

// protocolVersion is 3.0.
const protocolVersion = 196608

// cancelRequestCode starts a CancelRequest instead of a StartupMessage.
const cancelRequestCode = 80877102

// Connect connects to cfg.Database and logs in.
func Connect(ctx context.Context, cfg Config) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	c := &Conn{cfg: cfg, conn: nc, r: bufio.NewReader(nc)}
	// No query runs while logging in, so there is nothing to cancel (and
	// the backend's key isn't known yet): the deadline alone cuts it short.
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Now()) })
	err = c.startup()
	if !stop() && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

// Close ends the session.
func (c *Conn) Close() error {
	c.conn.Write(message('X', ""))
	return c.conn.Close()
}

// Exec runs sql (one or more statements), discarding any rows.
func (c *Conn) Exec(ctx context.Context, sql string) error {
	_, err := c.Query(ctx, sql)
	return err
}

// Query runs sql and returns the rows of its results, every value as text
// ("" for NULL). If ctx ends first, the query is cancelled on the server
// and the connection can't be used again.
func (c *Conn) Query(ctx context.Context, sql string) ([][]string, error) {
	var rows [][]string
	err := c.guard(ctx, func() error {
		if _, err := c.conn.Write(message('Q', cstring(sql))); err != nil {
			return err
		}
		var qerr error
		for {
			typ, body, err := c.read()
			if err != nil {
				// A FATAL error (such as the session being
				// terminated) is followed by the server closing
				// the connection: it is the reason.
				return cmp.Or(qerr, err)
			}
			switch typ {
			case 'D':
				rows = append(rows, dataRow(body))
			case 'E':
				qerr = parseError(body)
			case 'Z':
				return qerr
			}
		}
	})
	return rows, err
}

// guard runs f, which talks to the server, so that it stops when ctx ends:
// a query running on the server is cancelled, and the connection's
// deadline cuts f short.
func (c *Conn) guard(ctx context.Context, f func() error) error {
	stop := context.AfterFunc(ctx, func() {
		c.cancel()
		c.conn.SetDeadline(time.Now())
	})
	err := f()
	if !stop() && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// cancel asks the server, on a connection of its own, to cancel the query
// this connection is running. It is best effort.
func (c *Conn) cancel() {
	if c.pid == 0 {
		return
	}
	nc, err := net.DialTimeout("tcp", c.cfg.Addr, 2*time.Second)
	if err != nil {
		return
	}
	defer nc.Close()
	msg := make([]byte, 16)
	binary.BigEndian.PutUint32(msg[0:], 16)
	binary.BigEndian.PutUint32(msg[4:], cancelRequestCode)
	binary.BigEndian.PutUint32(msg[8:], c.pid)
	binary.BigEndian.PutUint32(msg[12:], c.secret)
	nc.SetDeadline(time.Now().Add(2 * time.Second))
	nc.Write(msg)
	io.Copy(io.Discard, nc) // the server closes it once it has the request
}

// startup sends the StartupMessage and handles authentication, up to the
// first ReadyForQuery.
func (c *Conn) startup() error {
	params := cstring("user") + cstring(c.cfg.User) + cstring("database") + cstring(c.cfg.Database) +
		cstring("client_encoding") + cstring("UTF8") + "\x00"
	msg := make([]byte, 8, 8+len(params))
	binary.BigEndian.PutUint32(msg[0:], uint32(8+len(params)))
	binary.BigEndian.PutUint32(msg[4:], protocolVersion)
	if _, err := c.conn.Write(append(msg, params...)); err != nil {
		return err
	}
	var scram *scram
	for {
		typ, body, err := c.read()
		if err != nil {
			return err
		}
		switch typ {
		case 'E':
			return parseError(body)
		case 'K':
			if len(body) >= 8 {
				c.pid, c.secret = binary.BigEndian.Uint32(body), binary.BigEndian.Uint32(body[4:])
			}
		case 'Z':
			return nil
		case 'R':
			if len(body) < 4 {
				return errors.New("pgwire: short authentication message")
			}
			var reply []byte
			switch kind := binary.BigEndian.Uint32(body); kind {
			case 0: // AuthenticationOk
				continue
			case 3: // cleartext password
				reply = message('p', cstring(c.cfg.Password))
			case 5: // MD5 password, salted
				if len(body) < 8 {
					return errors.New("pgwire: short MD5 salt")
				}
				reply = message('p', cstring("md5"+md5Hex(md5Hex(c.cfg.Password+c.cfg.User)+string(body[4:8]))))
			case 10: // SASL: the server lists its mechanisms
				if !strings.Contains(string(body[4:]), scramMechanism+"\x00") {
					return fmt.Errorf("pgwire: the server offers no authentication method this client knows (%q)", body[4:])
				}
				scram = newSCRAM(c.cfg.Password)
				first := scram.clientFirst()
				data := cstring(scramMechanism)
				data += string(binary.BigEndian.AppendUint32(nil, uint32(len(first)))) + first
				reply = message('p', data)
			case 11: // SASL continue
				if scram == nil {
					return errors.New("pgwire: unexpected SASL continue")
				}
				final, err := scram.clientFinal(string(body[4:]))
				if err != nil {
					return err
				}
				reply = message('p', final)
			case 12: // SASL final
				if scram == nil {
					return errors.New("pgwire: unexpected SASL final")
				}
				if err := scram.verify(string(body[4:])); err != nil {
					return err
				}
				continue
			default:
				return fmt.Errorf("pgwire: unsupported authentication method %d", kind)
			}
			if _, err := c.conn.Write(reply); err != nil {
				return err
			}
		}
	}
}

// read reads one backend message.
func (c *Conn) read() (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n < 4 || n > 1<<30 {
		return 0, nil, fmt.Errorf("pgwire: bad message length %d", n)
	}
	body := make([]byte, n-4)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return 0, nil, err
	}
	return head[0], body, nil
}

// message frames a frontend message.
func message(typ byte, body string) []byte {
	msg := make([]byte, 5, 5+len(body))
	msg[0] = typ
	binary.BigEndian.PutUint32(msg[1:], uint32(4+len(body)))
	return append(msg, body...)
}

func cstring(s string) string { return s + "\x00" }

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// dataRow decodes a DataRow's values as text.
func dataRow(body []byte) []string {
	if len(body) < 2 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(body))
	body = body[2:]
	row := make([]string, 0, n)
	for range n {
		if len(body) < 4 {
			break
		}
		size := int32(binary.BigEndian.Uint32(body))
		body = body[4:]
		if size < 0 || int(size) > len(body) {
			row = append(row, "")
			continue
		}
		row = append(row, string(body[:size]))
		body = body[size:]
	}
	return row
}

// parseError decodes an ErrorResponse's fields.
func parseError(body []byte) *Error {
	e := &Error{}
	for _, field := range strings.Split(string(body), "\x00") {
		if field == "" {
			continue
		}
		switch value := field[1:]; field[0] {
		case 'S':
			e.Severity = value
		case 'C':
			e.Code = value
		case 'M':
			e.Message = value
		case 'D':
			e.Detail = value
		}
	}
	return e
}

// Ident quotes name as an identifier: "name", with " doubled.
func Ident(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Literal quotes s as a string literal: 's', with ' doubled. The server
// must have standard_conforming_strings on (the default since 9.1).
func Literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
