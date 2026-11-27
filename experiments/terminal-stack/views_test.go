package stack

import (
	"reflect"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

func TestHideMoveAndRevealRetainTerminalIdentity(t *testing.T) {
	v := newViewState(3)
	v.selectTerminal(2)
	v.hide()
	if !reflect.DeepEqual(v.visible(), []int{0, 1}) || v.focus != 1 {
		t.Fatalf("hide: %+v", v)
	}
	r := v.rectangles(120, 30)
	if r[2].Width != 0 || r[0].Width != 59 || r[1].Width != 59 {
		t.Fatalf("hidden view did not release its space: %+v", r)
	}
	v.move(-1)
	if !reflect.DeepEqual(v.visible(), []int{1, 0}) || v.focus != 1 {
		t.Fatal("moving changed focused terminal identity")
	}
	r = v.rectangles(120, 30)
	if r[1].X != 0 || r[0].X != 60 {
		t.Fatal("terminal coordinates did not follow new order")
	}
	v.selectTerminal(2)
	if !reflect.DeepEqual(v.visible(), []int{1, 0, 2}) || v.focus != 2 {
		t.Fatal("select did not restore hidden terminal")
	}
	v.move(1)
	if !reflect.DeepEqual(v.visible(), []int{1, 0, 2}) {
		t.Fatal("edge movement changed order")
	}
}

func TestAllViewsCanBeHiddenAndRestored(t *testing.T) {
	v := newViewState(3)
	v.hide()
	v.hide()
	v.hide()
	v.hide()
	v.next(1)
	v.move(-1)
	if len(v.visible()) != 0 || v.focus != -1 {
		t.Fatal("hiding the last view invented a replacement")
	}
	for _, r := range v.rectangles(120, 30) {
		if r.Width != 0 {
			t.Fatal("hidden terminal still has a placement")
		}
	}
	v.selectTerminal(1)
	if !reflect.DeepEqual(v.visible(), []int{1}) || v.focus != 1 {
		t.Fatal("terminal could not be restored from an empty layout")
	}
	v.selectTerminal(99)
	if v.focus != 1 {
		t.Fatal("invalid selection changed focus")
	}
}

func TestDirectWorkspaceKeysDoNotRequirePrefix(t *testing.T) {
	v := newViewState(3)
	for _, key := range []uv.KeyPressEvent{{Code: '3', Mod: uv.ModAlt}, {Code: '0', Mod: uv.ModAlt}, {Code: 'H', Mod: uv.ModAlt}} {
		if handled, detach := workspaceKey(key, v); !handled || detach {
			t.Fatalf("layout key rejected: %+v", key)
		}
	}
	if !reflect.DeepEqual(v.visible(), []int{1, 0}) {
		t.Fatal(v.visible())
	}
	if handled, _ := workspaceKey(uv.KeyPressEvent{Code: 'a', Mod: uv.ModCtrl}, v); handled {
		t.Fatal("Ctrl-a became a workspace action")
	}
}
