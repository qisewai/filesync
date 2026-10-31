package main

import (
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
)

const treeMotion = 200 * time.Millisecond

type animatedTreeState struct {
	handle ui.Handle
	first  *animatedTreeState
}

type animatedTreeStateList struct{ items []*animatedTreeState }

func (a *app) animatedTree(c *ui.Context, children func()) ui.Element {
	root := ui.Column(c).Role(ui.RoleTree)
	tree := ui.Local(root, "tree", func() animatedTreeStateList { return animatedTreeStateList{} })
	clear(tree.items)
	tree.items = tree.items[:0]
	saved := a.treeKeyboard
	a.treeKeyboard = tree
	defer func() { a.treeKeyboard = saved }()
	root.Children(children)
	return root
}

// Branch snapshots animate out without executing directory loaders after
// collapse. Handles and local state retain focus through interrupted motion.
func (a *app) animatedTreeItem(c *ui.Context, label string, open *bool, children func()) ui.Element {
	depth := len(a.treeBuildStack)
	theme := c.Theme()
	item := ui.ButtonBase(c).Row().Height(theme.Space(7)).Gap(theme.Space(0.5)).
		AlignItems(ui.Center).Justify(ui.Start).Padding(0, theme.Space(2)).Shrink(0).Role(ui.RoleTreeItem).Radius(theme.Radius)
	if item.Hovered() {
		item.Background(theme.SurfaceHover)
	}
	item.Children(func() {
		ui.Box(c).Size(theme.Space(4), theme.Space(4)).Shrink(0).Role(ui.RoleNone)
		ui.Text(c, label).SingleLine()
	})
	state := ui.Local(item, "tree-motion", func() animatedTreeState { return animatedTreeState{} })
	state.first = nil
	tree := a.treeKeyboard
	if tree != nil {
		tree.items = append(tree.items, state)
		move := func(delta int) {
			at := slices.Index(tree.items, state)
			if next := at + delta; at >= 0 && next >= 0 && next < len(tree.items) {
				tree.items[next].handle.Focus(c)
			}
		}
		item.OnShortcut(0, ui.KeyUp, func() { move(-1) }).OnShortcut(0, ui.KeyDown, func() { move(1) })
	}
	item.Bind(&state.handle).Level(depth + 1).
		PaddingStart(c.Theme().Space(1 + 4*float32(depth))).
		Transition(ui.ElementTransition{Duration: treeMotion, Position: true})
	var parent *animatedTreeState
	if depth > 0 {
		parent = a.treeBuildStack[depth-1]
		if parent.first == nil {
			parent.first = state
		}
	}
	branch := open != nil && children != nil
	back, forth := ui.KeyLeft, ui.KeyRight
	if c.Direction() == ui.RightToLeft {
		back, forth = forth, back
	}
	item.OnShortcut(0, back, func() {
		if branch && *open {
			*open = false
		} else if parent != nil {
			parent.handle.Focus(c)
		}
	})
	if !branch {
		return item
	}
	item.Expanded(*open).OnShortcut(0, forth, func() {
		if !*open {
			*open = true
		} else if state.first != nil {
			state.first.handle.Focus(c)
		}
	})
	item.Children(func() {
		p := ui.CollapsibleBase(c.Key("disclosure"), open)
		arrow := p.Trigger.Absolute().Top(c.Theme().Space(1.5)).
			Size(c.Theme().Space(4), c.Theme().Space(4)).Label("展开或收起")
		if c.Direction() == ui.RightToLeft {
			arrow.Right(c.Theme().Space(1 + 4*float32(depth)))
		} else {
			arrow.Left(c.Theme().Space(1 + 4*float32(depth)))
		}
		angle := -90 * (1 - p.Progress())
		if c.Direction() == ui.RightToLeft {
			angle = -angle
		}
		arrow.Children(func() {
			ui.Icon(c, iconChevronDown).Size(c.Theme().Space(4), c.Theme().Space(4)).
				TextColor(c.Theme().TextMuted).Rotate(angle)
		})
	})
	if *open {
		// This keyed sibling keeps descendants scoped separately from row content.
		ui.Column(c.Key(state)).Shrink(0).Transition(ui.ElementTransition{
			Duration: treeMotion, Position: true,
			Enter: &ui.Motion{Collapse: true}, Exit: &ui.Motion{Collapse: true},
		}).Children(func() {
			a.treeBuildStack = append(a.treeBuildStack, state)
			defer func() {
				a.treeBuildStack[depth] = nil
				a.treeBuildStack = a.treeBuildStack[:depth]
			}()
			children()
		})
	}
	return item
}
