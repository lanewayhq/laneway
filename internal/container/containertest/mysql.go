package containertest

import (
	"bufio"
	"bytes"
	"cmp"
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

	"github.com/mrsafalpiya/portby.dev/internal/container"
	"github.com/mrsafalpiya/portby.dev/internal/mysqlwire"
)

// MySQL is a Server speaking a little of MySQL's protocol, as the mysql image
// configured by MYSQL_USER / MYSQL_PASSWORD would: a caching_sha2_password
// login (the fast path only; the RSA exchange is left to the slow tier), and
// text queries for the statements Portby and the tests send, matched by
// pattern rather than parsed:
//
//	SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '<db>'
//	SELECT TABLE_NAME[, TABLE_TYPE] FROM information_schema.TABLES WHERE TABLE_SCHEMA = '<db>' …
//	SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE … (the table's one column)
//	SELECT … FROM information_schema.ROUTINES / TRIGGERS … (always none)
//	CREATE DATABASE `<db>` [CHARACTER SET … COLLATE …], DROP DATABASE [IF EXISTS] `<db>`, USE `<db>`
//	SHOW CREATE TABLE <table>, CREATE TABLE <table> (<column> <type>), DROP TABLE <table>
//	INSERT INTO <table> (`<column>`) SELECT `<column>` FROM <table>
//	SHOW TABLES, INSERT INTO <table> VALUES ('<text>'), SELECT <column> FROM <table>
//	SET SESSION …, KILL QUERY <id> (accepted, no effect)
//
// where <table> is name, `name` or `db`.`name`. Databases (each a set of
// one-column tables) are kept in the container's Volume. There are no
// views, routines or triggers: the slow tier copies those.
func MySQL(ln net.Listener, spec container.Spec, data *Volume) {
	my := &myServer{data: data}
	for _, kv := range spec.Env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "MYSQL_USER":
			my.user = v
		case "MYSQL_PASSWORD":
			my.password = v
		}
	}
	if _, ok := data.Get(myDatabasesKey); !ok {
		// What the server's initialisation creates.
		for _, db := range []string{"information_schema", "mysql", "performance_schema", "sys"} {
			my.setDatabase(db, myDatabase{})
		}
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go my.serve(conn)
	}
}

const myDatabasesKey = "mysql:databases"

// myTable is a fake table: one text column and its rows.
type myTable struct {
	Column string
	Rows   []string
}

// myDatabase is a fake database: table → its contents.
type myDatabase map[string]*myTable

type myServer struct {
	data           *Volume
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

func (my *myServer) serve(conn net.Conn) {
	defer conn.Close()
	s := &mySession{conn: conn, r: bufio.NewReader(conn)}
	scramble := make([]byte, 20)
	rand.Read(scramble)
	for i := range scramble { // no NULs: the greeting NUL-terminates it
		scramble[i] = scramble[i]%127 + 1
	}
	id := my.nextID.Add(1) + 100
	g := append([]byte{10}, "8.4.0-portby-fake\x00"...)
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
		if my.query(s, strings.TrimSpace(string(p[1:]))) != nil {
			return
		}
	}
}

// myRef matches a table: name, `name` or `database`.`name`.
const myRef = "(?:`(\\w+)`\\.)?`?(\\w+)`?"

var (
	mySchemaQuery    = regexp.MustCompile(`^SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema\.SCHEMATA WHERE SCHEMA_NAME = '(\w+)'$`)
	myTablesQuery    = regexp.MustCompile(`^SELECT TABLE_NAME, TABLE_TYPE FROM information_schema\.TABLES WHERE TABLE_SCHEMA = '(\w+)' ORDER BY TABLE_NAME$`)
	myTableQuery     = regexp.MustCompile(`^SELECT TABLE_NAME FROM information_schema\.TABLES WHERE TABLE_SCHEMA = '(\w+)' AND TABLE_NAME = '(\w+)'$`)
	myColumnsQuery   = regexp.MustCompile(`^SELECT COLUMN_NAME FROM information_schema\.COLUMNS WHERE TABLE_SCHEMA = '(\w+)' AND TABLE_NAME = '(\w+)' AND GENERATION_EXPRESSION = '' ORDER BY ORDINAL_POSITION$`)
	myObjectsQuery   = regexp.MustCompile(`^SELECT (ROUTINE_NAME, ROUTINE_TYPE|TRIGGER_NAME) FROM information_schema\.(?:ROUTINES|TRIGGERS) WHERE \w+ = '\w+' ORDER BY [\w, ]+$`)
	myCreateDatabase = regexp.MustCompile("^CREATE DATABASE `(\\w+)`(?: CHARACTER SET \\w+ COLLATE \\w+)?$")
	myDropDatabase   = regexp.MustCompile("^DROP DATABASE (IF EXISTS )?`(\\w+)`$")
	myUse            = regexp.MustCompile("^USE `(\\w+)`$")
	mySet            = regexp.MustCompile(`^SET SESSION `)
	myKill           = regexp.MustCompile(`^KILL QUERY \d+$`)
	myShowCreate     = regexp.MustCompile(`^SHOW CREATE TABLE ` + myRef + `$`)
	myCreateTable    = regexp.MustCompile(`(?s)^CREATE TABLE ` + myRef + ` \(\s*` + "`?(\\w+)`?" + ` \w+\s*\)$`)
	myDropTable      = regexp.MustCompile(`^DROP TABLE ` + myRef + `$`)
	myInsert         = regexp.MustCompile(`^INSERT INTO ` + myRef + ` VALUES \('((?:[^'\\]|'')*)'\)$`)
	myInsertSelect   = regexp.MustCompile(`^INSERT INTO ` + myRef + " \\(`\\w+`\\) SELECT `\\w+` FROM " + myRef + `$`)
	mySelect         = regexp.MustCompile(`^SELECT \w+ FROM ` + myRef + `$`)
)

// query runs one statement for s and sends its result.
func (my *myServer) query(s *mySession, sql string) error {
	my.mu.Lock()
	defer my.mu.Unlock()
	noDatabase := func(name string) error {
		return s.fail(mysqlwire.BadDatabase, "42000", fmt.Sprintf("Unknown database '%s'", name))
	}
	// table finds the table m[i+1] of the database m[i] (s's if "").
	table := func(m []string, i int) (myDatabase, string, *myTable, error) {
		dbName := cmp.Or(m[i], s.database)
		if dbName == "" {
			return nil, "", nil, s.fail(1046, "3D000", "No database selected")
		}
		db, ok := my.database(dbName)
		if !ok {
			return nil, "", nil, noDatabase(dbName)
		}
		return db, dbName, db[m[i+1]], nil
	}
	noTable := func(db, name string) error {
		return s.fail(mysqlwire.NoSuchTable, "42S02", fmt.Sprintf("Table '%s.%s' doesn't exist", db, name))
	}

	if sql == "SHOW TABLES" {
		if s.database == "" {
			return s.fail(1046, "3D000", "No database selected")
		}
		db, _ := my.database(s.database)
		var tables [][]string
		for _, t := range slices.Sorted(maps.Keys(db)) {
			tables = append(tables, []string{t})
		}
		return s.rows([]string{"Tables_in_" + s.database}, tables)
	}
	if m := mySchemaQuery.FindStringSubmatch(sql); m != nil {
		var found [][]string
		if _, ok := my.database(m[1]); ok {
			found = [][]string{{"utf8mb4", "utf8mb4_0900_ai_ci"}}
		}
		return s.rows([]string{"DEFAULT_CHARACTER_SET_NAME", "DEFAULT_COLLATION_NAME"}, found)
	}
	if m := myTablesQuery.FindStringSubmatch(sql); m != nil {
		db, _ := my.database(m[1])
		var tables [][]string
		for _, t := range slices.Sorted(maps.Keys(db)) {
			tables = append(tables, []string{t, "BASE TABLE"})
		}
		return s.rows([]string{"TABLE_NAME", "TABLE_TYPE"}, tables)
	}
	if m := myTableQuery.FindStringSubmatch(sql); m != nil {
		db, _ := my.database(m[1])
		var found [][]string
		if _, ok := db[m[2]]; ok {
			found = [][]string{{m[2]}}
		}
		return s.rows([]string{"TABLE_NAME"}, found)
	}
	if m := myColumnsQuery.FindStringSubmatch(sql); m != nil {
		db, _ := my.database(m[1])
		var found [][]string
		if t, ok := db[m[2]]; ok {
			found = [][]string{{t.Column}}
		}
		return s.rows([]string{"COLUMN_NAME"}, found)
	}
	if m := myObjectsQuery.FindStringSubmatch(sql); m != nil {
		return s.rows(strings.Split(m[1], ", "), nil) // the fake has no routines or triggers
	}
	if m := myCreateDatabase.FindStringSubmatch(sql); m != nil {
		if _, ok := my.database(m[1]); ok {
			return s.fail(1007, "HY000", fmt.Sprintf("Can't create database '%s'; database exists", m[1]))
		}
		my.setDatabase(m[1], myDatabase{})
		return s.ok()
	}
	if m := myDropDatabase.FindStringSubmatch(sql); m != nil {
		if _, ok := my.database(m[2]); !ok {
			if m[1] != "" {
				return s.ok()
			}
			return s.fail(1008, "HY000", fmt.Sprintf("Can't drop database '%s'; database doesn't exist", m[2]))
		}
		my.data.Set(myDatabasesKey, strings.Join(slices.DeleteFunc(my.databases(), func(n string) bool { return n == m[2] }), " "))
		return s.ok()
	}
	if m := myUse.FindStringSubmatch(sql); m != nil {
		if _, ok := my.database(m[1]); !ok {
			return noDatabase(m[1])
		}
		s.database = m[1]
		return s.ok()
	}
	if mySet.MatchString(sql) || myKill.MatchString(sql) {
		return s.ok()
	}
	if m := myShowCreate.FindStringSubmatch(sql); m != nil {
		_, dbName, t, err := table(m, 1)
		if t == nil {
			return cmp.Or(err, noTable(dbName, m[2]))
		}
		return s.rows([]string{"Table", "Create Table"},
			[][]string{{m[2], "CREATE TABLE `" + m[2] + "` (\n  `" + t.Column + "` text\n)"}})
	}
	if m := myCreateTable.FindStringSubmatch(sql); m != nil {
		db, dbName, t, err := table(m, 1)
		if err != nil {
			return err
		}
		if t != nil {
			return s.fail(1050, "42S01", fmt.Sprintf("Table '%s' already exists", m[2]))
		}
		db[m[2]] = &myTable{Column: m[3], Rows: []string{}}
		my.setDatabase(dbName, db)
		return s.ok()
	}
	if m := myDropTable.FindStringSubmatch(sql); m != nil {
		db, dbName, t, err := table(m, 1)
		if t == nil {
			return cmp.Or(err, s.fail(1051, "42S02", fmt.Sprintf("Unknown table '%s.%s'", dbName, m[2])))
		}
		delete(db, m[2])
		my.setDatabase(dbName, db)
		return s.ok()
	}
	if m := myInsert.FindStringSubmatch(sql); m != nil {
		db, dbName, t, err := table(m, 1)
		if t == nil {
			return cmp.Or(err, noTable(dbName, m[2]))
		}
		t.Rows = append(t.Rows, strings.ReplaceAll(m[3], "''", "'"))
		my.setDatabase(dbName, db)
		return s.ok()
	}
	if m := myInsertSelect.FindStringSubmatch(sql); m != nil {
		db, dbName, t, err := table(m, 1)
		if t == nil {
			return cmp.Or(err, noTable(dbName, m[2]))
		}
		_, fromName, from, err := table(m, 3)
		if from == nil {
			return cmp.Or(err, noTable(fromName, m[4]))
		}
		t.Rows = append(t.Rows, from.Rows...)
		my.setDatabase(dbName, db)
		return s.ok()
	}
	if m := mySelect.FindStringSubmatch(sql); m != nil {
		_, dbName, t, err := table(m, 1)
		if t == nil {
			return cmp.Or(err, noTable(dbName, m[2]))
		}
		var rows [][]string
		for _, v := range t.Rows {
			rows = append(rows, []string{v})
		}
		return s.rows([]string{t.Column}, rows)
	}
	return s.fail(1064, "42000", fmt.Sprintf("the fake MySQL doesn't understand %q", sql))
}
