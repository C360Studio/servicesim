package scenario

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// maxResolvedNodes bounds the total number of nodes resolveAliases will visit
// or clone across one call, including every node produced by expanding a
// fanned-out alias more than once.
//
// yaml.v3 refuses to decode a document whose plain alias expansion balloons
// ("document contains excessive aliasing"), but that guard runs on the
// original document, before this file ever touches it — resolveAliases
// deep-copies the providers subtree ahead of any decoder seeing it, so it
// needs its own budget rather than relying on yaml.v3's. The trust boundary
// here is fixture authors, not untrusted input, so the budget is generous:
// it exists to turn a pathological fixture (nested anchors of large fan-out,
// each aliasing the last) into a load-time error instead of a multi-second
// hang, not to constrain realistic scenarios, which use at most one alias per
// anchor.
const maxResolvedNodes = 1_000_000

// resolveAliases returns node with every yaml.AliasNode in its tree — root
// included — replaced by a deep, alias-free copy of its Alias target, or an
// error if the tree contains a self-referencing anchor or expands past
// maxResolvedNodes.
//
// It exists because Providers.UnmarshalYAML retains every provider block as a
// raw *yaml.Node instead of decoding it immediately: decodeProviderEntry,
// decodeTurns, normalizeRespond, and the auth/validation/create/turn_key
// sub-decodes all walk nodes retained this way, because a level-1 scenario
// package cannot know what an Exa or Tavily projection looks like without
// importing it and creating a cycle. This function is called once, on the
// whole providers subtree, at the single point every one of those retained
// nodes passes through — so a future retention site inherits alias support
// with no separate change, and neither of the two problems below can resurface
// without a bug in this function itself:
//
//   - DecodeStrict re-marshals ONE node in isolation before strict-decoding it
//     (see its own doc comment: "the node is re-encoded and fed to a strict
//     decoder"). An anchor declared on a sibling turn is absent from that one
//     node's re-marshalled bytes, so yaml.Marshal reports "unknown anchor" for
//     an anchor that was perfectly valid in the source document.
//   - A bare Kind == yaml.MappingNode check — Turn.Respond's own in
//     validate.go, and scenarios_test.go's — never matches an AliasNode, even
//     when its target is a mapping, so a well-formed aliased respond reads as
//     "not a mapping".
//
// The copy's root Line and Column are set to the ALIAS node's position, not
// the anchor's, so a load-time finding raised against the turn that used the
// alias points at that turn rather than at the anchor's definition — which
// may be many lines earlier, in a different provider block, or outside
// providers entirely (in sources:). The anchor definition still gets its own,
// independent finding at its own position if its content is itself invalid;
// aliasing does not suppress that.
func resolveAliases(node *yaml.Node) (*yaml.Node, error) {
	r := &aliasResolver{active: make(map[*yaml.Node]bool)}
	return r.resolve(node)
}

// aliasResolver carries the two pieces of state a resolveAliases call needs
// across its whole recursion: active is the set of anchor targets currently
// being resolved on the CURRENT path (for cycle detection — the same anchor
// aliased twice from two different, non-overlapping branches is fine and not
// tracked as a conflict), and budget is a running count of every node visited
// or cloned across the WHOLE call (for the fan-out bound, which is global
// rather than per-path because the cost that matters is total work done).
type aliasResolver struct {
	active map[*yaml.Node]bool
	budget int
}

// resolve walks node, resolving any AliasNode it finds (including node
// itself) into a deep, alias-free, anchor-free copy of its target.
func (r *aliasResolver) resolve(node *yaml.Node) (*yaml.Node, error) {
	if node == nil {
		return node, nil
	}
	r.budget++
	if r.budget > maxResolvedNodes {
		return nil, fmt.Errorf("line %d: alias expansion exceeded %d nodes", node.Line, maxResolvedNodes)
	}
	if node.Kind == yaml.AliasNode {
		if node.Alias == nil {
			// Unreachable from a real yaml.v3 parse: an alias with no resolvable
			// anchor is a parse-time error ("unknown anchor"), never a nil Alias
			// on a node that reached here. Handled anyway because nothing about
			// this function should be the thing that panics on a malformed tree.
			return node, nil
		}
		// yaml.v3 v3.0.1 does NOT reject a self-referencing anchor at parse time
		// — confirmed in TestResolveAliases_CyclicAliasDoesNotHang, which parses
		// "a: &x\n  b: *x\n" with no error into a Node whose Alias pointer
		// genuinely closes the cycle back on itself — so this check is
		// load-bearing, not decorative: without it, a cyclic anchor recurses
		// forever. active tracks the path, not the whole document, so the same
		// anchor aliased twice from two independent turns — the ordinary,
		// supported case — is never flagged.
		if r.active[node.Alias] {
			return nil, fmt.Errorf("line %d: anchor %q contains itself", node.Line, node.Value)
		}
		r.active[node.Alias] = true
		resolvedTarget, err := r.resolve(node.Alias)
		delete(r.active, node.Alias)
		if err != nil {
			return nil, err
		}
		resolved, err := r.clone(resolvedTarget)
		if err != nil {
			return nil, err
		}
		resolved.Line, resolved.Column = node.Line, node.Column
		return resolved, nil
	}
	for i, child := range node.Content {
		resolvedChild, err := r.resolve(child)
		if err != nil {
			return nil, err
		}
		node.Content[i] = resolvedChild
	}
	return node, nil
}

// clone returns a copy of node — already alias-free by the time this is
// called, recursively, by resolve — whose Content slice is independently
// allocated all the way down, with every Anchor in the copied subtree
// cleared, not just the root's.
//
// The independent slice is what makes the copy safe to hand to code that
// mutates in place: decodeProviderEntry builds its projection body with
// body.Content = append(body.Content, key, val), and if two alias occurrences
// of one anchor shared a backing array, appending through one copy could
// silently resize or overwrite what the other copy — or the anchor's own
// original definition — sees.
//
// Anchors are cleared at every depth, not only the root: yaml.Marshal would
// otherwise be willing to re-emit "&pending" at a nested position too, and
// DecodeStrict's later re-marshal-and-strict-decode of some OTHER node that
// happens to also read "pending" would then resolve against whichever copy
// was produced most recently rather than failing loudly — a silent
// misattribution, and a stranger bug than the one this file fixes. Since
// resolve already resolved every alias reachable from node before calling
// clone, no live alias in the tree can point at a cleared anchor by the time
// this runs.
func (r *aliasResolver) clone(node *yaml.Node) (*yaml.Node, error) {
	if node == nil {
		return nil, nil
	}
	r.budget++
	if r.budget > maxResolvedNodes {
		return nil, fmt.Errorf("line %d: alias expansion exceeded %d nodes", node.Line, maxResolvedNodes)
	}
	out := *node
	out.Anchor = ""
	if node.Content != nil {
		out.Content = make([]*yaml.Node, len(node.Content))
		for i, c := range node.Content {
			cc, err := r.clone(c)
			if err != nil {
				return nil, err
			}
			out.Content[i] = cc
		}
	}
	return &out, nil
}
