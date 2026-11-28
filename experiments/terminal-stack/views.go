package stack

// viewState is client-only experiment state. Order and visibility refer to the
// original terminal indices; changing placement must never change input targets.
// Production per-worktree tabs/layout persistence belongs to later stages.
type viewState struct {
	order  []int
	hidden []bool
	focus  int
	solo   bool
}

func newViewState(count int) *viewState {
	v := &viewState{hidden: make([]bool, count), focus: -1}
	for i := 0; i < count; i++ {
		v.order = append(v.order, i)
	}
	if count > 0 {
		v.focus = 0
	}
	return v
}
func (v *viewState) visible() []int {
	var ids []int
	for _, id := range v.order {
		if !v.hidden[id] {
			ids = append(ids, id)
		}
	}
	return ids
}
func (v *viewState) selectTerminal(id int) {
	if id >= 0 && id < len(v.order) {
		v.hidden[id] = false
		v.focus = id
	}
}
func (v *viewState) next(delta int) {
	ids := v.visible()
	for i, id := range ids {
		if id == v.focus {
			v.focus = ids[(i+delta+len(ids))%len(ids)]
			return
		}
	}
}
func (v *viewState) hide() {
	if v.focus < 0 {
		return
	}
	ids := v.visible()
	position := 0
	for i, id := range ids {
		if id == v.focus {
			position = i
			break
		}
	}
	v.hidden[v.focus] = true
	ids = v.visible()
	v.focus = -1
	if len(ids) > 0 {
		v.focus = ids[min(position, len(ids)-1)]
	}
}
func (v *viewState) move(delta int) {
	ids := v.visible()
	for i, id := range ids {
		if id != v.focus {
			continue
		}
		other := i + delta
		if other < 0 || other >= len(ids) {
			return
		}
		left, right := -1, -1
		for j, ordered := range v.order {
			if ordered == id {
				left = j
			}
			if ordered == ids[other] {
				right = j
			}
		}
		v.order[left], v.order[right] = v.order[right], v.order[left]
		return
	}
}
func (v *viewState) rectangles(width, height int) []Rect {
	ids := v.visible()
	position := 0
	for i, id := range ids {
		if id == v.focus {
			position = i
		}
	}
	visible := Rectangles(width, height, len(ids), position, v.solo)
	result := make([]Rect, len(v.order))
	for i, id := range ids {
		result[id] = visible[i]
	}
	return result
}
