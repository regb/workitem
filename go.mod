module github.com/regb/workitem

go 1.24.0

require go.etcd.io/bbolt v1.4.3

require golang.org/x/sys v0.29.0

require github.com/rivo/uniseg v0.4.7

require (
	connectrpc.com/connect v1.19.1
	github.com/pelletier/go-toml/v2 v2.2.3
	google.golang.org/protobuf v1.36.10
)

tool (
	connectrpc.com/connect/cmd/protoc-gen-connect-go
	google.golang.org/protobuf/cmd/protoc-gen-go
)
