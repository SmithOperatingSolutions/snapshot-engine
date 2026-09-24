// Package engine is the Versioned DB's one API: a database opened on a branch
// of a snapshot-core repository, sessions carrying a principal, transactions
// with snapshot isolation and optimistic commit through the models' merge,
// and the version operations (commit, branch, merge, diff, log) an access
// layer needs, reached here and never by importing the core
// (docs/specs/engine-layers.md; Engine Spec L4's API sketch, DESIGN D1).
//
// Adapters (a wire protocol and a query language each, in their own
// repositories) import this package alone (DESIGN D6): what they need of the
// core is re-exported here.
package engine

import "github.com/SmithOperatingSolutions/snapshot-core/core/auth"

// Principal is who a session acts as: the core's principal, checked against
// the repository's authorizer on every call. An adapter builds one from its
// protocol's authentication and never reaches into the core for it.
type Principal = auth.Principal
