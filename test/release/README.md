# Release tests

Run `go test -count=1 ./test/release` from the repository root. On Linux or macOS the install test builds the native archive twice, compares both builds, and runs the install guide commands in a temporary home.

Set `OPENPLEXER_BIN_DIR` to the absolute path of an unpacked archive containing all three binaries. The install test runs the guide's commands for installing the unpacked binaries and checks that they report the same version. Check the original archive's checksum before unpacking it. [Install OpenPlexer](../../docs/install.md)
