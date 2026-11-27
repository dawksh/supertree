package core

import (
	"context"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/workspace"
)

// Registry selects the concrete commands supported by this core assembly.
// Native metadata does not register legacy process, view, layout or agent tools.
func (c *Core) Registry(inspect func(context.Context, InspectArgs) (workspace.Snapshot, error)) (*command.Registry, error) {
	r, err := Registry(inspect)
	if err != nil {
		return nil, err
	}
	if c.native {
		err = c.registerBuffers(r)
		if err == nil {
			err = c.registerTerminals(r)
		}
	} else {
		for _, register := range []func(*command.Registry) error{c.RegisterProcesses, c.RegisterViews, c.RegisterLayouts, c.RegisterCollaboration} {
			if err = register(r); err != nil {
				break
			}
		}
	}
	return r, err
}

func (c *Core) requireLegacy() error {
	if c.native {
		return &command.Error{Code: "unsupported", Message: "legacy runtime commands are unavailable in native metadata mode"}
	}
	return nil
}
