package domain

import "fmt"

// State is the state a sub-task occupies at an instant in time. The zero value
// is deliberately invalid so that an unset State is a visible bug rather than a
// silent "To Do".
type State uint8

const (
	StateUnknown State = iota
	StateToDo
	StateInProgress
	StateReviewRequested
	StateFeedbackGiven
	StateApproved
	StateBlocked
	StateDone
)

var stateNames = map[State]string{
	StateUnknown:         "UNKNOWN",
	StateToDo:            "TO_DO",
	StateInProgress:      "IN_PROGRESS",
	StateReviewRequested: "REVIEW_REQUESTED",
	StateFeedbackGiven:   "FEEDBACK_GIVEN",
	StateApproved:        "APPROVED",
	StateBlocked:         "BLOCKED",
	StateDone:            "DONE",
}

func (s State) String() string {
	if name, ok := stateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("State(%d)", uint8(s))
}

// MarshalText renders the stable wire name, so presenters and the GUI share one
// vocabulary without either restating it.
func (s State) MarshalText() ([]byte, error) {
	if _, ok := stateNames[s]; !ok {
		return nil, fmt.Errorf("domain: cannot marshal unrecognised state %d", uint8(s))
	}
	return []byte(s.String()), nil
}
