package recipe

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lanewayhq/laneway/internal/gitrepo"
)

// testEnvFile is the env file Laravel loads instead of .env when APP_ENV is
// testing, which `php artisan test` and phpunit.xml set.
const testEnvFile = ".env.testing"

var _ TestDatabaseWarner = Laravel{}

// AutoTestDatabase decides database.test = auto for a Laravel Project, from
// its files only: its tests use a database of their own when it has a
// .env.testing or its PHPUnit configuration sets DB_CONNECTION to a
// connection other than SQLite. A linked Worktree without a .env.testing
// counts the main Worktree's, which setup copies into it (it is often not
// committed).
func (Laravel) AutoTestDatabase(dir string) bool {
	if exists(dir, testEnvFile) {
		return true
	}
	if main, ok := gitrepo.MainDir(dir); ok && exists(main, testEnvFile) {
		return true
	}
	connection, ok := readPHPUnit(dir).setting("DB_CONNECTION")
	connection = strings.TrimSpace(connection)
	return ok && connection != "" && connection != "sqlite"
}

// TestDatabaseWarnings warns when the Project's PHPUnit configuration sets
// DB_DATABASE: it wins over .env.testing, which Laneway points at the
// Worktree's Test Database, and Laneway never edits it.
func (Laravel) TestDatabaseWarnings(dir string) []string {
	p := readPHPUnit(dir)
	database, ok := p.setting("DB_DATABASE")
	if !ok {
		return nil
	}
	return []string{fmt.Sprintf("%s sets DB_DATABASE to %q, so the Project's tests use that database "+
		"instead of the Worktree's Test Database; remove DB_DATABASE from %s and let %s set it "+
		"(Laneway patches %s, but never edits %s)", p.file, database, p.file, testEnvFile, testEnvFile, p.file)}
}

// phpunit is what the Recipe reads of a Project's PHPUnit configuration: the
// variables its <php> element gives the tests through <env> and <server>
// (Laravel's env() reads both).
type phpunit struct {
	// file is the configuration file, relative to the Project; "" if it has
	// none (or none PHPUnit could read).
	file string
	vars []phpunitVar
}

type phpunitVar struct {
	XMLName xml.Name
	Name    string `xml:"name,attr"`
	Value   string `xml:"value,attr"`
}

// readPHPUnit reads the configuration PHPUnit uses when it isn't given one:
// the first of phpunit.xml and phpunit.xml.dist the Project has.
func readPHPUnit(dir string) phpunit {
	for _, name := range []string{"phpunit.xml", "phpunit.xml.dist"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var doc struct {
			PHP struct {
				Vars []phpunitVar `xml:",any"`
			} `xml:"php"`
		}
		if xml.Unmarshal(data, &doc) != nil {
			return phpunit{}
		}
		p := phpunit{file: name}
		for _, v := range doc.PHP.Vars {
			if v.XMLName.Local == "env" || v.XMLName.Local == "server" {
				p.vars = append(p.vars, v)
			}
		}
		return p
	}
	return phpunit{}
}

// setting is the value the configuration gives the environment variable
// name, and whether it gives it one.
func (p phpunit) setting(name string) (string, bool) {
	for _, v := range p.vars {
		if v.Name == name {
			return v.Value, true
		}
	}
	return "", false
}
