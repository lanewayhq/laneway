# Laneway resolves Runtimes but never installs them

Laneway does not install or manage language Runtimes. It resolves each Runtime through mise first and falls back to whatever is on `PATH` (e.g. PHP from distro packages or the ondrej PPA). The Project's own files are the single source of truth: version files (`.nvmrc`, `.python-version`, `.tool-versions`, `mise.toml`) say which version to use, and dependency manifests (`composer.json` `require.php` / `ext-*`, `package.json` `engines`) say what's acceptable. Laneway config never declares Runtime versions.

Owning Runtimes for every language would mean rebuilding mise, asdf or nix. The PATH fallback exists because mise compiles PHP from source, which is slow and makes extensions awkward to add; insisting on mise alone would make Laravel's first run worse than Herd's. Prebuilt PHP downloads (Herd's approach) were rejected because they would make Laneway a Runtime distributor.

Instead of a "config vs version file" mismatch warning, Laneway checks the resolved Runtime against the Project's declared constraints and required extensions. It reports problems in `lane doctor` and when a Worktree starts.
