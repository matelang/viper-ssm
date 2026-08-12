package viperssm

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// document turns a flat list of parameters into the JSON document Viper parses.
//
// The parameter name below the path becomes the nesting. Under path /app/prod, the
// parameter /app/prod/db/host lands at {"db":{"host":"..."}}, and Viper reads it as
// the key db.host.
//
// A String or SecureString parameter becomes a JSON string. Numbers and booleans
// stay strings on purpose. Viper's GetInt and GetBool already convert them, and
// guessing here would only add surprises. A StringList becomes a JSON array, split
// on commas. It is not trimmed, because that is how SSM defines the type.
func (p *Provider) document(prefix string, params []types.Parameter) ([]byte, error) {
	root := &node{}
	for _, param := range params {
		if param.Name == nil {
			p.log().Warn("skipping SSM parameter with no name", "prefix", prefix)
			continue
		}
		name := *param.Name

		segments, err := relativeSegments(prefix, name)
		if err != nil {
			return nil, err
		}
		if err := root.insert(prefix, name, segments, leafValue(param)); err != nil {
			return nil, err
		}
	}

	// json.Marshal sorts map keys. So the same set of parameters always gives a
	// byte-for-byte identical document.
	doc, err := json.Marshal(root.value())
	if err != nil {
		// Reachable only if leafValue stops producing strings and slices.
		// Report the failure without echoing what failed to marshal.
		return nil, fmt.Errorf("viperssm: build configuration document for %q: %w", prefix, err)
	}
	return doc, nil
}

// leafValue converts one parameter's value to what belongs in the document.
func leafValue(param types.Parameter) any {
	value := ""
	if param.Value != nil {
		value = *param.Value
	}
	if param.Type != types.ParameterTypeStringList {
		return value
	}
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ",")
}

// relativeSegments strips the path off a parameter name, and splits the rest into
// segments.
func relativeSegments(prefix, name string) ([]string, error) {
	boundary := prefix
	if boundary != "/" {
		boundary += "/"
	}

	// SSM parameter names are case-sensitive, so an exact match is what we expect.
	// Fold the comparison anyway. The alternative to tolerating a differently
	// cased echo of our own path is dropping the parameter.
	if len(name) <= len(boundary) || !strings.EqualFold(name[:len(boundary)], boundary) {
		return nil, &InvalidNameError{
			Name:   name,
			Prefix: prefix,
			Reason: "name is not below the path it was read from",
		}
	}

	segments := strings.Split(name[len(boundary):], "/")
	if slices.Contains(segments, "") {
		return nil, &InvalidNameError{
			Name:   name,
			Prefix: prefix,
			Reason: "name has an empty path segment",
		}
	}
	return segments, nil
}

// node is one level of the configuration document. The lowercased segment keys
// each child, because Viper keys are case-insensitive. The segment field keeps the
// original casing for the document itself.
type node struct {
	segment  string
	name     string // full parameter name; set on leaves only
	leaf     any
	isLeaf   bool
	children map[string]*node
}

// insert places one parameter in the tree, or reports the collision that stops it.
// A collision error names both parameters, so it is fixable without guessing.
func (n *node) insert(prefix, name string, segments []string, value any) error {
	current := n
	for i, segment := range segments {
		key := strings.ToLower(segment)
		// The key as Viper reports it, so the error names what the user sees.
		dotted := dottedKey(segments[:i+1])

		child, exists := current.children[key]
		if !exists {
			child = &node{segment: segment}
			if current.children == nil {
				current.children = make(map[string]*node)
			}
			current.children[key] = child
		}

		if i == len(segments)-1 {
			switch {
			case child.isLeaf:
				return &DuplicateKeyError{Key: dotted, First: child.name, Second: name}
			case len(child.children) > 0:
				return &CollisionError{Key: dotted, Leaf: name, Prefix: child.anyLeafName()}
			}
			child.isLeaf = true
			child.leaf = value
			child.name = name
			return nil
		}

		if child.isLeaf {
			return &CollisionError{Key: dotted, Leaf: child.name, Prefix: name}
		}
		current = child
	}

	// Unreachable: relativeSegments never returns an empty slice.
	return &InvalidNameError{Name: name, Prefix: prefix, Reason: "name has no segments below the path"}
}

// dottedKey renders segments the way Viper reports keys. Joined by dots, and
// lowercased.
func dottedKey(segments []string) string {
	return strings.ToLower(strings.Join(segments, "."))
}

// anyLeafName returns the name of one parameter under n. It picks the same one on
// every run, so a collision error always reads the same.
func (n *node) anyLeafName() string {
	for _, key := range sortedKeys(n.children) {
		child := n.children[key]
		if child.isLeaf {
			return child.name
		}
		if name := child.anyLeafName(); name != "" {
			return name
		}
	}
	return ""
}

// value renders n as what json.Marshal should see.
func (n *node) value() any {
	if n.isLeaf {
		return n.leaf
	}
	out := make(map[string]any, len(n.children))
	for _, child := range n.children {
		out[child.segment] = child.value()
	}
	return out
}

func sortedKeys(children map[string]*node) []string {
	keys := make([]string, 0, len(children))
	for key := range children {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
