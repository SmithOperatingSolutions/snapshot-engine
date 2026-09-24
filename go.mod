module github.com/SmithOperatingSolutions/snapshot-engine

go 1.27

require (
	github.com/SmithOperatingSolutions/snapshot-core v0.1.1
	pgregory.net/rapid v1.3.0
)

require (
	github.com/SmithOperatingSolutions/disknexus-engine v0.2.11 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

tool (
	github.com/SmithOperatingSolutions/snapshot-core/tools/ci
	github.com/SmithOperatingSolutions/snapshot-core/tools/mutate
	github.com/SmithOperatingSolutions/snapshot-core/tools/redcheck
)
