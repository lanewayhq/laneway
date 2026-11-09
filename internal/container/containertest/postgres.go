package containertest

import (
	"bufio"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lanewayhq/laneway/internal/container"
)

// Postgres is a Server speaking a little of Postgres's protocol (v3), as
// the postgres image configured by POSTGRES_USER / POSTGRES_PASSWORD would:
// a cleartext password login, and simple queries for the statements the
// tests send, and Laneway sends through psql (PostgresCommands), matched by
// pattern rather than parsed:
//
//	SELECT 1 FROM pg_database WHERE datname = '<db>'
//	SELECT datname FROM pg_database WHERE NOT datistemplate
//	CREATE DATABASE "<db>" [TEMPLATE "<db>"]
//	DROP DATABASE IF EXISTS "<db>" WITH (FORCE)
//	SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '<db>' AND pid <> pg_backend_pid()
//	SELECT tablename FROM pg_tables WHERE schemaname = 'public'
//	CREATE TABLE <table> (…)
//	INSERT INTO <table> VALUES ('<text>')
//	SELECT <column> FROM <table>
//
// Like the real server, CREATE DATABASE … TEMPLATE fails with SQLSTATE 55006
// while another session is connected to the template, and
// pg_terminate_backend closes those sessions with 57P01. Databases (each a
// set of one-column tables) are kept in the container's Volume.
func Postgres(ln net.Listener, spec container.Spec, data *Volume) {
	pg := pgServerOf(spec, data)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go pg.serve(conn)
	}
}

const pgDatabasesKey = "postgres:databases"

// pgServerOf is the fake container's Postgres server, shared by its port
// and the commands run in it (PostgresCommands), so that psql sees the
// sessions connected over TCP.
func pgServerOf(spec container.Spec, data *Volume) *pgServer {
	return data.Shared("postgres:server", func() any {
		pg := &pgServer{data: data, user: "postgres", sessions: map[*pgSession]bool{}}
		for _, kv := range spec.Env {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "POSTGRES_USER":
				pg.user = v
			case "POSTGRES_PASSWORD":
				pg.password = v
			}
		}
		if _, ok := data.Get(pgDatabasesKey); !ok {
			// What initdb and the image's entrypoint create.
			for _, db := range []string{"postgres", "template0", "template1", pg.user} {
				pg.setDatabase(db, pgDatabase{})
			}
		}
		return pg
	}).(*pgServer)
}

// PostgresCommands answers psql run in a fake postgres container, as the
// image's psql would over its Unix socket (where the image trusts every
// login): one statement given with -c, in database -d as user -U, printed
// as -tA prints it (one row per line, no headers), with -q leaving out
// command tags. An error goes to stderr with its SQLSTATE when VERBOSITY is
// verbose, and psql exits 1; a login refused exits 2.
func PostgresCommands(_ context.Context, spec container.Spec, data *Volume, cmd []string) container.Output {
	if cmd[0] != "psql" {
		return container.Output{ExitCode: 127, Stderr: fmt.Appendf(nil, "%s: command not found\n", cmd[0])}
	}
	pg := pgServerOf(spec, data)
	var user, database, sql string
	var quiet, verbose bool
	for i := 1; i < len(cmd); i++ {
		arg := func() string {
			if i+1 < len(cmd) {
				i++
				return cmd[i]
			}
			return ""
		}
		switch cmd[i] {
		case "-U":
			user = arg()
		case "-d":
			database = arg()
		case "-c":
			sql = arg()
		case "-v":
			verbose = verbose || arg() == "VERBOSITY=verbose"
		case "-q":
			quiet = true
		}
	}
	user = cmp.Or(user, "root")
	database = cmp.Or(database, user)
	pg.mu.Lock()
	defer pg.mu.Unlock()
	refused := func(message string) container.Output {
		return container.Output{ExitCode: 2, Stderr: fmt.Appendf(nil,
			"psql: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: FATAL:  %s\n", message)}
	}
	if user != pg.user && user != "postgres" {
		return refused(fmt.Sprintf("role %q does not exist", user))
	}
	if _, ok := pg.database(database); !ok {
		return refused(fmt.Sprintf("database %q does not exist", database))
	}
	// A session of its own, never in pg.sessions: no one else sees it
	// while pg.mu is held for its one statement.
	s := &pgSession{pid: pg.nextPID.Add(1) + 100, database: database}
	res := pg.query(s, strings.TrimSpace(sql))
	var out container.Output
	switch {
	case res.code != "":
		message := res.message
		if verbose {
			message = res.code + ": " + message
		}
		out.ExitCode, out.Stderr = 1, fmt.Appendf(nil, "ERROR:  %s\n", message)
	case res.column != "":
		for _, row := range res.rows {
			out.Stdout = append(out.Stdout, row+"\n"...)
		}
	case !quiet:
		out.Stdout = []byte(res.tag + "\n")
	}
	return out
}

// pgDatabase is a fake database: table → its rows (one text column).
type pgDatabase map[string][]string

type pgServer struct {
	data           *Volume
	user, password string
	nextPID        atomic.Uint32

	mu       sync.Mutex // guards sessions and the databases in data
	sessions map[*pgSession]bool
}

// pgSession is one logged-in connection.
type pgSession struct {
	conn     net.Conn
	pid      uint32
	database string
	writeMu  sync.Mutex
}

func (s *pgSession) send(typ byte, body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writePG(s.conn, typ, body)
}

func writePG(w io.Writer, typ byte, body []byte) error {
	msg := make([]byte, 5, 5+len(body))
	msg[0] = typ
	binary.BigEndian.PutUint32(msg[1:], uint32(4+len(body)))
	_, err := w.Write(append(msg, body...))
	return err
}

func pgError(severity, code, message string) []byte {
	return []byte("S" + severity + "\x00V" + severity + "\x00C" + code + "\x00M" + message + "\x00\x00")
}

func (pg *pgServer) databases() []string {
	list, _ := pg.data.Get(pgDatabasesKey)
	return strings.Fields(list)
}

func (pg *pgServer) database(name string) (pgDatabase, bool) {
	if !slices.Contains(pg.databases(), name) {
		return nil, false
	}
	raw, _ := pg.data.Get("postgres:db:" + name)
	db := pgDatabase{}
	json.Unmarshal([]byte(raw), &db)
	return db, true
}

func (pg *pgServer) setDatabase(name string, db pgDatabase) {
	if !slices.Contains(pg.databases(), name) {
		pg.data.Set(pgDatabasesKey, strings.Join(append(pg.databases(), name), " "))
	}
	raw, _ := json.Marshal(db)
	pg.data.Set("postgres:db:"+name, string(raw))
}

func (pg *pgServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	params, ok := readStartup(r, conn)
	if !ok {
		return
	}
	fatal := func(code, message string) { writePG(conn, 'E', pgError("FATAL", code, message)) }
	// The entrypoint sets up password authentication for TCP connections.
	writePG(conn, 'R', binary.BigEndian.AppendUint32(nil, 3))
	typ, body, err := readPG(r)
	if err != nil || typ != 'p' {
		return
	}
	if params["user"] != pg.user || strings.TrimSuffix(string(body), "\x00") != pg.password {
		fatal("28P01", fmt.Sprintf("password authentication failed for user %q", params["user"]))
		return
	}
	s := &pgSession{conn: conn, pid: pg.nextPID.Add(1) + 100, database: params["database"]}
	if s.database == "" {
		s.database = params["user"]
	}
	pg.mu.Lock()
	_, exists := pg.database(s.database)
	if exists {
		pg.sessions[s] = true
	}
	pg.mu.Unlock()
	if !exists {
		fatal("3D000", fmt.Sprintf("database %q does not exist", s.database))
		return
	}
	defer func() {
		pg.mu.Lock()
		delete(pg.sessions, s)
		pg.mu.Unlock()
	}()
	s.send('R', binary.BigEndian.AppendUint32(nil, 0))
	s.send('S', []byte("server_version\x0018.0 (laneway fake)\x00"))
	s.send('K', binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, s.pid), 42))
	s.send('Z', []byte("I"))
	for {
		typ, body, err := readPG(r)
		if err != nil || typ == 'X' {
			return
		}
		if typ != 'Q' {
			s.send('E', pgError("ERROR", "08P01", fmt.Sprintf("the fake Postgres only takes simple queries, not %q", typ)))
			s.send('Z', []byte("I"))
			continue
		}
		pg.mu.Lock()
		res := pg.query(s, strings.TrimSpace(strings.TrimSuffix(string(body), "\x00")))
		pg.mu.Unlock()
		s.reply(res)
		if s.send('Z', []byte("I")) != nil {
			return
		}
	}
}

// readStartup reads the StartupMessage's parameters, refusing SSL. A
// CancelRequest (there is never anything to cancel) gets no session.
func readStartup(r *bufio.Reader, conn net.Conn) (map[string]string, bool) {
	for {
		var head [8]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return nil, false
		}
		n, code := binary.BigEndian.Uint32(head[:]), binary.BigEndian.Uint32(head[4:])
		if n < 8 || n > 1<<16 {
			return nil, false
		}
		body := make([]byte, n-8)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, false
		}
		switch code {
		case 80877103: // SSLRequest
			conn.Write([]byte("N"))
			continue
		case 196608: // protocol 3.0
			params := map[string]string{}
			fields := strings.Split(string(body), "\x00")
			for i := 0; i+1 < len(fields); i += 2 {
				params[fields[i]] = fields[i+1]
			}
			return params, true
		default:
			return nil, false
		}
	}
}

func readPG(r *bufio.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n < 4 || n > 1<<20 {
		return 0, nil, fmt.Errorf("bad length %d", n)
	}
	body := make([]byte, n-4)
	_, err := io.ReadFull(r, body)
	return head[0], body, err
}

var (
	pgExistsQuery    = regexp.MustCompile(`^SELECT 1 FROM pg_database WHERE datname = '((?:[^']|'')*)'$`)
	pgListDatabases  = regexp.MustCompile(`^SELECT datname FROM pg_database WHERE NOT datistemplate$`)
	pgDropDatabase   = regexp.MustCompile(`^DROP DATABASE IF EXISTS "((?:[^"]|"")+)" WITH \(FORCE\)$`)
	pgCreateDatabase = regexp.MustCompile(`^CREATE DATABASE "((?:[^"]|"")+)"(?: TEMPLATE "((?:[^"]|"")+)")?$`)
	pgTerminate      = regexp.MustCompile(`^SELECT pg_terminate_backend\(pid\) FROM pg_stat_activity WHERE datname = '((?:[^']|'')*)' AND pid <> pg_backend_pid\(\)$`)
	pgListTables     = regexp.MustCompile(`^SELECT tablename FROM pg_tables WHERE schemaname = 'public'$`)
	pgCreateTable    = regexp.MustCompile(`^CREATE TABLE (\w+) \(.*\)$`)
	pgInsert         = regexp.MustCompile(`^INSERT INTO (\w+) VALUES \('((?:[^']|'')*)'\)$`)
	pgSelect         = regexp.MustCompile(`^SELECT \w+ FROM (\w+)$`)
)

// pgResult is what one statement gave: rows of one column, a command tag,
// or an error (code is its SQLSTATE).
type pgResult struct {
	column        string
	rows          []string
	tag           string
	code, message string
}

// reply sends res to s (not ReadyForQuery).
func (s *pgSession) reply(res pgResult) {
	if res.code != "" {
		s.send('E', pgError("ERROR", res.code, res.message))
		return
	}
	if res.column != "" {
		desc := binary.BigEndian.AppendUint16(nil, 1)
		desc = append(desc, res.column+"\x00"...)
		desc = binary.BigEndian.AppendUint32(desc, 0)      // table OID
		desc = binary.BigEndian.AppendUint16(desc, 0)      // column number
		desc = binary.BigEndian.AppendUint32(desc, 25)     // text
		desc = binary.BigEndian.AppendUint16(desc, 0xffff) // size -1
		desc = binary.BigEndian.AppendUint32(desc, 0xffffffff)
		desc = binary.BigEndian.AppendUint16(desc, 0) // text format
		s.send('T', desc)
		for _, v := range res.rows {
			row := binary.BigEndian.AppendUint16(nil, 1)
			row = binary.BigEndian.AppendUint32(row, uint32(len(v)))
			s.send('D', append(row, v...))
		}
	}
	s.send('C', []byte(res.tag+"\x00"))
}

// query runs one statement for s, which pg.mu must be held for.
func (pg *pgServer) query(s *pgSession, sql string) pgResult {
	fail := func(code, message string) pgResult { return pgResult{code: code, message: message} }
	done := func(tag string) pgResult { return pgResult{tag: tag} }
	rows := func(column string, values []string) pgResult {
		return pgResult{column: column, rows: values, tag: fmt.Sprintf("SELECT %d", len(values))}
	}
	unquote := func(s, q string) string { return strings.ReplaceAll(s, q+q, q) }

	if m := pgExistsQuery.FindStringSubmatch(sql); m != nil {
		var found []string
		if _, ok := pg.database(unquote(m[1], "'")); ok {
			found = []string{"1"}
		}
		return rows("?column?", found)
	}
	if pgListDatabases.MatchString(sql) {
		return rows("datname", slices.DeleteFunc(pg.databases(), func(db string) bool {
			return db == "template0" || db == "template1"
		}))
	}
	if m := pgDropDatabase.FindStringSubmatch(sql); m != nil {
		name := unquote(m[1], `"`)
		if name == s.database {
			return fail("55006", "cannot drop the currently open database")
		}
		pg.terminate(name, s)
		pg.removeDatabase(name)
		return done("DROP DATABASE")
	}
	if m := pgCreateDatabase.FindStringSubmatch(sql); m != nil {
		name, template := unquote(m[1], `"`), cmp.Or(unquote(m[2], `"`), "template1")
		if _, ok := pg.database(name); ok {
			return fail("42P04", fmt.Sprintf("database %q already exists", name))
		}
		source, ok := pg.database(template)
		if !ok {
			return fail("3D000", fmt.Sprintf("template database %q does not exist", template))
		}
		if others := pg.connectedTo(template, s); others > 0 {
			return fail("55006", fmt.Sprintf("source database %q is being accessed by other users", template))
		}
		pg.setDatabase(name, source)
		return done("CREATE DATABASE")
	}
	if m := pgTerminate.FindStringSubmatch(sql); m != nil {
		var closed []string
		for range pg.terminate(unquote(m[1], "'"), s) {
			closed = append(closed, "t")
		}
		return rows("pg_terminate_backend", closed)
	}
	db, _ := pg.database(s.database)
	switch {
	case pgListTables.MatchString(sql):
		var tables []string
		for t := range db {
			tables = append(tables, t)
		}
		slices.Sort(tables)
		return rows("tablename", tables)
	case pgCreateTable.MatchString(sql):
		table := pgCreateTable.FindStringSubmatch(sql)[1]
		if _, ok := db[table]; ok {
			return fail("42P07", fmt.Sprintf("relation %q already exists", table))
		}
		db[table] = []string{}
		pg.setDatabase(s.database, db)
		return done("CREATE TABLE")
	case pgInsert.MatchString(sql):
		m := pgInsert.FindStringSubmatch(sql)
		if _, ok := db[m[1]]; !ok {
			return fail("42P01", fmt.Sprintf("relation %q does not exist", m[1]))
		}
		db[m[1]] = append(db[m[1]], unquote(m[2], "'"))
		pg.setDatabase(s.database, db)
		return done("INSERT 0 1")
	case pgSelect.MatchString(sql):
		table := pgSelect.FindStringSubmatch(sql)[1]
		values, ok := db[table]
		if !ok {
			return fail("42P01", fmt.Sprintf("relation %q does not exist", table))
		}
		return rows("value", values)
	default:
		return fail("42601", fmt.Sprintf("the fake Postgres doesn't understand %q", sql))
	}
}

// terminate closes the sessions other than s connected to db with 57P01,
// and returns them.
func (pg *pgServer) terminate(db string, s *pgSession) []*pgSession {
	var closed []*pgSession
	for other := range pg.sessions {
		if other != s && other.database == db {
			other.send('E', pgError("FATAL", "57P01", "terminating connection due to administrator command"))
			other.conn.Close()
			delete(pg.sessions, other)
			closed = append(closed, other)
		}
	}
	return closed
}

// removeDatabase forgets the database name, if it exists.
func (pg *pgServer) removeDatabase(name string) {
	pg.data.Set(pgDatabasesKey, strings.Join(slices.DeleteFunc(pg.databases(), func(db string) bool { return db == name }), " "))
	pg.data.Delete("postgres:db:" + name)
}

// connectedTo counts the sessions other than s connected to db.
func (pg *pgServer) connectedTo(db string, s *pgSession) int {
	n := 0
	for other := range pg.sessions {
		if other != s && other.database == db {
			n++
		}
	}
	return n
}
