package participle

import (
	"fmt"
	"hash/fnv"
	"reflect"
	"sort"

	"github.com/alecthomas/participle/lexer"
)

const lookaheadLimit = 32

type lookahead struct {
	root   int
	tokens []lexer.Token
}

func (l lookahead) String() string {
	return fmt.Sprintf("lookahead{root: %d, token: %#v}", l.root, l.tokens)
}

func (l *lookahead) hash() uint64 {
	w := fnv.New64a()
	for _, t := range l.tokens {
		fmt.Fprintf(w, "%d:%s\n", t.Type, t.Value)
	}
	return w.Sum64()
}

func buildLookahead(nodes ...node) (table []lookahead, err error) {
	l := &lookaheadWalker{limit: lookaheadLimit, seen: map[node]int{}}
	for root, node := range nodes {
		if node != nil {
			l.push(root, node, nil)
		}
	}
	depth := 0
	for ; depth < lookaheadLimit; depth++ {
		ambiguous := l.ambiguous()
		if len(ambiguous) == 0 {
			return l.collect(), nil
		}
		stepped := false
		for _, group := range ambiguous {
			for _, c := range group {
				// fmt.Printf("root=%d, depth=%d: %T %#v\n", c.root, c.depth, c.branch, c.token)
				if l.step(c.branch, c) {
					stepped = true
				}
			}
			// fmt.Println()
		}
		if !stepped {
			break
		}
	}
	// TODO: We should never fail to build lookahead.
	return nil, fmt.Errorf("could not disambiguate after %d tokens of lookahead", depth)
}

type lookaheadCursor struct {
	branch node // Branch leaf was stepped from.
	lookahead
}

type lookaheadWalker struct {
	seen    map[node]int
	limit   int
	cursors []*lookaheadCursor
}

func (l *lookaheadWalker) collect() []lookahead {
	out := []lookahead{}
	for _, cursor := range l.cursors {
		out = append(out, cursor.lookahead)
	}
	sort.Slice(out, func(i, j int) bool {
		n := len(out[i].tokens)
		m := len(out[j].tokens)
		if n > m {
			return true
		}
		return (n == m && len(out[i].tokens[n-1].Value) > len(out[j].tokens[m-1].Value)) || out[i].root < out[j].root
	})
	return out
}

// Find cursors that are still ambiguous.
func (l *lookaheadWalker) ambiguous() [][]*lookaheadCursor {
	grouped := map[uint64][]*lookaheadCursor{}
	for _, cursor := range l.cursors {
		key := cursor.hash()
		grouped[key] = append(grouped[key], cursor)
	}
	out := [][]*lookaheadCursor{}
	for _, group := range grouped {
		if len(group) > 1 {
			out = append(out, group)
		}
	}
	return out
}

func (l *lookaheadWalker) push(root int, node node, tokens []lexer.Token) {
	cursor := &lookaheadCursor{
		branch: node,
		lookahead: lookahead{
			root:   root,
			tokens: append([]lexer.Token{}, tokens...),
		},
	}
	l.cursors = append(l.cursors, cursor)
	l.step(node, cursor)
}

func (l *lookaheadWalker) remove(cursor *lookaheadCursor) {
	for i, c := range l.cursors {
		if cursor == c {
			l.cursors = append(l.cursors[:i], l.cursors[i+1:]...)
		}
	}
}

// Returns true if a step occurred or false if the cursor has already terminated.
func (l *lookaheadWalker) step(node node, cursor *lookaheadCursor) bool {
	l.seen[node]++
	if cursor.branch == nil || l.seen[node] > 32 {
		return false
	}
	switch n := node.(type) {
	case *disjunction:
		for _, c := range n.nodes {
			l.push(cursor.root, c, cursor.tokens)
		}
		l.remove(cursor)

	case *sequence:
		if n != nil {
			l.step(n.node, cursor)
			cursor.branch = n.next
		}

	case *capture:
		l.step(n.node, cursor)

	case *strct:
		l.step(n.expr, cursor)

	case *optional:
		l.step(n.node, cursor)
		if n.next != nil {
			cursor.branch = n.next
		}

	case *repetition:
		l.push(cursor.root, n.node, cursor.tokens)
		if n.next != nil {
			l.push(cursor.root, n.next, cursor.tokens)
		}
		l.remove(cursor)

	case *parseable:

	case *literal:
		cursor.tokens = append(cursor.tokens, lexer.Token{Type: n.t, Value: n.s})
		cursor.branch = nil
		return true

	case *reference:
		cursor.tokens = append(cursor.tokens, lexer.Token{Type: n.typ})
		cursor.branch = nil

	default:
		panic(fmt.Sprintf("unsupported node type %T", n))
	}

	return true
}

func applyLookahead(m node, seen map[node]bool) error {
	if seen[m] {
		return nil
	}
	seen[m] = true
	switch n := m.(type) {
	case *disjunction:
		lookahead, err := buildLookahead(n.nodes...)
		if err == nil {
			n.lookahead = lookahead
		} else {
			return Error(err.Error() + ": " + n.String())
		}
		for _, c := range n.nodes {
			err := applyLookahead(c, seen)
			if err != nil {
				return err
			}
		}

	case *sequence:
		for c := n; c != nil; c = c.next {
			err := applyLookahead(c.node, seen)
			if err != nil {
				return err
			}
		}

	case *literal:

	case *capture:
		err := applyLookahead(n.node, seen)
		if err != nil {
			return err
		}

	case *reference:

	case *strct:
		err := applyLookahead(n.expr, seen)
		if err != nil {
			return err
		}

	case *optional:
		lookahead, err := buildLookahead(n.node, n.next)
		if err == nil {
			n.lookahead = lookahead
		} else {
			return Error(err.Error() + ": " + n.String())
		}
		err = applyLookahead(n.node, seen)
		if err != nil {
			return err
		}
		if n.next != nil {
			err = applyLookahead(n.next, seen)
			if err != nil {
				return err
			}
		}

	case *repetition:
		lookahead, err := buildLookahead(n.node, n.next)
		if err == nil {
			n.lookahead = lookahead
		} else {
			return Error(err.Error() + ": " + n.String())
		}
		err = applyLookahead(n.node, seen)
		if err != nil {
			return err
		}
		if n.next != nil {
			err = applyLookahead(n.next, seen)
			if err != nil {
				return err
			}
		}

	case *parseable:

	default:
		panic(fmt.Sprintf("unsupported node type %T", m))
	}

	return nil
}

type lookaheadTable []lookahead

// Select node to use.
//
// Will return -2 if lookahead table is missing, -1 for no match, or index of selected node.
func (l lookaheadTable) Select(lex lexer.PeekingLexer, parent reflect.Value) (selected int, err error) {
	if l == nil {
		return -2, nil
	}
next:
	for _, look := range l {
		for depth, lt := range look.tokens {
			t, err := lex.Peek(depth)
			if err != nil {
				return 0, err
			}
			if !((lt.Value == "" || lt.Value == t.Value) && (lt.Type == lexer.EOF || lt.Type == t.Type)) {
				continue next
			}
		}
		return look.root, nil
	}
	return -1, nil
}
