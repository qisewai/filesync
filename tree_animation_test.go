package main

import (
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestTreeMotion(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(map[bool]string{false: "animated", true: "reduced"}[reduced], func(t *testing.T) {
			a := &app{}
			open, nested := false, false
			builds, clicks := 0, 0
			tt := ui.NewTester(func(c *ui.Context) {
				a.animatedTree(c, func() {
					a.animatedTreeItem(c.Key("root"), "Root", &open, func() {
						builds++
						a.animatedTreeItem(c.Key("child"), "Child", &nested, func() {
							a.animatedTreeItem(c.Key("leaf"), "Leaf", nil, nil)
						})
					}).OnClick(func() { clicks++ })
					a.animatedTreeItem(c.Key("next"), "Next", nil, nil)
				})
			}, 300, 240)
			tt.SetPreferences(ui.Preferences{ReduceMotion: reduced})
			tt.Frame()
			closed, _ := tt.Find("Next")
			if err := tt.Click("展开或收起"); err != nil {
				t.Fatal(err)
			}
			if !open || clicks != 0 {
				t.Fatal("arrow should only expand")
			}
			tt.Advance(70 * time.Millisecond)
			middle, _ := tt.Find("Next")
			if out := os.Getenv("FILESYNC_TREE_SNAPSHOT"); out != "" && !reduced {
				f, err := os.Create(out)
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, tt.Image())
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			tt.Advance(treeMotion)
			expanded, _ := tt.Find("Next")
			if expanded.Y <= closed.Y {
				t.Fatal("branch did not occupy height")
			}
			if !reduced && !(middle.Y > closed.Y && middle.Y < expanded.Y) {
				t.Fatalf("no intermediate layout: closed=%v middle=%v expanded=%v", closed, middle, expanded)
			}
			if reduced && middle.Y != expanded.Y {
				t.Fatal("reduced motion still animates")
			}
			if err := tt.Click("Root"); err != nil {
				t.Fatal(err)
			}
			tt.Key(0, ui.KeyRight)
			if !tt.Focused("Child") {
				t.Fatal("Right did not focus child")
			}
			tt.Key(0, ui.KeyRight)
			tt.Advance(treeMotion)
			if !nested || !tt.HasText("Leaf") {
				t.Fatal("nested expansion failed")
			}
			tt.Key(0, ui.KeyLeft)
			tt.Key(0, ui.KeyLeft)
			if !tt.Focused("Root") {
				t.Fatal("Left did not focus parent")
			}
			tt.Key(0, ui.KeyLeft)
			if open {
				t.Fatal("Left did not collapse")
			}
			before := builds
			tt.Advance(treeMotion + time.Millisecond)
			if builds != before || tt.HasText("Child") {
				t.Fatal("collapsed children still build")
			}
			tt.Key(0, ui.KeyDown)
			if !tt.Focused("Next") {
				t.Fatal("Down did not skip collapsed branch")
			}
			// Reverse in flight, then remove and restore nested content.
			for range 4 {
				wasOpen := open
				if err := tt.Click("展开或收起"); err != nil {
					t.Fatal(err)
				}
				if open == wasOpen {
					t.Fatal("rapid arrow click was lost")
				}
				tt.Advance(35 * time.Millisecond)
			}
			tt.Advance(treeMotion)
			if len(a.treeBuildStack) != 0 {
				t.Fatal("unbalanced tree build")
			}
		})
	}
}
