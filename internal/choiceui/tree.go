// SPEC: _spec/internal/choiceui/scope-tree.puml
package choiceui

import (
	"slices"
	"strings"
)

// Tree is a folder tree whose first row is the root. The cursor starts on the root.
type Tree struct {
	nodes  []treeNode // every folder, depth first, siblings by name
	folded map[string]bool
	filter string
	at     string // the cursor's path; "" is the root
}

type treeNode struct {
	path, name string
	depth      int  // 1 for a child of the root
	given      bool // one of the paths NewTree was handed
	under      int  // given paths below this folder
}

// TreeRow is one row the tree shows.
type TreeRow struct {
	Path   string // "" for the root
	Name   string
	Depth  int
	Folder bool // holds other rows
	Folded bool
	Hidden int // given paths a folded folder hides
	Given  bool
}

// NewTree builds the tree of paths and every folder above them. Paths are slash-separated.
func NewTree(paths []string) *Tree {
	seen := map[string]*treeNode{}
	var all []string
	for _, p := range paths {
		p = strings.Trim(p, "/")
		if p == "" || (seen[p] != nil && seen[p].given) {
			continue
		}
		parts := strings.Split(p, "/")
		for i := range parts {
			at := strings.Join(parts[:i+1], "/")
			n := seen[at]
			if n == nil {
				n = &treeNode{path: at, name: parts[i], depth: i + 1}
				seen[at] = n
				all = append(all, at)
			}
			if i == len(parts)-1 {
				n.given = true
			} else {
				n.under++
			}
		}
	}
	slices.SortFunc(all, func(a, b string) int {
		return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
	})
	t := &Tree{folded: map[string]bool{}}
	for _, p := range all {
		t.nodes = append(t.nodes, *seen[p])
	}
	return t
}

// Rows is what the tree shows now: the root, then every folder no fold or filter hides.
func (t *Tree) Rows() []TreeRow {
	rows := []TreeRow{{Folder: len(t.nodes) > 0}}
	for _, n := range t.nodes {
		if !t.shows(n) {
			continue
		}
		folder := n.under > 0
		row := TreeRow{Path: n.path, Name: n.name, Depth: n.depth, Folder: folder, Given: n.given}
		if folder && t.filter == "" && t.folded[n.path] {
			row.Folded, row.Hidden = true, n.under
		}
		rows = append(rows, row)
	}
	return rows
}

// shows reports whether n is on screen: under a filter, a match or a folder above
// one; otherwise, a folder with no folded folder above it.
func (t *Tree) shows(n treeNode) bool {
	if t.filter != "" {
		return t.matches(n.path) || slices.ContainsFunc(t.nodes, func(o treeNode) bool {
			return strings.HasPrefix(o.path, n.path+"/") && t.matches(o.path)
		})
	}
	for at := parent(n.path); at != ""; at = parent(at) {
		if t.folded[at] {
			return false
		}
	}
	return true
}

func (t *Tree) matches(path string) bool {
	return strings.Contains(strings.ToLower(path), strings.ToLower(t.filter))
}

func parent(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return ""
	}
	return path[:i]
}

// At is the cursor's path; "" is the root.
func (t *Tree) At() string { return t.at }

// index is the cursor's row among rows; 0, the root, when its row is hidden.
func (t *Tree) index(rows []TreeRow) int {
	return max(0, slices.IndexFunc(rows, func(r TreeRow) bool { return r.Path == t.at }))
}

// Place puts the cursor on path and opens every folder above it; an unknown path leaves the root.
func (t *Tree) Place(path string) {
	path = strings.Trim(path, "/")
	if !slices.ContainsFunc(t.nodes, func(n treeNode) bool { return n.path == path }) {
		t.at = ""
		return
	}
	for at := parent(path); at != ""; at = parent(at) {
		delete(t.folded, at)
	}
	t.at = path
}

// Move steps the cursor by delta rows and stops at either end.
func (t *Tree) Move(delta int) {
	rows := t.Rows()
	i := min(max(t.index(rows)+delta, 0), len(rows)-1)
	t.at = rows[i].Path
}

// Fold closes the cursor's folder; on a closed folder or a leaf it steps to the folder above.
func (t *Tree) Fold() {
	if t.at == "" || t.filter != "" {
		return
	}
	rows := t.Rows()
	if r := rows[t.index(rows)]; r.Folder && !r.Folded {
		t.folded[t.at] = true
		return
	}
	t.at = parent(t.at)
}

// Unfold opens the cursor's folder.
func (t *Tree) Unfold() { delete(t.folded, t.at) }

// Filter is the text the tree is narrowed to.
func (t *Tree) Filter() string { return t.filter }

// SetFilter narrows the tree to the paths that hold s; the root always stays. The
// cursor goes to the first match unless it is on one, and to the root when none is
// left. Clearing the filter keeps the cursor and opens the folders above it.
func (t *Tree) SetFilter(s string) {
	t.filter = s
	if s == "" {
		t.Place(t.at)
		return
	}
	if t.at != "" && t.matches(t.at) {
		return
	}
	t.at = ""
	for _, n := range t.nodes {
		if t.matches(n.path) {
			t.at = n.path
			return
		}
	}
}
