package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/uitheme"
)

func (w *worker) row(kind, verb, detail string) {
	w.print.Lock()
	defer w.print.Unlock()
	fmt.Fprintln(w.output, "\n"+uitheme.New().Row(kind, verb, detail))
}
func (w *worker) prompt() {
	w.print.Lock()
	defer w.print.Unlock()
	theme := uitheme.New()
	fmt.Fprint(w.output, "\n"+theme.Accent.Render(theme.Prompt+" "))
}
func (w *worker) publishState() {
	if w.options.PaneID == "" || w.options.ProcessID == "" {
		return
	}
	w.presentation.Lock()
	defer w.presentation.Unlock()
	ctx, cancel := context.WithTimeout(w.ctx, 500*time.Millisecond)
	defer cancel()
	client := tmux.Client{Socket: w.options.TmuxSocket}
	if _, err := client.Tagged(ctx, w.options.PaneID, w.options.ProcessID); err != nil {
		return
	}
	_, _ = client.Run(ctx, "set-option", "-p", "-t", w.options.PaneID, "@st_agent_state", "native:"+w.snapshot().State)
}
