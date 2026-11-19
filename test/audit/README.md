# Audit integration tests

Run `go test -count=1 ./test/audit` from the repository root. The tests build the hub and verifier, export an audit log, and check the guide examples, reference chain fixtures, and tamper cases. They use temporary directories and need no running hub.

Set `OPENPLEXER_BIN_DIR` to the absolute path of a directory containing `openplexer-hub` and `openplexer-verify` to test prebuilt binaries.

Run `go test -count=1 ./apps/openplexer-verify -run '^TestRunVerifiesCheckpointVectors$'` with the same setting to check every checkpoint fixture in text and JSON modes.
