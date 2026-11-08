package service

import (
	"context"
	"fmt"
	"io"
	"net"
)

// MySQL is the MySQL engine. Its superuser is the shared User, with the
// shared Password: the image creates User (MYSQL_USER) with rights to no
// database, so the container's command adds a script that the image runs
// once, after creating the user on the first start, granting it everything.
// root has the shared Password too.
var MySQL = &Engine{
	Name:           "mysql",
	DefaultVersion: "8.4",
	Port:           3306,
	Image:          "docker.io/library/mysql",
	DataDir:        "/var/lib/mysql",
	User:           User,
	Env: func(string) []string {
		return []string{"MYSQL_ROOT_PASSWORD=" + Password, "MYSQL_USER=" + User, "MYSQL_PASSWORD=" + Password}
	},
	// The image's entrypoint runs a command that isn't mysqld as is; this
	// one writes the script, then runs the entrypoint with mysqld.
	Cmd: func(string) []string {
		return []string{"sh", "-c", `printf '%s\n' "GRANT ALL PRIVILEGES ON *.* TO '` + User +
			`'@'%' WITH GRANT OPTION;" > /docker-entrypoint-initdb.d/laneway.sql && exec docker-entrypoint.sh mysqld`}
	},
	Probe: probeMySQL,
}

// probeMySQL reads the server's first packet: a protocol 10 greeting, or an
// error (such as too many connections), means mysqld itself answered. The
// image's first start initialises the data directory with networking off,
// so TCP connections fail until the real server listens.
func probeMySQL(ctx context.Context, conn net.Conn) error {
	var head [5]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fmt.Errorf("mysql: %w", err)
	}
	if head[4] != 0x0a && head[4] != 0xff {
		return fmt.Errorf("mysql: unexpected first packet 0x%02x", head[4])
	}
	return nil
}
