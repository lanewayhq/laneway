# Contributing to Laneway

Thanks for helping. The full flow, for features, bug fixes, contributions and releases across this repository and [lanewayhq/plugins](https://github.com/lanewayhq/plugins), is in [docs/development-flow.md](docs/development-flow.md). The short version:

1. **Start with an issue**, unless the change is small and obvious. Pull requests aren't a feature-request channel: agree the approach in an issue first. Issues labelled `good first issue` or `help wanted` are open for anyone; say in the issue that you're taking it.
2. **Fork, and branch from `main`.**
3. **Write tests with the change.** A bug fix starts with a test that fails without it.
   ```sh
   go test ./...                                           # fast suite
   LANEWAY_SLOW=1 go test ./e2e -run RealContainerEngine   # if you touched container engines
   gofmt -w <your files>
   ```
   If you touched `internal/platform`, `lane install` or `lane doctor`, go through the [install checklist](docs/install-checklist.md) too.
4. **Follow the conventions:** commit messages like `fix: <what is now true, as a sentence>` (`feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`), and the words [GLOSSARY.md](GLOSSARY.md) defines. Leave `.github/`, `install.sh` and `internal/catalog/` alone unless the issue asks for a change there.
5. **Open the pull request** against `main`, with `Fixes #N` and how you tested it. The maintainer approves the first CI run from a fork and reviews every change.

A change to a first-party Plugin (Laravel, Rails, Vite, the Engines, the Generators) belongs in [lanewayhq/plugins](https://github.com/lanewayhq/plugins). Your own Plugin doesn't need to be in either repository: see the [Plugin authoring guide](https://github.com/lanewayhq/plugins/blob/main/docs/authoring.md).

**Security issues:** don't open a public issue. Contact the maintainer privately.

By contributing, you agree that your contribution is licensed under the [Apache License 2.0](LICENSE).
