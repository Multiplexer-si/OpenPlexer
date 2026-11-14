# Contributing

This guide covers the tasks that a contributor does in this repository. [Architecture](docs/architecture.md) explains why the code is split the way it is, and [Repository layout](docs/repository-layout.md) lists every directory.

## Set up

1. Install [Go](https://go.dev/doc/install) 1.27 or later.
2. Clone the repository.

	```sh
	git clone https://github.com/Multiplexer-si/OpenPlexer.git
	cd OpenPlexer
	```

3. Run the checks.

	```sh
	make check
	```

	The last lines of the output list each package. The packages `internal/layering`, `internal/doclinks`, and `internal/spectest` report `ok`.

The first run downloads the modules that the tests in `internal/spectest` use. The binaries use only the standard library.

## Run the checks

Run `make check` before you open a pull request. CI runs the same command.

`make check` does three things in order.

1. It fails if `gofmt -l .` lists a file. To format every file, run `make fmt`.
2. It runs `go vet ./...`.
3. It runs `go test ./...`. The tests include the dependency rules, the documentation links, and the checks of the schemas and examples under `spec/`.

## Build the binaries

```sh
make build
```

The command writes `openplexer`, `openplexer-hub`, and `openplexer-verify` to `bin/`. Git ignores `bin/`.

## Add an adapter for a coding agent

Each coding agent has its own package under `internal/adapter/`.

1. Create the directory `internal/adapter/<agent>`. Use a lowercase name with no separators, such as `claudecode`.
2. Add a `doc.go` file with a package comment that names the coding agent.

	```go
	// Package cursor owns the Cursor hook adapter.
	package cursor
	```

3. Run `make check`.

The dependency rules cover the new package without a change to the test. An adapter must not import `internal/daemon` or `internal/hub`.

## Add an internal package

1. Create the directory under `internal/` and add a `doc.go` file with a package comment that says what the package owns.
2. Decide which packages the new package must not depend on. Add a rule to the `rules` table in `internal/layering/layering_test.go`.
3. Copy the rule into the table in [Repository layout](docs/repository-layout.md#dependency-rules), and add the package to the package table on the same page.
4. Run `make check`.

The verifier cannot import the new package. To allow it, add the package to the `except` list of the `apps/openplexer-verify` rule. Do that only if the verifier needs the package to check an audit log.

## Change a dependency rule

The `rules` table in `internal/layering/layering_test.go` is the authority.

1. Change the rule in the `rules` table.
2. Change the copy of the rule in [Repository layout](docs/repository-layout.md#dependency-rules).
3. If the change removes a protection that [Architecture](docs/architecture.md) describes, change that page too.
4. Run `make check`.

## Fix a dependency rule failure

A failure names the package and the dependency.

```
internal/policy must not depend on internal/hub
```

The dependency can be indirect. In this example `internal/policy` imports a package that imports `internal/hub`. Remove the import, or move the shared code into a package that both sides may import. Do not weaken the rule to make the test pass.

## Regenerate the contracts

The extractor in `tools/extract` writes every file under `spec/` except the README pages. Do not edit those files by hand. Change the extractor, and run it.

The extractor runs the code of the existing TypeScript implementation, so you need these things.

- A checkout of the existing TypeScript implementation at commit `7d80045a`, with its dependencies installed and its packages built.
- Node 22 or later.

The extractor has no dependencies of its own. It uses the `tsx` of the checkout, and it never writes in the checkout. Without a checkout you can still run `make check`, which validates the committed files against each other.

1. Set `REFERENCE` to the path of the checkout.

	```sh
	REFERENCE=/path/to/the/checkout
	```

2. Check that the committed files match the reference. Run every command from the root of this repository.

	```sh
	"$REFERENCE/node_modules/.bin/tsx" tools/extract/main.ts check --reference "$REFERENCE"
	```

	The command recomputes every file in memory and prints each difference. It exits with status 0 if there is none, and with status 1 if there is one.

3. Change the extractor. The schemas are in `tools/extract/schemas.ts`, and the examples are in `tools/extract/examples.ts`.
4. Write the files.

	```sh
	"$REFERENCE/node_modules/.bin/tsx" tools/extract/main.ts generate --reference "$REFERENCE"
	```

5. Read the diff of `spec/`.
6. Run the Go checks.

	```sh
	make check
	```

7. Commit the change to the extractor and the change to `spec/` together.

CI runs `make check` only. It has no checkout of the reference, so it cannot run the extractor. Run the `check` command before you open a pull request that changes `tools/extract` or `spec/`.

### Make new signatures

`spec/policy/v1/examples/signed-bundle/message.json` and `spec/policy/v1/examples/jwks/hub-keys.json` hold a signature and the public key that verifies it. An ES256 signature is different on every run, and the extractor discards the private key, so `generate` does not write these files again. `check` verifies the signature in them with the verifier of the reference.

To change the signed example, pass `--new-signatures`. The extractor makes a new key and writes both files.

```sh
"$REFERENCE/node_modules/.bin/tsx" tools/extract/main.ts generate --reference "$REFERENCE" --new-signatures
```

### Move to a newer commit of the reference

1. Change `REFERENCE_COMMIT` in `tools/extract/reference.ts`, the commit in the README pages under `spec/`, and the commit in this page.
2. Run `generate`.
3. Read the diff of `spec/`. A change to a schema changes the contract. [Versions and compatibility](spec/README.md#versions-and-compatibility) says whether the change fits in version 1.

## Write documentation

Put a page under `docs/` and link it from the table in the [README](README.md).

Give each page one job. A page explains an idea, or lists facts to look up, or gives the steps of a task. A page that tries to do two of these is harder to use for both.

Use each term with the meaning that the [Glossary](docs/glossary.md) gives it. Add a new term to the glossary when you introduce it.

Describe the design in the present tense, and start a page about behavior that does not exist yet with a note that says so. Do not describe planned behavior as if a reader can run it today.

The test in `internal/doclinks` fails when a relative link in a Markdown file points to a file that does not exist. The test does not check links to other websites or the part of a link after `#`.

## Open a pull request

Open the pull request against `main`. CI runs `make check` and `make build` on every pull request.
