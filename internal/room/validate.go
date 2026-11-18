package room

import "strings"

// Issue is one reason why a value is not valid. Path names the place in the
// value, such as body.paths[2], and is empty for the value itself.
type Issue struct {
	Path   string
	Reason string
}

func (i Issue) String() string {
	if i.Path == "" {
		return i.Reason
	}
	return i.Path + " " + i.Reason
}

// ValidationError holds every reason why a value was rejected. It has at
// least one issue.
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	reasons := make([]string, len(e.Issues))
	for i, issue := range e.Issues {
		reasons[i] = issue.String()
	}
	return strings.Join(reasons, "; ")
}
