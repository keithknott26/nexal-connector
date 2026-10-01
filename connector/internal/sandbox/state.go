package sandbox

// The sandbox state machine. "" is a sandbox the runner has not yet accepted.
//
//	""           -> provisioning
//	provisioning -> running | failed | stopping
//	running      -> stopping | failed | provisioning (reset/re-create) | paused
//	paused       -> running | stopping | failed | provisioning
//	failed       -> stopping | provisioning (reset)
//	stopping     -> deleted | failed
//	deleted      -> (terminal)
var transitions = map[State][]State{
	"":                {StateProvisioning},
	StateProvisioning: {StateRunning, StateFailed, StateStopping, StateProvisioning},
	StateRunning:      {StateStopping, StateFailed, StateProvisioning, StatePaused},
	StatePaused:       {StateRunning, StateStopping, StateFailed, StateProvisioning},
	StateFailed:       {StateStopping, StateProvisioning},
	StateStopping:     {StateDeleted, StateFailed},
	StateDeleted:      {},
}

// CanTransition reports whether from -> to is a legal move. It is pure.
func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Active reports whether a sandbox in state s holds CPU, memory or disk and so
// counts against the caps.
func (s State) Active() bool {
	return s == StateProvisioning || s == StateRunning || s == StatePaused || s == StateStopping
}
