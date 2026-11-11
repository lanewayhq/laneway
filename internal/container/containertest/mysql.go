package containertest

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/testkit/mysqlwire"
)

// MySQL is a Server speaking a little of MySQL's protocol, as the mysql image
// configured by MYSQL_USER / MYSQL_PASSWORD would: a caching_sha2_password
// login (the fast path only; the RSA exchange is left to the slow tier), and
// text queries for the statements the tests send, and the MySQL Engine's
// manifest sends through the mysql client (MySQLCommands), matched by
// pattern rather than parsed:
//
//	SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '<db>' AND SCHEMA_NAME NOT IN (SELECT TABLE_SCHEMA FROM information_schema.TABLES WHERE TABLE_SCHEMA = '<db>' AND TABLE_NAME = '<table>')
//	SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME NOT IN (<the system schemas>) ORDER BY SCHEMA_NAME
//	CREATE DATABASE `<db>` [CHARACTER SET … COLLATE …], DROP DATABASE [IF EXISTS] `<db>`, USE `<db>`
//	CREATE TABLE <table> (<column> <type>), DROP TABLE <table>
//	SHOW TABLES, INSERT INTO <table> VALUES ('<text>'), SELECT <column> FROM <table>
//	SET SESSION … (accepted, no effect)
//	SHOW DATABASES
//
// where <table> is name, `name` or `db`.`name`. Databases (each a set of
// one-column tables) are kept in the container's Volume. There are no
// views, routines or triggers: the slow tier copies those with the real
// mysqldump.
func MySQL(ln net.Listener, spec container.Spec, data *Volume) {
	serveMySQLProtocol(mysqlImage, ln, spec, data)
}

// MariaDB is the MySQL Server as the mariadb image configured by
// MARIADB_USER / MARIADB_PASSWORD would be, with the same login and
// statements (MariaDBCommands answers its clients).
func MariaDB(ln net.Listener, spec container.Spec, data *Volume) {
	serveMySQLProtocol(mariadbImage, ln, spec, data)
}

// serveMySQLProtocol serves the MySQL protocol on ln, as image's server would.
func serveMySQLProtocol(image myImage, ln net.Listener, spec container.Spec, data *Volume) {
	my := myServerOf(image, spec, data)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go my.serve(conn)
	}
}

// myImage is what differs between the mysql and mariadb images, for the
// fake: what their server and clients are called, the env naming the user,
// and what their sh and dump client accept.
type myImage struct {
	name      string // the image, for messages
	version   string // the server's version, in its greeting
	envPrefix string // of <prefix>USER and <prefix>PASSWORD
	client    string // runs SQL
	dump      string // dumps a database
	admin     string // pings the server
	// dash is whether the image's sh is dash, which has no pipefail
	// ("set: Illegal option -o pipefail", status 2), unlike bash.
	dash bool
	// gtid is whether the dump client has --set-gtid-purged, MySQL's.
	gtid bool
}

var (
	mysqlImage   = myImage{name: "mysql", version: "8.4.0-laneway-fake", envPrefix: "MYSQL_", client: "mysql", dump: "mysqldump", admin: "mysqladmin", gtid: true}
	mariadbImage = myImage{name: "mariadb", version: "11.8.0-MariaDB-laneway-fake", envPrefix: "MARIADB_", client: "mariadb", dump: "mariadb-dump", admin: "mariadb-admin", dash: true}
)

const myDatabasesKey = "mysql:databases"

// mySystemSchemas are what the server's initialisation creates.
var mySystemSchemas = []string{"information_schema", "mysql", "performance_schema", "sys"}

// myServerOf is the fake container's MySQL server, shared by its port and
// the commands run in it (MySQLCommands, MariaDBCommands).
func myServerOf(image myImage, spec container.Spec, data *Volume) *myServer {
	return data.Shared("mysql:server", func() any {
		my := &myServer{data: data, version: image.version}
		for _, kv := range spec.Env {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case image.envPrefix + "USER":
				my.user = v
			case image.envPrefix + "PASSWORD":
				my.password = v
			}
		}
		if _, ok := data.Get(myDatabasesKey); !ok {
			// What the server's initialisation creates.
			for _, db := range mySystemSchemas {
				my.setDatabase(db, myDatabase{})
			}
		}
		return my
	}).(*myServer)
}

// MySQLCommands answers commands run in a fake mysql container, as the
// image's clients would over its Unix socket, logged in as --user with the
// password in MYSQL_PWD (set with env):
//
//   - mysql --execute=<statements>, run one after another in one session
//     (split at ";", which no statement here holds in a literal) and printed
//     as --batch --skip-column-names prints them: a line per row, values
//     separated by tabs. An error goes to stderr as "ERROR <code> (<state>)
//     at line 1: …" and stops it with status 1.
//   - the manifest's clone script, sh -c '… mysqldump … | mysql …' sh <user>
//     <from> <to> (or bash -c): <to>, dropped first if it is there, made a
//     copy of <from>.
//   - the manifest's drop script, sh -c '… DROP DATABASE …' sh <user> <db>
//     (or bash -c): <db> dropped if it is there (the fake has no other
//     sessions to close).
//   - mysqladmin ping: the server is up as soon as the container is.
//
// A login refused exits 1, as the mysql client does.
func MySQLCommands(_ context.Context, spec container.Spec, data *Volume, cmd []string) container.Output {
	return myCommands(mysqlImage, spec, data, cmd)
}

// MariaDBCommands answers commands run in a fake mariadb container as
// MySQLCommands does, through the image's own client names: mariadb,
// mariadb-dump and mariadb-admin (images from 11.0 have no mysql ones). The
// image's sh is dash, so a clone script setting pipefail must run in bash,
// and mariadb-dump has no --set-gtid-purged: either fails the copy, as the
// real image would.
func MariaDBCommands(_ context.Context, spec container.Spec, data *Volume, cmd []string) container.Output {
	return myCommands(mariadbImage, spec, data, cmd)
}

func myCommands(image myImage, spec container.Spec, data *Volume, cmd []string) container.Output {
	my := myServerOf(image, spec, data)
	password := ""
	for len(cmd) > 0 && (cmd[0] == "env" || strings.Contains(cmd[0], "=")) {
		if v, ok := strings.CutPrefix(cmd[0], "MYSQL_PWD="); ok {
			password = v
		}
		cmd = cmd[1:]
	}
	if len(cmd) == 0 {
		return container.Output{ExitCode: 127, Stderr: []byte("env: no command\n")}
	}
	refused := func(client string, status int, user string) container.Output {
		return container.Output{ExitCode: status, Stderr: fmt.Appendf(nil,
			"%sERROR 1045 (28000): Access denied for user '%s'@'localhost' (using password: YES)\n", client, user)}
	}
	// shellScript reports whether cmd is an sh -c or bash -c script argc
	// words long (the script, $0 and its arguments) that has has in it.
	shellScript := func(argc int, has string) bool {
		return len(cmd) == argc && (cmd[0] == "sh" || cmd[0] == "bash") && cmd[1] == "-c" && strings.Contains(cmd[2], has)
	}
	switch {
	case cmd[0] == image.client:
		var user, sql string
		for _, arg := range cmd[1:] {
			if v, ok := strings.CutPrefix(arg, "--user="); ok {
				user = v
			} else if v, ok := strings.CutPrefix(arg, "--execute="); ok {
				sql = v
			}
		}
		if user != my.user || password != my.password {
			return refused("", 1, user)
		}
		var out container.Output
		var database string
		for stmt := range strings.SplitSeq(sql, ";") {
			if stmt = strings.TrimSpace(stmt); stmt == "" {
				continue
			}
			res := my.query(&database, stmt)
			if res.code != 0 {
				out.ExitCode = 1
				out.Stderr = fmt.Appendf(nil, "ERROR %d (%s) at line 1: %s\n", res.code, res.state, res.message)
				return out
			}
			for _, row := range res.rows {
				out.Stdout = append(out.Stdout, strings.Join(row, "\t")+"\n"...)
			}
		}
		return out
	case cmd[0] == image.admin && slices.Contains(cmd, "ping"):
		return container.Output{} // up as soon as the container is
	case shellScript(7, image.dump+" ") && strings.Contains(cmd[2], image.client+" "):
		user, from, to := cmd[4], cmd[5], cmd[6]
		if image.dash && cmd[0] == "sh" && strings.Contains(cmd[2], "pipefail") {
			return container.Output{ExitCode: 2, Stderr: []byte("sh: 1: set: Illegal option -o pipefail\n")}
		}
		if !image.gtid && strings.Contains(cmd[2], "--set-gtid-purged") {
			return container.Output{ExitCode: 1, Stderr: fmt.Appendf(nil, "%s: unknown variable 'set-gtid-purged=OFF'\n", image.dump)}
		}
		if user != my.user || password != my.password {
			return refused("", 1, user)
		}
		my.mu.Lock()
		defer my.mu.Unlock()
		source, ok := my.database(from)
		if !ok {
			return container.Output{ExitCode: 1, Stderr: fmt.Appendf(nil, "the database %s doesn't exist\n", from)}
		}
		my.removeDatabase(to)
		my.setDatabase(to, source)
		return container.Output{}
	case shellScript(6, "DROP DATABASE") && strings.Contains(cmd[2], image.client+" "):
		user, db := cmd[4], cmd[5]
		if user != my.user || password != my.password {
			return refused("", 1, user)
		}
		my.mu.Lock()
		defer my.mu.Unlock()
		my.removeDatabase(db)
		return container.Output{}
	}
	return container.Output{ExitCode: 127, Stderr: fmt.Appendf(nil, "the fake %s image can't run %q\n", image.name, cmd)}
}

// myTable is a fake table: one text column and its rows.
type myTable struct {
	Column string
	Rows   []string
}

// myDatabase is a fake database: table → its contents.
type myDatabase map[string]*myTable

type myServer struct {
	data           *Volume
	version        string // in the greeting
	user, password string
	nextID         atomic.Uint32

	mu sync.Mutex // guards the databases in data
}

func (my *myServer) databases() []string {
	list, _ := my.data.Get(myDatabasesKey)
	return strings.Fields(list)
}

func (my *myServer) database(name string) (myDatabase, bool) {
	if !slices.Contains(my.databases(), name) {
		return nil, false
	}
	raw, _ := my.data.Get("mysql:db:" + name)
	db := myDatabase{}
	json.Unmarshal([]byte(raw), &db)
	return db, true
}

// removeDatabase forgets the database name, if it exists.
func (my *myServer) removeDatabase(name string) {
	my.data.Set(myDatabasesKey, strings.Join(slices.DeleteFunc(my.databases(), func(n string) bool { return n == name }), " "))
	my.data.Delete("mysql:db:" + name)
}

func (my *myServer) setDatabase(name string, db myDatabase) {
	if !slices.Contains(my.databases(), name) {
		my.data.Set(myDatabasesKey, strings.Join(append(my.databases(), name), " "))
	}
	raw, _ := json.Marshal(db)
	my.data.Set("mysql:db:"+name, string(raw))
}

// mySession is one connection.
type mySession struct {
	conn     net.Conn
	r        *bufio.Reader
	seq      byte
	database string
}

func (s *mySession) read() ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(s.r, head[:]); err != nil {
		return nil, err
	}
	n := int(head[0]) | int(head[1])<<8 | int(head[2])<<16
	s.seq = head[3] + 1
	p := make([]byte, n)
	_, err := io.ReadFull(s.r, p)
	return p, err
}

func (s *mySession) write(p []byte) error {
	n := len(p)
	_, err := s.conn.Write(append([]byte{byte(n), byte(n >> 8), byte(n >> 16), s.seq}, p...))
	s.seq++
	return err
}

func (s *mySession) ok() error {
	return s.write([]byte{0x00, 0, 0, 0x02, 0, 0, 0})
}

func (s *mySession) fail(code uint16, state, message string) error {
	p := binary.LittleEndian.AppendUint16([]byte{0xff}, code)
	return s.write(append(append(p, "#"+state...), message...))
}

func (s *mySession) eof() error {
	return s.write([]byte{0xfe, 0, 0, 0x02, 0})
}

// rows sends a result whose columns are all text.
func (s *mySession) rows(columns []string, rows [][]string) error {
	s.write([]byte{byte(len(columns))})
	for _, name := range columns {
		var def []byte
		for _, f := range []string{"def", "", "", "", name, name} {
			def = append(append(def, byte(len(f))), f...)
		}
		def = append(def, 0x0c, 45, 0) // charset utf8mb4_general_ci
		def = binary.LittleEndian.AppendUint32(def, 1<<16)
		def = append(def, 0xfd, 0, 0, 0, 0, 0) // VAR_STRING, flags, decimals, filler
		s.write(def)
	}
	s.eof()
	for _, row := range rows {
		var p []byte
		for _, v := range row {
			p = append(binary.LittleEndian.AppendUint16(append(p, 0xfc), uint16(len(v))), v...)
		}
		s.write(p)
	}
	return s.eof()
}

// send sends what a statement gave.
func (s *mySession) send(res myResult) error {
	switch {
	case res.code != 0:
		return s.fail(res.code, res.state, res.message)
	case res.columns != nil:
		return s.rows(res.columns, res.rows)
	}
	return s.ok()
}

func (my *myServer) serve(conn net.Conn) {
	defer conn.Close()
	s := &mySession{conn: conn, r: bufio.NewReader(conn)}
	scramble := make([]byte, 20)
	rand.Read(scramble)
	for i := range scramble { // no NULs: the greeting NUL-terminates it
		scramble[i] = scramble[i]%127 + 1
	}
	id := my.nextID.Add(1) + 100
	g := append([]byte{10}, my.version+"\x00"...)
	g = binary.LittleEndian.AppendUint32(g, id)
	g = append(append(g, scramble[:8]...), 0)
	caps := uint32(0xffffffff &^ (1 << 11)) // everything but SSL
	g = binary.LittleEndian.AppendUint16(g, uint16(caps))
	g = append(g, 45, 0x02, 0)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps>>16))
	g = append(g, 21)
	g = append(g, make([]byte, 10)...)
	g = append(append(g, scramble[8:]...), 0)
	g = append(g, "caching_sha2_password\x00"...)
	if s.write(g) != nil {
		return
	}
	resp, err := s.read()
	if err != nil || len(resp) < 32 {
		return
	}
	clientCaps := binary.LittleEndian.Uint32(resp)
	rest := resp[32:]
	user, rest, _ := bytes.Cut(rest, []byte{0})
	var auth []byte
	if len(rest) > 0 {
		n := int(rest[0])
		if n > len(rest)-1 {
			return
		}
		auth, rest = rest[1:1+n], rest[1+n:]
	}
	if clientCaps&(1<<3) != 0 {
		db, _, _ := bytes.Cut(rest, []byte{0})
		s.database = string(db)
	}
	if string(user) != my.user || !bytes.Equal(auth, mysqlwire.ScrambleSHA256(my.password, scramble)) {
		s.fail(1045, "28000", fmt.Sprintf("Access denied for user '%s'@'%%' (using password: YES)", user))
		return
	}
	s.write([]byte{0x01, 0x03}) // fast authentication succeeded
	if s.database != "" {
		my.mu.Lock()
		_, ok := my.database(s.database)
		my.mu.Unlock()
		if !ok {
			s.fail(mysqlwire.BadDatabase, "42000", fmt.Sprintf("Unknown database '%s'", s.database))
			return
		}
	}
	if s.ok() != nil {
		return
	}
	for {
		p, err := s.read()
		if err != nil || p[0] == 0x01 { // COM_QUIT
			return
		}
		if p[0] != 0x03 {
			s.fail(1047, "08S01", "Unknown command")
			continue
		}
		if s.send(my.query(&s.database, strings.TrimSpace(string(p[1:])))) != nil {
			return
		}
	}
}

// myRef matches a table: name, `name` or `database`.`name`.
const myRef = "(?:`(\\w+)`\\.)?`?(\\w+)`?"

var (
	myExistsQuery    = regexp.MustCompile(`^SELECT SCHEMA_NAME FROM information_schema\.SCHEMATA WHERE SCHEMA_NAME = '(\w+)' AND SCHEMA_NAME NOT IN \(SELECT TABLE_SCHEMA FROM information_schema\.TABLES WHERE TABLE_SCHEMA = '\w+' AND TABLE_NAME = '(\w+)'\)$`)
	myCreateDatabase = regexp.MustCompile("^CREATE DATABASE `(\\w+)`(?: CHARACTER SET \\w+ COLLATE \\w+)?$")
	myDropDatabase   = regexp.MustCompile("^DROP DATABASE (IF EXISTS )?`(\\w+)`$")
	myUse            = regexp.MustCompile("^USE `(\\w+)`$")
	mySet            = regexp.MustCompile(`^SET SESSION `)
	myListDatabases  = regexp.MustCompile(`^SHOW DATABASES$`)
	myListOwn        = regexp.MustCompile(`^SELECT SCHEMA_NAME FROM information_schema\.SCHEMATA WHERE SCHEMA_NAME NOT IN \('information_schema', 'mysql', 'performance_schema', 'sys'\) ORDER BY SCHEMA_NAME$`)
	myCreateTable    = regexp.MustCompile(`(?s)^CREATE TABLE ` + myRef + ` \(\s*` + "`?(\\w+)`?" + ` \w+\s*\)$`)
	myDropTable      = regexp.MustCompile(`^DROP TABLE ` + myRef + `$`)
	myInsert         = regexp.MustCompile(`^INSERT INTO ` + myRef + ` VALUES \('((?:[^'\\]|'')*)'\)$`)
	mySelect         = regexp.MustCompile(`^SELECT \w+ FROM ` + myRef + `$`)
)

// myResult is what a statement gave: rows (columns non-nil), nothing (OK),
// or an error (code non-zero).
type myResult struct {
	columns []string
	rows    [][]string
	code    uint16
	state   string
	message string
}

func myFail(code uint16, state, message string) myResult {
	return myResult{code: code, state: state, message: message}
}

// query runs one statement in a session whose default database is
// *database ("" for none), which USE changes.
func (my *myServer) query(database *string, sql string) myResult {
	my.mu.Lock()
	defer my.mu.Unlock()
	noDatabase := func(name string) myResult {
		return myFail(mysqlwire.BadDatabase, "42000", fmt.Sprintf("Unknown database '%s'", name))
	}
	// table finds the table m[i+1] of the database m[i] (s's if "").
	// The result is non-nil if there is no such database.
	table := func(m []string, i int) (myDatabase, string, *myTable, *myResult) {
		dbName := cmp.Or(m[i], *database)
		if dbName == "" {
			fail := myFail(1046, "3D000", "No database selected")
			return nil, "", nil, &fail
		}
		db, ok := my.database(dbName)
		if !ok {
			fail := noDatabase(dbName)
			return nil, "", nil, &fail
		}
		return db, dbName, db[m[i+1]], nil
	}
	noTable := func(db, name string) myResult {
		return myFail(mysqlwire.NoSuchTable, "42S02", fmt.Sprintf("Table '%s.%s' doesn't exist", db, name))
	}

	if sql == "SHOW TABLES" {
		if *database == "" {
			return myFail(1046, "3D000", "No database selected")
		}
		db, _ := my.database(*database)
		var tables [][]string
		for _, t := range slices.Sorted(maps.Keys(db)) {
			tables = append(tables, []string{t})
		}
		return myResult{columns: []string{"Tables_in_" + *database}, rows: tables}
	}
	if myListDatabases.MatchString(sql) {
		var names [][]string
		for _, db := range slices.Sorted(slices.Values(my.databases())) {
			names = append(names, []string{db})
		}
		return myResult{columns: []string{"Database"}, rows: names}
	}
	if myListOwn.MatchString(sql) {
		var names [][]string
		for _, db := range slices.Sorted(slices.Values(my.databases())) {
			if !slices.Contains(mySystemSchemas, db) {
				names = append(names, []string{db})
			}
		}
		return myResult{columns: []string{"SCHEMA_NAME"}, rows: names}
	}
	if m := myExistsQuery.FindStringSubmatch(sql); m != nil {
		var found [][]string
		if db, ok := my.database(m[1]); ok && db[m[2]] == nil {
			found = [][]string{{m[1]}}
		}
		return myResult{columns: []string{"SCHEMA_NAME"}, rows: found}
	}
	if m := myCreateDatabase.FindStringSubmatch(sql); m != nil {
		if _, ok := my.database(m[1]); ok {
			return myFail(1007, "HY000", fmt.Sprintf("Can't create database '%s'; database exists", m[1]))
		}
		my.setDatabase(m[1], myDatabase{})
		return myResult{}
	}
	if m := myDropDatabase.FindStringSubmatch(sql); m != nil {
		if _, ok := my.database(m[2]); !ok {
			if m[1] != "" {
				return myResult{}
			}
			return myFail(1008, "HY000", fmt.Sprintf("Can't drop database '%s'; database doesn't exist", m[2]))
		}
		my.removeDatabase(m[2])
		return myResult{}
	}
	if m := myUse.FindStringSubmatch(sql); m != nil {
		if _, ok := my.database(m[1]); !ok {
			return noDatabase(m[1])
		}
		*database = m[1]
		return myResult{}
	}
	if mySet.MatchString(sql) {
		return myResult{}
	}
	if m := myCreateTable.FindStringSubmatch(sql); m != nil {
		db, dbName, t, err := table(m, 1)
		if err != nil {
			return *err
		}
		if t != nil {
			return myFail(1050, "42S01", fmt.Sprintf("Table '%s' already exists", m[2]))
		}
		db[m[2]] = &myTable{Column: m[3], Rows: []string{}}
		my.setDatabase(dbName, db)
		return myResult{}
	}
	if m := myDropTable.FindStringSubmatch(sql); m != nil {
		db, dbName, t, err := table(m, 1)
		if t == nil {
			return orFail(err, myFail(1051, "42S02", fmt.Sprintf("Unknown table '%s.%s'", dbName, m[2])))
		}
		delete(db, m[2])
		my.setDatabase(dbName, db)
		return myResult{}
	}
	if m := myInsert.FindStringSubmatch(sql); m != nil {
		db, dbName, t, err := table(m, 1)
		if t == nil {
			return orFail(err, noTable(dbName, m[2]))
		}
		t.Rows = append(t.Rows, strings.ReplaceAll(m[3], "''", "'"))
		my.setDatabase(dbName, db)
		return myResult{}
	}
	if m := mySelect.FindStringSubmatch(sql); m != nil {
		_, dbName, t, err := table(m, 1)
		if t == nil {
			return orFail(err, noTable(dbName, m[2]))
		}
		var rows [][]string
		for _, v := range t.Rows {
			rows = append(rows, []string{v})
		}
		return myResult{columns: []string{t.Column}, rows: rows}
	}
	return myFail(1064, "42000", fmt.Sprintf("the fake MySQL doesn't understand %q", sql))
}

// orFail is *fail if there is one, else otherwise.
func orFail(fail *myResult, otherwise myResult) myResult {
	if fail != nil {
		return *fail
	}
	return otherwise
}
