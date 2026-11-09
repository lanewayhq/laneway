package recipe_test

import (
	"reflect"
	"testing"

	"github.com/lanewayhq/laneway/internal/recipe"
)

func TestARailsAppUsingSidekiqOrActionCableOnRedisGetsRedisAtRedisURL(t *testing.T) {
	const sqlite = "development:\n  adapter: sqlite3\n  database: storage/development.sqlite3\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		redis bool
	}{
		{"Sidekiq", map[string]string{"Gemfile": railsGemfile + "gem \"sidekiq\", \"~> 8.0\"\n"}, true},
		{"Action Cable on Redis", map[string]string{"config/cable.yml": "development:\n  adapter: redis\n  url: <%= ENV.fetch(\"REDIS_URL\") { \"redis://localhost:6379/1\" } %>\n  channel_prefix: shop_development\n\ntest:\n  adapter: test\n"}, true},
		{"Action Cable in process", map[string]string{"config/cable.yml": "development:\n  adapter: async\n\nproduction:\n  adapter: redis\n"}, false},
		{"neither", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.files = mapWith(tc.files, "config/database.yml", sqlite)
			res := mustResolveDir(t, railsProject(t, tc.files))

			var want []recipe.Service
			if tc.redis {
				want = []recipe.Service{{Engine: "redis", Version: "7"}}
			}
			if !reflect.DeepEqual(res.Services, want) {
				t.Errorf("Services = %+v, want %+v", res.Services, want)
			}
			url, ok := res.Env["REDIS_URL"]
			switch {
			case tc.redis && url != "redis://:{redis_password}@{redis_host}:{redis_port}":
				t.Errorf("env.REDIS_URL = %q, want the Redis Service's URL", url)
			case !tc.redis && ok:
				t.Errorf("env.REDIS_URL = %q, want it left alone", url)
			}
		})
	}
}

// mapWith is m with key set to value.
func mapWith(m map[string]string, key, value string) map[string]string {
	out := map[string]string{key: value}
	for k, v := range m {
		out[k] = v
	}
	return out
}
