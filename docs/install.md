# Install OpenPlexer

Open the [releases page](https://github.com/Multiplexer-si/OpenPlexer/releases) and choose a version. Download `SHA256SUMS` and one archive into an empty directory. Match the suffix to your system. Each archive contains `openplexer`, `openplexer-hub`, `openplexer-verify`, `LICENSE`, `NOTICE`, and `README.md`. The Windows binaries have an `.exe` suffix.

| System | Archive suffix |
|---|---|
| Linux, x86-64 | `_linux_amd64.tar.gz` |
| Linux, ARM64 | `_linux_arm64.tar.gz` |
| macOS, Intel | `_darwin_amd64.tar.gz` |
| macOS, Apple silicon | `_darwin_arm64.tar.gz` |
| Windows, x86-64 | `_windows_amd64.zip` |

## Linux and macOS

Open a POSIX shell in the download directory. Check the checksum for the archive you downloaded.

```sh
set -- openplexer_*.tar.gz
archive=$1
if [ "$(uname -s)" = Darwin ]; then
	awk -v name="$archive" '$2 == name' SHA256SUMS | shasum -a 256 -c
else
	awk -v name="$archive" '$2 == name' SHA256SUMS | sha256sum -c
fi
```

Continue after the checksum command prints `OK`. You can also [verify the build attestation](#verify-the-build-attestation).

Unpack the archive.

```sh
mkdir -p unpacked
tar -xzf "$archive" -C unpacked
```

Install the three binaries in your home directory.

```sh
mkdir -p "$HOME/.local/bin"
install -m 0755 unpacked/openplexer unpacked/openplexer-hub unpacked/openplexer-verify "$HOME/.local/bin/"
export PATH="$HOME/.local/bin:$PATH"
openplexer --version
openplexer-hub --version
openplexer-verify --version
```

Each command prints the release tag. Add the `export PATH` line to your shell profile to use the binaries in new shells.

## Windows

Open PowerShell in the download directory. Check the ZIP against `SHA256SUMS`.

```powershell
$archive = (Get-Item .\openplexer_*.zip -ErrorAction Stop).Name
$expected = Get-Content .\SHA256SUMS | ForEach-Object {
	$hash, $name = $_ -split '  ', 2
	if ($name -ceq $archive) { $hash }
}
if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $expected) {
	throw "SHA-256 check failed for $archive"
}
```

Continue if the check succeeds. You can also [verify the build attestation](#verify-the-build-attestation).

```powershell
$bin = Join-Path $HOME '.local\bin'
Expand-Archive -LiteralPath $archive -DestinationPath .\unpacked
New-Item -ItemType Directory -Path $bin -Force | Out-Null
Copy-Item .\unpacked\openplexer*.exe -Destination $bin -Force
$env:Path = "$bin;$env:Path"
openplexer.exe --version
openplexer-hub.exe --version
openplexer-verify.exe --version
```

Each command prints the release tag. Add the directory in `$bin` to your user `Path` in Windows to use the binaries in new shells.

## Verify the build attestation

For an additional check before unpacking, sign in to the GitHub CLI with `gh auth login`. Run this command in the download directory with the `archive` variable you set above. It works in a shell or in PowerShell. [GitHub CLI attestation reference](https://cli.github.com/manual/gh_attestation_verify)

```sh
gh attestation verify "$archive" --repo Multiplexer-si/OpenPlexer --signer-workflow Multiplexer-si/OpenPlexer/.github/workflows/release.yml
```

## Set up a hub or hook

- Run a hub for your team. [Self-host the OpenPlexer hub](self-host.md)
- Connect a coding agent. [Claude Code hooks](hooks-claude-code.md) or [Codex hooks](hooks-codex.md)

## Run the daemon as a login service

The `service` command needs a build newer than `v0.1.0-rc.1`, so build from `main` with `make build` until the next release.
Install the resulting `bin/openplexer` in the location where you plan to keep it.

From your macOS or Linux login session, run these commands in the checkout that uses the configured home. Add `--home <dir>` after the subcommand to choose a specific home. Stop any foreground daemon for that home before you install the service.

```sh
openplexer service install
openplexer service status
```

Status shows the service state, local policy state, and log location. The service logs operational errors. To remove the service, run `openplexer service uninstall` for the same home.

On macOS, the logs are `logs/daemon.stdout.log` and `logs/daemon.stderr.log` under the OpenPlexer home. To rotate them, uninstall the service, rename the files, and install the service again.

On Linux, run the `journalctl --user -u` command printed by status. The journal handles rotation.
