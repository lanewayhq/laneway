package dotenv_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/dotenv"
)

const laravelEnv = `# Laravel env
APP_NAME="My App"
APP_URL=http://localhost   # set by hand

export DB_DATABASE=laravel
DB_PASSWORD=
PRIVATE_KEY="-----BEGIN-----
abc
-----END-----"
this line isn't valid dotenv
# keep me
DB_DATABASE=dup_later_wins
`

func TestAFileWithoutEditsIsWrittenBackByteForByte(t *testing.T) {
	for name, src := range map[string]string{
		"laravel":             laravelEnv,
		"empty":               "",
		"no trailing newline": "A=1\nB=2",
		"blank lines only":    "\n\n",
		"crlf":                "A=1\r\n# c\r\nB=\"x\r\ny\"\r\n",
		"unterminated quote":  "A=\"never closed\nB=2\n",
		"spaces around =":     "  A = 1  # note\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(dotenv.Parse([]byte(src)).Bytes()); got != src {
				t.Errorf("round trip changed the file:\n got %q\nwant %q", got, src)
			}
		})
	}
}

func TestGetReadsTheLastOccurrenceDecoded(t *testing.T) {
	f := dotenv.Parse([]byte(laravelEnv))
	tests := map[string]string{
		"APP_NAME":    "My App",
		"APP_URL":     "http://localhost",
		"DB_DATABASE": "dup_later_wins",
		"DB_PASSWORD": "",
		"PRIVATE_KEY": "-----BEGIN-----\nabc\n-----END-----",
	}
	for key, want := range tests {
		if got, ok := f.Get(key); !ok || got != want {
			t.Errorf("Get(%s) = %q, %t; want %q", key, got, ok, want)
		}
	}
	if _, ok := f.Get("MISSING"); ok {
		t.Error("Get(MISSING) found a value")
	}
}

func TestSetChangesOnlyThatKeysValueEverywhereItAppears(t *testing.T) {
	f := dotenv.Parse([]byte(laravelEnv))
	f.Set("APP_URL", "https://shop.localhost")
	f.Set("DB_DATABASE", "shop__feat")

	want := `# Laravel env
APP_NAME="My App"
APP_URL=https://shop.localhost   # set by hand

export DB_DATABASE=shop__feat
DB_PASSWORD=
PRIVATE_KEY="-----BEGIN-----
abc
-----END-----"
this line isn't valid dotenv
# keep me
DB_DATABASE=shop__feat
`
	if got := string(f.Bytes()); got != want {
		t.Errorf("after Set:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetAppendsAMissingKeyAtTheEnd(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"trailing newline", "A=1\n", "A=1\nNEW=x\n"},
		{"no trailing newline", "A=1", "A=1\nNEW=x"},
		{"empty file", "", "NEW=x\n"},
		{"crlf", "A=1\r\n", "A=1\r\nNEW=x\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := dotenv.Parse([]byte(tt.src))
			f.Set("NEW", "x")
			if got := string(f.Bytes()); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetKeepsCRLFLineEndings(t *testing.T) {
	f := dotenv.Parse([]byte("A=1\r\nB=2 # two\r\n"))
	f.Set("B", "3")
	if got, want := string(f.Bytes()), "A=1\r\nB=3 # two\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The expected forms are the ones phpdotenv v5.8 (Laravel's parser) was
// verified to read back exactly; see TestPhpdotenvReadsBackWhatSetWrote.
func TestQuoteWritesTheFormPhpdotenvReadsBackExactly(t *testing.T) {
	tests := map[string]string{
		"https://feat.shop.localhost": `https://feat.shop.localhost`,
		"shop__feat":                  `shop__feat`,
		"":                            ``,
		"a #b":                        `'a #b'`,
		"Laneway ${APP_NAME}":         `'Laneway ${APP_NAME}'`,
		`p@ss w0rd "quoted" $HOME ${APP_NAME} it's \ back#slash`: `"p@ss w0rd \"quoted\" \$HOME \${APP_NAME} it's \\ back#slash"`,
		"two\nlines": "\"two\nlines\"",
	}
	for value, want := range tests {
		if got := dotenv.Quote(value); got != want {
			t.Errorf("Quote(%q) = %s, want %s", value, got, want)
		}
		f := dotenv.Parse([]byte("K=" + want + "\n"))
		if got, _ := f.Get("K"); got != value {
			t.Errorf("Get of K=%s = %q, want %q", want, got, value)
		}
	}
}

func TestSetThenParseReadsTheValueBack(t *testing.T) {
	f := dotenv.Parse([]byte(laravelEnv))
	f.Set("APP_URL", "it's a \"url\" with $vars")
	f.Set("NEW", "multi\nline")
	again := dotenv.Parse(f.Bytes())
	if got, _ := again.Get("APP_URL"); got != "it's a \"url\" with $vars" {
		t.Errorf("APP_URL = %q", got)
	}
	if got, _ := again.Get("NEW"); got != "multi\nline" {
		t.Errorf("NEW = %q", got)
	}
	if got, _ := again.Get("PRIVATE_KEY"); got != "-----BEGIN-----\nabc\n-----END-----" {
		t.Errorf("PRIVATE_KEY = %q", got)
	}
}

// TestPhpdotenvReadsBackWhatSetWrote asks the real phpdotenv, configured the
// way Laravel configures it, to read an edited file. It needs PHP and a
// script that prints a .env file as JSON (parse.php in the Laneway research
// notes), so it only runs with LANEWAY_PHPDOTENV=<path to parse.php> and php
// (or $LANEWAY_PHP) available.
func TestPhpdotenvReadsBackWhatSetWrote(t *testing.T) {
	script := os.Getenv("LANEWAY_PHPDOTENV")
	if script == "" {
		t.Skip("set LANEWAY_PHPDOTENV to a phpdotenv parse.php to run this")
	}
	php := os.Getenv("LANEWAY_PHP")
	if php == "" {
		php = "php"
	}
	// phpdotenv refuses the whole file over a line it doesn't understand.
	base := `# Laravel env
APP_NAME="My App"
APP_URL=http://localhost   # set by hand

export DB_DATABASE=laravel
DB_PASSWORD=
PRIVATE_KEY="-----BEGIN-----
abc
-----END-----"
DB_DATABASE=dup_later_wins
`
	values := map[string]string{
		"APP_URL":        "https://feat.shop.localhost",
		"DB_DATABASE":    "shop__feat",
		"DB_PASSWORD":    `p@ss w0rd "quoted" $HOME ${APP_NAME} it's \ back#slash`,
		"REDIS_PREFIX":   "shop_feat_",
		"MAIL_FROM_NAME": "Laneway ${APP_NAME}",
		"NEW_EMPTY":      "",
		"NEW_HASH":       "a #b",
		"NEW_MULTILINE":  "two\nlines",
	}
	f := dotenv.Parse([]byte(base))
	for k, v := range values {
		f.Set(k, v)
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, f.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(php, script, path)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")} // real env vars would beat .env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", php, script, err, out)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	values["APP_NAME"] = "My App"
	values["PRIVATE_KEY"] = "-----BEGIN-----\nabc\n-----END-----"
	for k, want := range values {
		if got[k] != want {
			t.Errorf("phpdotenv read %s = %q, want %q", k, got[k], want)
		}
	}
}
