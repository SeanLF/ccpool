package store

// Regenerate the typed query layer in internal/store/db after editing schema.sql or query.sql.
// sqlc is pinned to the spike-tested version and installed standalone (kept out of go.mod's tool
// block so the module stays near-stdlib; sqlc drags in a large dep tree only needed at codegen time):
//
//	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
//	go generate ./internal/store/
//
//go:generate sqlc generate
