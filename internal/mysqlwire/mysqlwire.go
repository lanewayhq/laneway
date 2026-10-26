// Package mysqlwire is a small MySQL client: just enough of the client/server
// protocol for Portby to manage databases in its MySQL Service. It logs in
// (caching_sha2_password, including the RSA exchange a plain-TCP connection
// needs the first time, and mysql_native_password) and runs text queries,
// whose results come back as text.
//
// It is written here rather than taken from a driver for the reasons pgwire
// is: Portby sends a few statements, all on one session whose settings
// (USE, foreign_key_checks) must stick, which database/sql's pool works
// against. The fake MySQL in containertest has to speak the protocol anyway.
package mysqlwire

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Config says where to connect and as whom.
type Config struct {
	// Addr is the server's host:port.
	Addr     string
	User     string
	Password string
	// Database is the default database; "" for none.
	Database string
}

// Error is an error the server reported (an ERR packet).
type Error struct {
	Code    uint16 // such as 1049
	State   string // SQLSTATE, such as 42000
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s (MySQL error %d, SQLSTATE %s)", e.Message, e.Code, e.State)
}

// Error codes Portby acts on.
const (
	// BadDatabase: unknown database.
	BadDatabase = 1049
	// NoSuchTable: a table (or a view's table) doesn't exist.
	NoSuchTable = 1146
)

// HasCode reports whether err is an *Error with the error code.
func HasCode(err error, code uint16) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Capability flags.
const (
	clientLongPassword     = 1 << 0
	clientLongFlag         = 1 << 2
	clientConnectWithDB    = 1 << 3
	clientProtocol41       = 1 << 9
	clientTransactions     = 1 << 13
	clientSecureConnection = 1 << 15
	clientPluginAuth       = 1 << 19
	clientPluginAuthLenenc = 1 << 21
)

// utf8mb4GeneralCI is the connection's character set and collation; it
// exists in every MySQL since 5.5.
const utf8mb4GeneralCI = 45

// maxPacket is the largest payload of one packet; a longer one is split.
const maxPacket = 1<<24 - 1

// Conn is a connection to the server. It runs one query at a time.
type Conn struct {
	cfg  Config
	conn net.Conn
	r    *bufio.Reader
	seq  byte
	id   uint32 // the server's connection id, for KILL QUERY
}

// Connect connects to the server and logs in.
func Connect(ctx context.Context, cfg Config) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	c := &Conn{cfg: cfg, conn: nc, r: bufio.NewReader(nc)}
	// No statement runs while logging in, so there is nothing to kill (and
	// the connection's id isn't known yet): the deadline alone cuts it
	// short.
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Now()) })
	err = c.handshake()
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
	c.seq = 0
	c.write([]byte{comQuit})
	return c.conn.Close()
}

// Commands.
const (
	comQuit  = 0x01
	comQuery = 0x03
)

// Exec runs sql, one statement, discarding any rows.
func (c *Conn) Exec(ctx context.Context, sql string) error {
	_, err := c.Query(ctx, sql)
	return err
}

// Query runs sql, one statement, and returns the rows of its result, every
// value as text ("" for NULL). If ctx ends first, the statement is killed on
// the server and the connection can't be used again.
func (c *Conn) Query(ctx context.Context, sql string) ([][]string, error) {
	var rows [][]string
	err := c.guard(ctx, func() error {
		c.seq = 0
		if err := c.write(append([]byte{comQuery}, sql...)); err != nil {
			return err
		}
		p, err := c.read()
		if err != nil {
			return err
		}
		switch p[0] {
		case 0x00: // OK
			return nil
		case 0xff:
			return parseError(p)
		case 0xfb:
			return errors.New("mysqlwire: the server asked for a local file")
		}
		columns, _, err := lenencInt(p)
		if err != nil {
			return err
		}
		for range columns {
			if _, err := c.read(); err != nil {
				return err
			}
		}
		if p, err := c.read(); err != nil {
			return err
		} else if !isEOF(p) {
			return errors.New("mysqlwire: expected EOF after the column definitions")
		}
		for {
			p, err := c.read()
			if err != nil {
				return err
			}
			if isEOF(p) {
				return nil
			}
			if p[0] == 0xff {
				return parseError(p)
			}
			row, err := textRow(p, int(columns))
			if err != nil {
				return err
			}
			rows = append(rows, row)
		}
	})
	return rows, err
}

// guard runs f, which talks to the server, so that it stops when ctx ends:
// a statement running on the server is killed, and the connection's
// deadline cuts f short.
func (c *Conn) guard(ctx context.Context, f func() error) error {
	stop := context.AfterFunc(ctx, func() {
		c.kill()
		c.conn.SetDeadline(time.Now())
	})
	err := f()
	if !stop() && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// kill asks the server, on a connection of its own, to kill the statement
// this connection is running. It is best effort.
func (c *Conn) kill() {
	if c.id == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := c.cfg
	cfg.Database = ""
	k, err := Connect(ctx, cfg)
	if err != nil {
		return
	}
	defer k.Close()
	k.Exec(ctx, "KILL QUERY "+strconv.FormatUint(uint64(c.id), 10))
}

// handshake reads the server's greeting and logs in.
func (c *Conn) handshake() error {
	p, err := c.read()
	if err != nil {
		return err
	}
	if p[0] == 0xff {
		return parseError(p)
	}
	g, err := parseGreeting(p)
	if err != nil {
		return err
	}
	c.id = g.id
	if g.caps&clientProtocol41 == 0 || g.caps&clientPluginAuth == 0 {
		return errors.New("mysqlwire: the server is too old (no protocol 4.1 or auth plugins)")
	}
	plugin, scramble := g.plugin, g.scramble
	auth, err := authResponse(plugin, c.cfg.Password, scramble)
	if err != nil {
		return err
	}
	caps := uint32(clientLongPassword | clientLongFlag | clientProtocol41 | clientTransactions |
		clientSecureConnection | clientPluginAuth | clientPluginAuthLenenc)
	if c.cfg.Database != "" {
		caps |= clientConnectWithDB
	}
	resp := binary.LittleEndian.AppendUint32(nil, caps)
	resp = binary.LittleEndian.AppendUint32(resp, maxPacket)
	resp = append(resp, utf8mb4GeneralCI)
	resp = append(resp, make([]byte, 23)...)
	resp = append(append(resp, c.cfg.User...), 0)
	resp = append(appendLenenc(resp, uint64(len(auth))), auth...)
	if c.cfg.Database != "" {
		resp = append(append(resp, c.cfg.Database...), 0)
	}
	resp = append(append(resp, plugin...), 0)
	if err := c.write(resp); err != nil {
		return err
	}
	for {
		p, err := c.read()
		if err != nil {
			return err
		}
		switch p[0] {
		case 0x00:
			return nil
		case 0xff:
			return parseError(p)
		case 0xfe: // AuthSwitchRequest: another plugin, a new scramble
			name, data, _ := strings.Cut(string(p[1:]), "\x00")
			plugin, scramble = name, []byte(strings.TrimSuffix(data, "\x00"))
			auth, err := authResponse(plugin, c.cfg.Password, scramble)
			if err != nil {
				return err
			}
			if err := c.write(auth); err != nil {
				return err
			}
		case 0x01: // AuthMoreData
			if plugin != cachingSHA2 || len(p) < 2 {
				return fmt.Errorf("mysqlwire: unexpected auth data for %s", plugin)
			}
			switch {
			case p[1] == 3 && len(p) == 2: // fast auth succeeded; OK follows
			case p[1] == 4 && len(p) == 2: // full auth: ask for the public key
				if err := c.write([]byte{2}); err != nil {
					return err
				}
			default: // the public key
				enc, err := encryptPassword(p[1:], c.cfg.Password, scramble)
				if err != nil {
					return err
				}
				if err := c.write(enc); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("mysqlwire: unexpected packet 0x%02x while logging in", p[0])
		}
	}
}

type greeting struct {
	id       uint32
	caps     uint32
	scramble []byte
	plugin   string
}

// parseGreeting decodes a protocol 10 handshake.
func parseGreeting(p []byte) (greeting, error) {
	var g greeting
	bad := errors.New("mysqlwire: bad greeting")
	if p[0] != 10 {
		return g, fmt.Errorf("mysqlwire: unsupported protocol %d", p[0])
	}
	_, rest, ok := strings.Cut(string(p[1:]), "\x00") // server version
	if !ok || len(rest) < 4+8+1+2 {
		return g, bad
	}
	b := []byte(rest)
	g.id = binary.LittleEndian.Uint32(b)
	g.scramble = append(g.scramble, b[4:12]...)
	b = b[13:]
	g.caps = uint32(binary.LittleEndian.Uint16(b))
	if len(b) < 2+1+2+2+1+10 {
		return g, nil
	}
	g.caps |= uint32(binary.LittleEndian.Uint16(b[5:])) << 16
	authLen := int(b[7])
	b = b[18:]
	if g.caps&clientSecureConnection != 0 {
		n := max(13, authLen-8)
		if len(b) < n {
			return g, bad
		}
		g.scramble = append(g.scramble, b[:n-1]...) // without its NUL
		b = b[n:]
	}
	if g.caps&clientPluginAuth != 0 {
		name, _, _ := strings.Cut(string(b), "\x00")
		g.plugin = name
	}
	return g, nil
}

// read reads one packet's payload, joining a payload split over several.
func (c *Conn) read() ([]byte, error) {
	var payload []byte
	for {
		var head [4]byte
		if _, err := io.ReadFull(c.r, head[:]); err != nil {
			return nil, err
		}
		n := int(head[0]) | int(head[1])<<8 | int(head[2])<<16
		c.seq = head[3] + 1
		start := len(payload)
		payload = append(payload, make([]byte, n)...)
		if _, err := io.ReadFull(c.r, payload[start:]); err != nil {
			return nil, err
		}
		if n < maxPacket {
			if len(payload) == 0 {
				return nil, errors.New("mysqlwire: empty packet")
			}
			return payload, nil
		}
	}
}

// write sends payload as the next packet(s).
func (c *Conn) write(payload []byte) error {
	var buf []byte
	for {
		n := min(len(payload), maxPacket)
		buf = append(buf, byte(n), byte(n>>8), byte(n>>16), c.seq)
		buf = append(buf, payload[:n]...)
		c.seq++
		payload = payload[n:]
		if n < maxPacket {
			break
		}
	}
	_, err := c.conn.Write(buf)
	return err
}

func isEOF(p []byte) bool { return p[0] == 0xfe && len(p) < 9 }

// parseError decodes an ERR packet.
func parseError(p []byte) *Error {
	e := &Error{}
	if len(p) >= 3 {
		e.Code = binary.LittleEndian.Uint16(p[1:])
		p = p[3:]
		if len(p) >= 6 && p[0] == '#' {
			e.State, p = string(p[1:6]), p[6:]
		}
		e.Message = string(p)
	}
	return e
}

// textRow decodes a row of a text result: n length-encoded strings, 0xfb
// for NULL.
func textRow(p []byte, n int) ([]string, error) {
	row := make([]string, 0, n)
	for range n {
		if len(p) == 0 {
			return nil, errors.New("mysqlwire: short row")
		}
		if p[0] == 0xfb {
			row, p = append(row, ""), p[1:]
			continue
		}
		size, used, err := lenencInt(p)
		if err != nil || uint64(len(p)-used) < size {
			return nil, errors.New("mysqlwire: bad row")
		}
		row = append(row, string(p[used:used+int(size)]))
		p = p[used+int(size):]
	}
	return row, nil
}

// lenencInt decodes a length-encoded integer, returning it and how many
// bytes it took.
func lenencInt(p []byte) (uint64, int, error) {
	if len(p) == 0 {
		return 0, 0, errors.New("mysqlwire: short integer")
	}
	var n int
	switch p[0] {
	case 0xfc:
		n = 2
	case 0xfd:
		n = 3
	case 0xfe:
		n = 8
	default:
		return uint64(p[0]), 1, nil
	}
	if len(p) < 1+n {
		return 0, 0, errors.New("mysqlwire: short integer")
	}
	var v uint64
	for i := n; i >= 1; i-- {
		v = v<<8 | uint64(p[i])
	}
	return v, 1 + n, nil
}

func appendLenenc(b []byte, v uint64) []byte {
	switch {
	case v < 251:
		return append(b, byte(v))
	case v < 1<<16:
		return append(b, 0xfc, byte(v), byte(v>>8))
	case v < 1<<24:
		return append(b, 0xfd, byte(v), byte(v>>8), byte(v>>16))
	default:
		return binary.LittleEndian.AppendUint64(append(b, 0xfe), v)
	}
}

// Ident quotes name as an identifier: `name`, with ` doubled.
func Ident(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// Literal quotes s as a string literal: 's', with ' doubled and \ escaped
// (the server must not be in NO_BACKSLASH_ESCAPES mode, the default).
func Literal(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", "''").Replace(s) + "'"
}
