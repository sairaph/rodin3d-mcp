// Package fbx reads the skeleton of an FBX file the way the Hyper3D web
// app does when the user imports a skeleton for rigging: three.js r180
// FBXLoader builds the scene (the web bundle's module 58005), then the
// rigging panel collects its bones (main.fmt.js:55413-55443, kn). Both are
// ported here, quirks included, so rig_model sends the hierarchy the web
// sends for the same file.
package fbx

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Bone is one bone of the imported skeleton: its name, the index of its
// parent bone (-1 for a root) and its world matrix in the file's scene.
type Bone struct {
	Name      string
	Parent    int
	Transform Mat4
}

// node is a three.js Object3D of the loaded scene.
type node struct {
	id       float64
	hasID    bool
	bone     bool
	name     string // originalName, or the sanitized name when that is empty
	parent   *node
	children []*node
	td       *transformData
	matrix   Mat4
	world    Mat4
}

func newNode() *node { return &node{matrix: Identity(), world: Identity()} }

// add is Object3D.add: the child leaves its old parent and goes last.
func (n *node) add(c *node) {
	if c == n {
		return
	}
	if p := c.parent; p != nil {
		for i, x := range p.children {
			if x == c {
				p.children = append(p.children[:i:i], p.children[i+1:]...)
				break
			}
		}
	}
	c.parent = n
	n.children = append(n.children, c)
}

type conn struct {
	id  float64
	rel any
}

type links struct{ parents, children []conn }

// Skeleton returns the bones of an FBX file as the web's skeleton import
// collects them, in its order, or the error the web reports.
func Skeleton(data []byte) ([]Bone, error) {
	tree, err := parseTree(data)
	if err != nil {
		return nil, err
	}
	return bones(tree)
}

func bones(tree *obj) ([]Bone, error) {
	objects := tree.child("Objects")
	if objects == nil {
		return nil, errors.New("the FBX file has no Objects section")
	}
	conns, err := connections(tree)
	if err != nil {
		return nil, err
	}
	skins, err := skeletons(objects, conns)
	if err != nil {
		return nil, err
	}
	models := objects.child("Model")
	// The loader's model Map: keyed by id, insertion order kept when an id
	// is set again.
	byID := map[float64]*node{}
	var ids []float64
	if models != nil {
		for _, key := range models.order() {
			m, _ := models.vals[key].(*obj)
			if m == nil {
				continue
			}
			id := parseIntJS(key)
			l, ok := conns[id]
			if !ok {
				return nil, fmt.Errorf("FBX model %s has no connections", key)
			}
			attrName := m.vals["attrName"]
			n := buildSkeleton(l, skins, id, attrName)
			if n == nil {
				n = newNode()
				typ, _ := m.str("attrType")
				n.bone = typ == "LimbNode" || typ == "Root"
				n.name, n.id, n.hasID = nameOf(attrName), id, true
			}
			n.td = transformDataOf(m)
			if _, dup := byID[id]; !dup {
				ids = append(ids, id)
			}
			byID[id] = n
		}
	}
	scene := newNode()
	for _, id := range ids {
		n := byID[id]
		for _, p := range conns[n.id].parents {
			if parent, ok := byID[p.id]; ok {
				parent.add(n)
			}
		}
		if n.parent == nil {
			scene.add(n)
		}
	}
	var walk func(n *node)
	walk = func(n *node) {
		if n.td != nil {
			local := generateTransform(n.td, n.parent.matrix, n.parent.world)
			p, q, s := local.Decompose()
			n.matrix = Compose(p, q, s)
		}
		if n.parent != nil {
			n.world = Mul(n.parent.world, n.matrix)
		} else {
			n.world = n.matrix
		}
		for _, c := range append([]*node(nil), n.children...) {
			walk(c)
		}
	}
	walk(scene)
	// The loader returns the scene's only child when it is a group; the
	// import walks from there, which gives the same bones as from the scene.
	return collect(scene)
}

// nameOf is String(originalName || sanitizeNodeName(attrName)): the name
// the import reads.
func nameOf(attrName any) string {
	switch v := attrName.(type) {
	case string:
		return v
	case float64:
		if v == 0 || math.IsNaN(v) {
			return ""
		}
		return jsNumber(v)
	case bool:
		if v {
			return "true"
		}
	}
	return ""
}

func jsNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e21 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// connections is parseConnections: per id, its parents and children in
// file order.
func connections(tree *obj) (map[float64]*links, error) {
	out := map[float64]*links{}
	c := tree.child("Connections")
	if !tree.has("Connections") {
		return out, nil
	}
	if c == nil || !c.has("connections") {
		return nil, errors.New("the FBX Connections section lists no connection")
	}
	list, _ := c.vals["connections"].([]any)
	get := func(id float64) *links {
		l, ok := out[id]
		if !ok {
			l = &links{}
			out[id] = l
		}
		return l
	}
	for _, e := range list {
		row, _ := e.([]any)
		child, _ := num(at(row, 0))
		parent, _ := num(at(row, 1))
		rel := at(row, 2)
		get(child).parents = append(get(child).parents, conn{parent, rel})
		get(parent).children = append(get(parent).children, conn{child, rel})
	}
	return out, nil
}

type skin struct {
	clusters []float64 // rawBones: cluster ids, in the skin's child order
}

// skeletons is parseDeformers for the skins: per Skin deformer (by key, in
// for...in order) its Cluster children.
func skeletons(objects *obj, conns map[float64]*links) ([]skin, error) {
	defs := objects.child("Deformer")
	if defs == nil {
		return nil, nil
	}
	var out []skin
	for _, key := range defs.order() {
		d, _ := defs.vals[key].(*obj)
		if d == nil {
			continue
		}
		if typ, _ := d.str("attrType"); typ != "Skin" {
			continue
		}
		l, ok := conns[parseIntJS(key)]
		if !ok {
			return nil, fmt.Errorf("FBX skin deformer %s has no connections", key)
		}
		var s skin
		for _, c := range l.children {
			cl, _ := defs.vals[numKey(c.id)].(*obj)
			if cl == nil {
				return nil, fmt.Errorf("FBX skin deformer %s links an object that is not a deformer", key)
			}
			if typ, _ := cl.str("attrType"); typ != "Cluster" {
				continue
			}
			if tl := cl.child("TransformLink"); tl == nil || !tl.has("a") {
				return nil, fmt.Errorf("FBX cluster %s has no TransformLink", numKey(c.id))
			}
			s.clusters = append(s.clusters, c.id)
		}
		if len(l.parents) == 0 {
			return nil, fmt.Errorf("FBX skin deformer %s is attached to no geometry", key)
		}
		out = append(out, s)
	}
	return out, nil
}

// buildSkeleton is the loader's buildSkeleton: a model linked to a skin
// cluster becomes a bone; one linked to several clusters becomes nested
// copies of itself, the last one outermost.
func buildSkeleton(l *links, skins []skin, id float64, attrName any) *node {
	var b *node
	for _, p := range l.parents {
		for _, s := range skins {
			for _, c := range s.clusters {
				if c == p.id {
					prev := b
					b = newNode()
					b.bone, b.name, b.id, b.hasID = true, nameOf(attrName), id, true
					if prev != nil {
						b.add(prev)
					}
				}
			}
		}
	}
	return b
}

// collect is the rigging panel's kn: bones in depth-first order, a bone met
// again (same FBX id) or named "pose" passes its place to its children.
func collect(root *node) ([]Bone, error) {
	var out []Bone
	seen := map[float64]int{}
	var walk func(n *node, parent int) error
	walk = func(n *node, parent int) error {
		o := parent
		if n.bone {
			if n.hasID {
				if idx, ok := seen[n.id]; ok {
					for _, c := range n.children {
						if err := walk(c, idx); err != nil {
							return err
						}
					}
					return nil
				}
			}
			name := trimJS(n.name)
			if name == "" {
				return errors.New("the FBX contains an unnamed bone")
			}
			if !IsPoseBone(name) {
				if !n.world.Finite() {
					return fmt.Errorf("the FBX bone %q has an invalid transform", name)
				}
				o = len(out)
				out = append(out, Bone{Name: name, Parent: parent, Transform: n.world})
			}
			if n.hasID {
				seen[n.id] = o
			}
		}
		for _, c := range n.children {
			if err := walk(c, o); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, -1); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no usable skeleton bones found in the FBX file")
	}
	return out, nil
}

// IsPoseBone is the web's Mn: a bone named "pose" (after a namespace, a
// mixamorig prefix, spaces, "_" and "-") is no bone of the skeleton.
func IsPoseBone(name string) bool {
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	name = mixamoPrefix.ReplaceAllString(name, "")
	name = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || isJSSpace(r) {
			return -1
		}
		return r
	}, name)
	return strings.ToLower(name) == "pose"
}
