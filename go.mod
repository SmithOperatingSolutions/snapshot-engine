module github.com/SmithOperatingSolutions/snapshot-engine

go 1.27

require github.com/SmithOperatingSolutions/snapshot-core v0.1.0

tool (
	github.com/SmithOperatingSolutions/snapshot-core/tools/mutate
	github.com/SmithOperatingSolutions/snapshot-core/tools/redcheck
)
