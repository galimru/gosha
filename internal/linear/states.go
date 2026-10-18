package linear

import (
	"fmt"
	"strings"

	"github.com/galimru/gosha/internal/config"
)

// States are the team's states that Gosha uses, resolved from the config's
// names (DESIGN §3). Done and Canceled are recognised by type, not name.
type States struct {
	Ready, InProgress, InReview, Blocked State
}

// ResolveStates maps the configured names to the team's states. Any missing
// state is an error: Gosha refuses to run the project.
func ResolveStates(t *Team, names config.States) (*States, error) {
	byName := map[string]State{}
	for _, s := range t.States {
		byName[s.Name] = s
	}
	var missing []string
	get := func(key, name string) State {
		s, ok := byName[name]
		if !ok {
			missing = append(missing, fmt.Sprintf("%s (%q)", key, name))
		}
		return s
	}
	st := &States{
		Ready:      get("ready", names.Ready),
		InProgress: get("in_progress", names.InProgress),
		InReview:   get("in_review", names.InReview),
		Blocked:    get("blocked", names.Blocked),
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("team %s has no state %s: create it or set linear.states", t.Key, strings.Join(missing, ", "))
	}
	return st, nil
}

// IsDone reports a completed-type state (Done, whatever it's called).
func (s State) IsDone() bool { return s.Type == TypeCompleted }

// IsCanceled reports a canceled- or duplicate-type state.
func (s State) IsCanceled() bool { return s.Type == TypeCanceled || s.Type == TypeDuplicate }
