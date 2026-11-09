// Command rails is the rails Plugin, built into Laneway: the Rails Recipe,
// for a Project whose Gemfile declares the rails gem and that has a Rails
// app's config/application.rb. Its answer comes from the Project's files:
// the database from DATABASE_URL in .env or config/database.yml, Processes
// from Procfile.dev, Redis from the Gemfile and config/cable.yml, and the
// Test Database from database.yml's test section.
package main

import "github.com/lanewayhq/laneway/sdk"

func main() {
	sdk.Run(sdk.Plugin{Recipes: map[string]sdk.Recipe{"rails": recipe}})
}

// withEnv loads the Worktree's .env into the environment of the command
// after it. Rails doesn't read .env itself (only through the dotenv gem, which
// many apps don't use), so this is how the Env Patch, DATABASE_URL among it,
// reaches the app. Laneway writes its keys in a form sh reads back exactly.
const withEnv = "set -a; [ ! -f .env ] || . ./.env; set +a; "

// railsServer serves the app with Rails' own server, through the app's
// binstub, on the port Laneway gives the Primary Process.
const railsServer = withEnv + "exec bin/rails server -b 127.0.0.1 -p {port}"

func recipe(p sdk.Project) (sdk.RecipeResult, error) {
	l := sdk.Layer{Processes: map[string]sdk.Process{}}
	procfileProcesses(&l, p)
	l.Processes["web"] = sdk.Process{Command: railsServer, Primary: true}
	l.Env = map[string]string{}
	var res sdk.RecipeResult
	res.Warnings = database(&l, p)
	redis(&l, p)
	// A new Worktree has no gems installed yet. Laneway makes its database
	// itself, so there is no db:prepare.
	l.Worktree = &sdk.Worktree{OnCreate: []string{"bundle install"}}
	res.Layer = l
	res.TestDatabase = autoTestDatabase(p)
	res.TestDatabaseWarnings = testDatabaseWarnings(p)
	res.TestEnv = &testEnv
	return res, nil
}
