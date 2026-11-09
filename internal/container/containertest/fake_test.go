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
