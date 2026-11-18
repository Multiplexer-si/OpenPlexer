package room

// ErrorCode names an error of the hub. A code that the table does not list,
// such as one from a newer hub, is kept as received and behaves as the empty
// row.
type ErrorCode string

const (
	UnsupportedVersion   ErrorCode = "unsupported_version"
	Unauthorized         ErrorCode = "unauthorized"
	TokenExpired         ErrorCode = "token_expired"
	AlreadyAuthenticated ErrorCode = "already_authenticated"
	BadMessage           ErrorCode = "bad_message"
	Schema               ErrorCode = "schema"
	RightToPost          ErrorCode = "right_to_post"
	Forbidden            ErrorCode = "forbidden"
	Unsupported          ErrorCode = "unsupported"
	RateLimit            ErrorCode = "rate_limit"
	Internal             ErrorCode = "internal"
	Coalesced            ErrorCode = "coalesced"
	Behind               ErrorCode = "behind"
)

// PostOutcome is what a poster does when an error answers its post. The zero
// value is Rejected, so a code that no row marks refuses.
type PostOutcome uint8

const (
	Rejected PostOutcome = iota
	Retry
	// Done: the post needs nothing more, and nothing new is stored.
	Done
)

var errorCodes = map[ErrorCode]struct {
	closeWith int
	onPost    PostOutcome
	resumes   bool
}{
	UnsupportedVersion:   {closeWith: 4400},
	Unauthorized:         {closeWith: 4401},
	TokenExpired:         {closeWith: 4401},
	AlreadyAuthenticated: {},
	BadMessage:           {},
	Schema:               {},
	RightToPost:          {},
	Forbidden:            {},
	Unsupported:          {},
	RateLimit:            {onPost: Retry},
	Internal:             {onPost: Retry},
	Coalesced:            {onPost: Done},
	Behind:               {resumes: true},
}

// CloseCode is the WebSocket close code that follows an error with this code.
// 0 keeps the connection.
func (c ErrorCode) CloseCode() int { return errorCodes[c].closeWith }

func (c ErrorCode) PostOutcome() PostOutcome { return errorCodes[c].onPost }
