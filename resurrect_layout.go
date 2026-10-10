package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
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
	if !strings.HasSuffix(zlist, ">") {
		return "", fmt.Errorf("unclosed floating z-list")
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
	// Every z-list entry is a leaf; ID is the fourth comma-delimited field.
	fields := strings.Split(zlist, ",")
	if zlist == "" {
		fields = nil
	}
	if len(fields)%4 != 0 {
		return "", fmt.Errorf("invalid floating z-list field count")
	}
	for i := 3; i < len(fields); i += 4 {
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
	for i := 3; i < len(fields); i += 4 {
		p, ok := metadata["%"+fields[i]]
		if !ok {
			continue
		}
		index := 0
		if p.Index != nil {
			index = *p.Index
		}
		z := i / 4
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

func resurrectLeaves(root *resurrectCell) []*resurrectCell {
	if root == nil {
		return nil
	}
	if root.Type == "p" {
		return []*resurrectCell{root}
	}
	var leaves []*resurrectCell
	for _, child := range root.Children {
		leaves = append(leaves, resurrectLeaves(child)...)
	}
	return leaves
}

// Older snapshots did not save pane IDs/indices or floating geometry.
// Use JSON's indices to recover that metadata when unambiguous. A legacy
// layout containing extra floating picker cells instead falls back to the
// saved pane paths/programs as tiled panes, matching historical restore.
func prepareRestoreWindow(w ResurrectWindow) (ResurrectWindow, error) {
	w.Panes = slices.Clone(w.Panes)
	missing := slices.ContainsFunc(w.Panes, func(p ResurrectPane) bool { return p.ID == "" })
	if missing && strings.Contains(w.Layout, "<") {
		w.Layout = ""
		w.layoutFallback = true
		return w, nil
	}
	if missing && strings.HasPrefix(strings.TrimSpace(w.Layout), "{") {
		var layout resurrectLayout
		if err := json.Unmarshal([]byte(w.Layout), &layout); err != nil {
			return w, err
		}
		if layout.Version != 2 || layout.Root == nil {
			return w, fmt.Errorf("invalid JSON layout version/root")
		}
		leaves := resurrectLeaves(layout.Root)
		slices.SortFunc(leaves, func(a, b *resurrectCell) int { return cmp.Compare(a.Index, b.Index) })
		if len(leaves) != len(w.Panes) {
			w.Layout = ""
			w.layoutFallback = true
			return w, nil
		}
		for i, c := range leaves {
			index := c.Index
			p := &w.Panes[i]
			p.ID, p.Index, p.Floating = c.ID, &index, c.Z != nil
			p.Width, p.Height, p.X, p.Y = c.Width, c.Height, c.X, c.Y
		}
	}
	if w.Layout != "" {
		raw, err := filterResurrectLayout(w.Layout, w.Panes)
		if err != nil {
			return w, err
		}
		w.Layout = raw
	}
	return w, nil
}

// tmux assigns cells sorted by i to its current pane-list order. Rewriting
// i from the saved-ID -> created-ID mapping keeps each pane's cwd/program
// attached to its saved geometry even when floats and tiles interleave.
func remapRestoredLayout(w ResurrectWindow, ids []string, target string, server tmuxServerSpec, runner capabilityRunner) (string, error) {
	if !strings.HasPrefix(strings.TrimSpace(w.Layout), "{") {
		return w.Layout, nil
	}
	var layout resurrectLayout
	if err := json.Unmarshal([]byte(w.Layout), &layout); err != nil {
		return "", err
	}
	lines, err := runner.runLines(server, "list-panes", "-t", target, "-F", "#{pane_id}\t#{pane_index}")
	if err != nil {
		return "", fmt.Errorf("read restored pane order: %w", err)
	}
	indices := map[string]int{}
	for _, line := range lines {
		id, index, ok := strings.Cut(line, "\t")
		value, err := strconv.Atoi(index)
		if !ok || err != nil {
			return "", fmt.Errorf("invalid restored pane order %q", line)
		}
		indices[id] = value
	}
	saved := map[string]string{}
	for i, p := range w.Panes {
		saved[p.ID] = ids[i]
	}
	leaves := resurrectLeaves(layout.Root)
	if len(leaves) != len(ids) {
		return "", fmt.Errorf("layout pane count does not match snapshot")
	}
	for _, c := range leaves {
		id, ok := saved[c.ID]
		if !ok {
			return "", fmt.Errorf("layout pane %s absent from snapshot", c.ID)
		}
		index, ok := indices[id]
		if !ok {
			return "", fmt.Errorf("created pane %s absent from window", id)
		}
		c.ID, c.Index = id, index
	}
	data, err := json.Marshal(layout)
	return string(data), err
}
