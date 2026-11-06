# Generate the daemon API

The source contract is in `proto/wi/core/v1/`. Generated protobuf messages and
Connect clients and handler interfaces are checked in under `gen/wi/core/v1/`.
Do not edit generated files.

From the repository root, run:

```sh
nix develop
scripts/generate-api
```

The development shell supplies Buf. `buf.gen.yaml` invokes `go tool` for
`protoc-gen-go` and `protoc-gen-connect-go`, whose versions are pinned in
`go.mod`. No global Go plugin installation or remote generation service is
needed. The normal application build uses the checked-in output and does not
run generation.

Outside Nix, install Buf 1.57.2 or run the equivalent commands:

```sh
go run github.com/bufbuild/buf/cmd/buf@v1.57.2 lint
go run github.com/bufbuild/buf/cmd/buf@v1.57.2 generate
```

To check reproducibility, regenerate and check that `git diff --exit-code --
gen/` succeeds. To compare a proposed contract change with a prior commit:

```sh
buf breaking --against '.git#ref=HEAD'
```

The API design and current transport, job, confirmation and read guarantees
are documented in `docs/design/api/README.md`. Run Buf from the repository root,
not from the design directory. After a contract edit, regenerate both messages
and Connect bindings, then run the Go, API race, web and real-binary tests.
