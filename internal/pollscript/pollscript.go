// Package pollscript holds the load-time checks over an entry's scripts that
// more than one reference profile needs: rejecting a `cancel:` block on an entry
// that has no cancel.
//
// It is internal on purpose. Each check is the reference profiles' policy, not
// framework surface (CLAUDE.md house rule 7): a profile outside this module
// writes its own, and nothing here is part of the provider seam. It may import
// provider and scenario; neither may import it.
package pollscript

import (
	"fmt"

	"github.com/c360studio/servicesim/scenario"
)

// RejectCancel returns one load error, under the calling profile's own code,
// when e declares a `cancel:` block, and nil otherwise.
//
// scenario decodes `cancel:` on any entry because which entries have a cancel
// is a profile's knowledge. A profile calls this from the validator of every
// entry it serves no cancel on, so a block nothing would ever read stops the
// load instead of letting its author believe a cancellation was scripted.
func RejectCancel(e *scenario.ProviderEntry, code string) []scenario.Finding {
	if e == nil || e.Cancel == nil {
		return nil
	}
	return []scenario.Finding{{
		Severity: scenario.SeverityError,
		Code:     code,
		Path:     "providers." + e.Name + ".cancel",
		Message: fmt.Sprintf("entry %q serves no cancel operation, so this cancel: block could never take effect; "+
			"remove it, or move it to an entry whose profile serves a cancel", e.Name),
	}}
}
