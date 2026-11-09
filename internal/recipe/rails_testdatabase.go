package recipe

import "github.com/lanewayhq/laneway/internal/service"

var _ TestEnvRecipe = Rails{}

// TestEnv is .env.test: dotenv-rails, the usual way a Rails app reads .env,
// reads it over .env when the tests run. A .env.test is also what keeps the
// tests off the development database, which .env's DATABASE_URL names: Rails
// applies DATABASE_URL in whichever environment runs.
func (Rails) TestEnv() TestEnv { return TestEnv{File: ".env.test", Layered: true} }

var _ TestDatabaseRecipe = Rails{}

// AutoTestDatabase decides database.test = auto for a Rails Project: its
// tests use a database of their own when the test section of
// config/database.yml (its primary database, with several) uses an adapter
// whose database Laneway keeps in a Service. Tests on SQLite already get a
// file per Worktree.
func (Rails) AutoTestDatabase(dir string) bool {
	db, err := railsYmlDatabase(dir, "test")
	if err != nil {
		return false
	}
	engine, ok := railsAdapters[db.adapter]
	if !ok {
		return false
	}
	_, inService := service.Lookup(engine)
	return inService
}

var _ TestDatabaseWarner = Rails{}

// TestDatabaseWarnings warns when the Project's Gemfile has no gem that
// loads .env files: Rails doesn't read them itself, so its tests never see
// .env.test, where Laneway points them at the Worktree's Test Database.
// (Laneway's own Processes read .env through their command; the tests are
// run by the developer.)
func (Rails) TestDatabaseWarnings(dir string) []string {
	if gemfileDeclares(dir, "dotenv-rails", "dotenv") {
		return nil
	}
	return []string{"the Gemfile doesn't load .env files (it has neither dotenv-rails nor dotenv), so the Project's tests " +
		"don't read .env.test, where Laneway points them at the Worktree's Test Database; add gem \"dotenv-rails\" " +
		"to the Gemfile's :development, :test group, or set database.test to off"}
}
