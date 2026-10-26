package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// Postgres is the Postgres engine. Its superuser is the shared User, with
// the shared Password; the image creates it on the first start.
//
// The data directory is set explicitly (PGDATA) because the images moved it
// in Postgres 18 (to /var/lib/postgresql/<major>/docker, with the volume at
// /var/lib/postgresql): with PGDATA and the volume at the old place, every
// version keeps its data in Portby's volume the same way.
var Postgres = &Engine{
	Name:           "postgres",
	DefaultVersion: "18",
	Port:           5432,
	Image:          "docker.io/library/postgres",
	DataDir:        postgresData,
	User:           User,
	Env: func(string) []string {
		return []string{"POSTGRES_USER=" + User, "POSTGRES_PASSWORD=" + Password, "PGDATA=" + postgresData}
	},
	Probe: probePostgres,
}

const postgresData = "/var/lib/postgresql/data"

// probePostgres sends a StartupMessage, as pg_isready does, and reads the
// first reply. An authentication request means the server accepts
// connections; so does any error other than 57P03 ("the database system is
// starting up"), such as a missing database. The image's first start runs
// a temporary server on a Unix socket only, so TCP connections fail until
// the real one listens.
func probePostgres(ctx context.Context, conn net.Conn) error {
	params := "user\x00" + User + "\x00database\x00postgres\x00\x00"
	msg := make([]byte, 8, 8+len(params))
	binary.BigEndian.PutUint32(msg[0:], uint32(8+len(params)))
	binary.BigEndian.PutUint32(msg[4:], 196608) // protocol 3.0
	if _, err := conn.Write(append(msg, params...)); err != nil {
		return err
	}
	var head [5]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	switch head[0] {
	case 'R':
		return nil
	case 'E':
		n := binary.BigEndian.Uint32(head[1:])
		if n < 4 || n > 1<<16 {
			return errors.New("postgres: bad error message")
		}
		body := make([]byte, n-4)
		if _, err := io.ReadFull(conn, body); err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		for _, field := range strings.Split(string(body), "\x00") {
			if field == "C57P03" {
				return errors.New("postgres: the database system is starting up")
			}
		}
		return nil
	default:
		return fmt.Errorf("postgres: unexpected message %q", head[0])
	}
}
