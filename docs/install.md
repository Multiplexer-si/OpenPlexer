# Install OpenPlexer

Open the [releases page](https://github.com/Multiplexer-si/OpenPlexer/releases) and choose a version. Download `SHA256SUMS` and one archive into an empty directory. Match the suffix to your system. Each archive contains `openplexer`, `openplexer-hub`, and `openplexer-verify`.

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

Unpack the archive and install the three binaries in your home directory.

```sh
mkdir -p unpacked "$HOME/.local/bin"
tar -xzf "$archive" -C unpacked
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
