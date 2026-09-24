// Package merge is the three-way merge of typed values every data model of
// the engine shares (docs/specs/engine-layers.md, "The merge library";
// DESIGN D5). Given a base and the two sides that grew from it, each function
// returns the merged value or says, by path, where the two sides cannot be
// combined. Nothing here repairs a conflict, and nothing here knows the
// storage core: the models turn a Conflict into a model.Conflict at their own
// address.
//
// The policies and their promises:
//
//   - Scalar: equal is clean; a side that changed wins over one that did
//     not; both changed to different values is a conflict. Symmetric.
//   - Counter: the merged value is the base plus both sides' deltas. Always
//     clean. Symmetric.
//   - Set: members carry the tag of the write that added them; a member is
//     present after the merge when both sides have it, or one side added it.
//     One side removing what the other kept drops it; one side removing what
//     the other added again, under a new tag, keeps it (observed remove).
//     Always clean. Symmetric.
//   - Sequence: an ordered list merged by element identity (a key function)
//     and position, as diff3 does; changes to different stretches both land;
//     both sides inserting at one position keep both blocks, in the order of
//     their keys, and the position is flagged; both sides changing one
//     stretch differently is a conflict. Symmetric.
//   - Tree: a JSON-like value merged by path: fields changed on different
//     paths both land; one field changed on both sides is a Scalar merge (or a
//     Counter merge where the options say so) at that path; a field deleted
//     on one side and changed on the other is a conflict; arrays merge as
//     Sequences of their elements. Symmetric.
//
// Every policy is deterministic and returns a side unchanged when the other
// side made no change. Results whose Conflicts are empty are valid merges;
// Flagged names places merged by rule that a person may want to look at.
package merge
