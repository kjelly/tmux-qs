package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// tmux 3.8's layout includes pane indices, geometry and floating z-order.
// I is informational: tmux assigns cells by index, not by saved pane ID.
type resurrectLayout struct {
	Version int            `json:"V"`
	Root    *resurrectCell `json:"L"`
}
type resurrectCell struct {
	Type     string           `json:"t"`
	Width    int              `json:"w"`
	Height   int              `json:"h"`
	X        int              `json:"x"`
	Y        int              `json:"y"`
	Children []*resurrectCell `json:"c,omitempty"`
	ID       string           `json:"I,omitempty"`
	Index    int              `json:"i,omitzero"`
	Z        *int             `json:"z,omitempty"`
	Active   bool             `json:"a,omitzero"`
	Last     *int             `json:"l,omitempty"`
}

// filterResurrectLayout drops temporary UI leaves, retaining the tiled tree
// and regular floats. Old layouts omit floats and remain unchanged.
func filterResurrectLayout(raw string, panes []ResurrectPane) (string, error) {
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		if strings.Contains(raw, "<") {
			return normalizeLegacyFloatingLayout(raw, panes)
		}
		return raw, nil
	}
	var layout resurrectLayout
	if err := json.Unmarshal([]byte(raw), &layout); err != nil {
		return "", err
	}
	if layout.Version != 2 || layout.Root == nil {
		return "", fmt.Errorf("unsupported layout version %d", layout.Version)
	}
	keep := map[string]bool{}
	for _, p := range panes {
		if p.ID == "" {
			return raw, nil
		}
		keep[p.ID] = true
	}
	var prune func(*resurrectCell) (*resurrectCell, error)
	prune = func(c *resurrectCell) (*resurrectCell, error) {
		if c == nil {
			return nil, fmt.Errorf("nil layout cell")
		}
		if c.Type == "p" {
			if keep[c.ID] {
				return c, nil
			}
			if c.Z == nil {
				return nil, fmt.Errorf("cannot exclude an internal tiled pane from saved geometry")
			}
			return nil, nil
		}
		var children []*resurrectCell
		for _, child := range c.Children {
			next, err := prune(child)
			if err != nil {
				return nil, err
			}
			if next != nil {
				children = append(children, next)
			}
		}
		if len(children) == 0 {
			return nil, nil
		}
		if len(children) == 1 {
			return children[0], nil
		}
		c.Children = children
		return c, nil
	}
	root, err := prune(layout.Root)
	if err != nil {
		return "", err
	}
	if root == nil {
		return "", fmt.Errorf("layout has no restorable panes")
	}
	layout.Root = root
	// i is required even when it is zero. Use a map marshaler for leaves
	// below rather than omitting an explicit zero index.
	data, err := json.Marshal(layout)
	return string(data), err
}

func (c resurrectCell) MarshalJSON() ([]byte, error) {
	type cellAlias resurrectCell
	if c.Type != "p" {
		return json.Marshal(cellAlias(c))
	}
	return json.Marshal(struct {
		cellAlias
		Index int `json:"i"`
	}{cellAlias(c), c.Index})
}

var legacyCellHeader = regexp.MustCompile(`^(\d+)x(\d+),(-?\d+),(-?\d+)`)

// 3.7 appends a floating z-list and also includes those leaves in the
// main tree. Convert mixed snapshots to v2; an internal-picker-only
// snapshot can remain a historical tiled layout, usable on old servers.
func normalizeLegacyFloatingLayout(raw string, panes []ResurrectPane) (string, error) {
	_, body, ok := strings.Cut(raw, ",")
	if !ok {
		return "", fmt.Errorf("invalid layout header")
	}
	tiled, zlist, ok := strings.Cut(body, "<")
	if !ok {
		return raw, nil
	}
	zlist = strings.TrimSuffix(zlist, ">")
	pos := 0
	var parse func(int) (*resurrectCell, error)
	parse = func(depth int) (*resurrectCell, error) {
		if depth > 1000 {
			return nil, fmt.Errorf("layout too deep")
		}
		m := legacyCellHeader.FindStringSubmatch(tiled[pos:])
		if m == nil {
			return nil, fmt.Errorf("invalid legacy cell")
		}
		c := &resurrectCell{Type: "p"}
		c.Width, _ = strconv.Atoi(m[1])
		c.Height, _ = strconv.Atoi(m[2])
		c.X, _ = strconv.Atoi(m[3])
		c.Y, _ = strconv.Atoi(m[4])
		pos += len(m[0])
		if pos < len(tiled) && tiled[pos] == ',' {
			start := pos + 1
			end := start
			for end < len(tiled) && tiled[end] >= '0' && tiled[end] <= '9' {
				end++
			}
			if end > start && (end == len(tiled) || tiled[end] != 'x') {
				c.ID = "%" + tiled[start:end]
				pos = end
			}
		}
		if pos < len(tiled) && (tiled[pos] == '[' || tiled[pos] == '{') {
			close := byte(']')
			c.Type = "v"
			if tiled[pos] == '{' {
				close = '}'
				c.Type = "h"
			}
			pos++
			for {
				child, err := parse(depth + 1)
				if err != nil {
					return nil, err
				}
				c.Children = append(c.Children, child)
				if pos >= len(tiled) {
					return nil, fmt.Errorf("unclosed layout")
				}
				if tiled[pos] == close {
					pos++
					break
				}
				if tiled[pos] != ',' {
					return nil, fmt.Errorf("invalid cell separator")
				}
				pos++
			}
		}
		return c, nil
	}
	root, err := parse(0)
	if err != nil {
		return "", err
	}
	if pos != len(tiled) {
		return "", fmt.Errorf("trailing layout data")
	}
	floating := map[string]bool{}
	// Every z-list entry is a leaf; ID is the fifth comma-delimited field.
	fields := strings.Split(zlist, ",")
	for i := 4; i < len(fields); i += 5 {
		floating["%"+fields[i]] = true
	}
	metadata := map[string]ResurrectPane{}
	var floats []*resurrectCell
	for _, p := range panes {
		metadata[p.ID] = p
	}
	var remove func(*resurrectCell) *resurrectCell
	remove = func(c *resurrectCell) *resurrectCell {
		if c.Type == "p" {
			if floating[c.ID] {
				return nil
			}
			if p, ok := metadata[c.ID]; ok && p.Index != nil {
				c.Index = *p.Index
			}
			return c
		}
		var children []*resurrectCell
		for _, child := range c.Children {
			if next := remove(child); next != nil {
				children = append(children, next)
			}
		}
		if len(children) == 0 {
			return nil
		}
		if len(children) == 1 {
			return children[0]
		}
		c.Children = children
		return c
	}
	root = remove(root)
	if root == nil {
		return "", fmt.Errorf("no tiled panes in layout")
	}
	for i := 4; i < len(fields); i += 5 {
		p, ok := metadata["%"+fields[i]]
		if !ok {
			continue
		}
		index := 0
		if p.Index != nil {
			index = *p.Index
		}
		z := i / 5
		floats = append(floats, &resurrectCell{Type: "p", Width: p.Width, Height: p.Height, X: p.X, Y: p.Y, ID: p.ID, Index: index, Z: &z})
	}
	if len(floats) == 0 {
		body := legacyCellString(root)
		var sum uint16
		for _, b := range []byte(body) {
			sum = (sum >> 1) | (sum << 15)
			sum += uint16(b)
		}
		return fmt.Sprintf("%04x,%s", sum, body), nil
	}
	if root.Type == "p" {
		root = &resurrectCell{Type: "v", Width: root.Width, Height: root.Height, X: root.X, Y: root.Y, Children: []*resurrectCell{root}}
	}
	root.Children = append(root.Children, floats...)
	data, err := json.Marshal(resurrectLayout{Version: 2, Root: root})
	return string(data), err
}

func legacyCellString(c *resurrectCell) string {
	header := fmt.Sprintf("%dx%d,%d,%d", c.Width, c.Height, c.X, c.Y)
	if c.Type == "p" {
		return header + "," + strings.TrimPrefix(c.ID, "%")
	}
	var children []string
	for _, child := range c.Children {
		children = append(children, legacyCellString(child))
	}
	if c.Type == "h" {
		return header + "{" + strings.Join(children, ",") + "}"
	}
	return header + "[" + strings.Join(children, ",") + "]"
}
