package containertest_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/container/containertest"
)

// A container's Server starts in the background as Start returns, so the
// first commands run in it race the Server, and each other, to set up what
// the image's initialisation makes. Several first commands in each of many
// fresh containers make that race likely.
const (
	freshContainers = 50
	firstCommands   = 4
)

// atOnce runs do(0) … do(n-1) at the same time and waits for them.
func atOnce(n int, do func(i int)) {
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { do(i) })
	}
	wg.Wait()
}

func TestAPostgresContainerKeepsWhatItsFirstCommandsMakeWhileItsServerStarts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for i := range freshContainers {
		f := containertest.New()
		name := fmt.Sprintf("pg-%d", i)
		if _, err := f.Start(ctx, container.Spec{Name: name, Image: "docker.io/library/postgres:18", Port: 5432,
			Env: []string{"POSTGRES_USER=laneway", "POSTGRES_PASSWORD=secret"}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Stop(ctx, name) })
		psql := func(sql string) string {
			out, err := f.Exec(ctx, name, []string{"psql", "-U", "laneway", "-d", "postgres", "-tA", "-c", sql})
			if err != nil || out.ExitCode != 0 {
				t.Errorf("container %d: psql -c %q: %v, exit %d: %s", i, sql, err, out.ExitCode, out.Stderr)
			}
			return string(out.Stdout)
		}
		want := []string{"postgres", "template0", "template1", "laneway"}
		atOnce(firstCommands, func(n int) { psql(fmt.Sprintf(`CREATE DATABASE "app%d"`, n)) })
		for n := range firstCommands {
			want = append(want, fmt.Sprintf("app%d", n))
		}
		if t.Failed() {
			t.FailNow()
		}
		for _, db := range want {
			if got := psql(fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s'", db)); got != "1\n" {
				t.Fatalf("container %d: the database %s is gone", i, db)
			}
		}
	}
}

func TestAMySQLContainerKeepsWhatItsFirstCommandsMakeWhileItsServerStarts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for i := range freshContainers {
		f := containertest.New()
		name := fmt.Sprintf("my-%d", i)
		if _, err := f.Start(ctx, container.Spec{Name: name, Image: "docker.io/library/mysql:8.4", Port: 3306,
			Env: []string{"MYSQL_USER=laneway", "MYSQL_PASSWORD=secret"}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Stop(ctx, name) })
		mysql := func(sql string) string {
			out, err := f.Exec(ctx, name, []string{"env", "MYSQL_PWD=secret", "mysql", "--user=laneway", "--execute=" + sql})
			if err != nil || out.ExitCode != 0 {
				t.Errorf("container %d: mysql --execute=%q: %v, exit %d: %s", i, sql, err, out.ExitCode, out.Stderr)
			}
			return string(out.Stdout)
		}
		want := []string{"information_schema", "mysql", "performance_schema", "sys"}
		atOnce(firstCommands, func(n int) { mysql(fmt.Sprintf("CREATE DATABASE `app%d`", n)) })
		for n := range firstCommands {
			want = append(want, fmt.Sprintf("app%d", n))
		}
		if t.Failed() {
			t.FailNow()
		}
		got := strings.Fields(mysql("SHOW DATABASES"))
		for _, db := range want {
			if !slices.Contains(got, db) {
				t.Fatalf("container %d: SHOW DATABASES gives %q, want %s among them", i, got, db)
			}
		}
	}
}

func TestAMariaDBContainerKeepsWhatItsFirstCommandsMakeWhileItsServerStarts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for i := range freshContainers {
		f := containertest.New()
		name := fmt.Sprintf("maria-%d", i)
		if _, err := f.Start(ctx, container.Spec{Name: name, Image: "docker.io/library/mariadb:11.8", Port: 3306,
			Env: []string{"MARIADB_USER=laneway", "MARIADB_PASSWORD=secret"}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Stop(ctx, name) })
		mariadb := func(sql string) string {
			out, err := f.Exec(ctx, name, []string{"env", "MYSQL_PWD=secret", "mariadb", "--user=laneway", "--execute=" + sql})
			if err != nil || out.ExitCode != 0 {
				t.Errorf("container %d: mariadb --execute=%q: %v, exit %d: %s", i, sql, err, out.ExitCode, out.Stderr)
			}
			return string(out.Stdout)
		}
		want := []string{"information_schema", "mysql", "performance_schema", "sys"}
		atOnce(firstCommands, func(n int) { mariadb(fmt.Sprintf("CREATE DATABASE `app%d`", n)) })
		for n := range firstCommands {
			want = append(want, fmt.Sprintf("app%d", n))
		}
		if t.Failed() {
			t.FailNow()
		}
		got := strings.Fields(mariadb("SHOW DATABASES"))
		for _, db := range want {
			if !slices.Contains(got, db) {
				t.Fatalf("container %d: SHOW DATABASES gives %q, want %s among them", i, got, db)
			}
		}
	}
}

// The clone script of a MariaDB manifest, as the mariadb image would run it.
func mariadbClone(shell, dumpFlags string) []string {
	script := `set -o pipefail
user=$1 from=$2 to=$3
mariadb-dump --user="$user" --single-transaction` + dumpFlags + ` "$from" | mariadb --user="$user" --database="$to"`
	return []string{"env", "MYSQL_PWD=secret", shell, "-c", script, shell, "laneway", "app", "copy"}
}

func TestAMariaDBContainerCopiesADatabaseOnlyWithAScriptItsImageCanRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := containertest.New()
	if _, err := f.Start(ctx, container.Spec{Name: "maria", Image: "docker.io/library/mariadb:11.8", Port: 3306,
		Env: []string{"MARIADB_USER=laneway", "MARIADB_PASSWORD=secret"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Stop(ctx, "maria") })
	if out, err := f.Exec(ctx, "maria", []string{"env", "MYSQL_PWD=secret", "mariadb", "--user=laneway", "--execute=CREATE DATABASE `app`"}); err != nil || out.ExitCode != 0 {
		t.Fatalf("creating app: %v, exit %d: %s", err, out.ExitCode, out.Stderr)
	}

	for _, c := range []struct {
		what string
		cmd  []string
	}{
		{"in sh, which is dash and has no pipefail", mariadbClone("sh", "")},
		{"with MySQL's --set-gtid-purged", mariadbClone("bash", " --set-gtid-purged=OFF")},
	} {
		if out, err := f.Exec(ctx, "maria", c.cmd); err != nil || out.ExitCode == 0 {
			t.Errorf("a clone script %s: %v, exit %d, want it to fail", c.what, err, out.ExitCode)
		}
	}
	if out, err := f.Exec(ctx, "maria", mariadbClone("bash", "")); err != nil || out.ExitCode != 0 {
		t.Fatalf("a clone script in bash: %v, exit %d: %s", err, out.ExitCode, out.Stderr)
	}
	out, _ := f.Exec(ctx, "maria", []string{"env", "MYSQL_PWD=secret", "mariadb", "--user=laneway", "--execute=SHOW DATABASES"})
	if !slices.Contains(strings.Fields(string(out.Stdout)), "copy") {
		t.Fatalf("SHOW DATABASES gives %q after the copy, want copy among them", out.Stdout)
	}
}
