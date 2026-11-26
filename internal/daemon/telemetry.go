package daemon

import (
	"encoding/json"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"unicode"

	"github.com/multiplexer-si/openplexer/internal/room"
)

const maxPostBytes = 64 << 10

var (
	secretText = regexp.MustCompile(`ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{10,}|sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|xox[baprs]-[A-Za-z0-9-]+|(?i:(?:api[_-]?key|secret|token)[\s\p{Z}\x{FEFF}]*[:=][\s\p{Z}\x{FEFF}]*['"][A-Za-z0-9_-]{16,})`)
	codeText   = regexp.MustCompile("(?m)[{}`;]|=>|^[\\s\\p{Z}\\x{FEFF}]*(?:const|let|var|function|class|import|export|return|def|async|await|from|type|interface)\\s|^[\\s\\p{Z}\\x{FEFF}]*(?:if|for|while|switch|catch)\\s*\\(|^\\s*[\\w.$]+(?:\\s*\\([^\\n]*\\)\\s*:|\\s*[+*/?-]?=)")
)

type postFields struct {
	ids, prose string
}

var userPostFields = map[string]postFields{
	"status":   {"", "summary plan_step"},
	"decision": {"supersedes", "title detail scope"},
	"question": {"addressed_to", "text"},
	"answer":   {"question_id", "text"},
	"blocked":  {"", "reason"},
	"human":    {"", "text"},
}

func preparePost(text []byte, allowDenial bool) ([]byte, error) {
	if len(text) > maxPostBytes {
		return nil, errors.New("Room post exceeds 64 KiB")
	}
	post, err := room.ParseEventPost(text)
	if err != nil {
		return nil, err
	}
	fields, supported := userPostFields[post.Type]
	if !supported && !(allowDenial && post.Type == "policy.denied") {
		return nil, errors.New("this Room event type cannot be posted by the local agent")
	}
	body := make(map[string]any)
	output := map[string]any{"type": post.Type, "body": body}
	if post.Type == "human" {
		if post.Agent != nil {
			return nil, errors.New("a human post has no agent session")
		}
	} else {
		a := post.Agent
		if a == nil || !safeID(a.ID) || !safeID(a.Vendor) || a.Vendor == "human" ||
			a.Parent != "" && (!safeID(a.Parent) || a.Parent == a.ID) {
			return nil, errors.New("Room post needs a valid agent session, vendor and optional parent")
		}
		agent := map[string]string{"id": a.ID, "vendor": a.Vendor}
		if a.Parent != "" {
			agent["parent"] = a.Parent
		}
		output["agent"] = agent
	}
	if err := copyPostFields(post, fields, body); err != nil {
		return nil, err
	}
	filtered, err := json.Marshal(output)
	if err == nil && len(filtered) > maxPostBytes {
		return nil, errors.New("Room post exceeds 64 KiB after JSON encoding")
	}
	return filtered, err
}

func copyPostFields(post room.EventPost, fields postFields, body map[string]any) error {
	src := post.Body
	idFields := "thread_id reply_to " + fields.ids
	for _, field := range strings.Fields(idFields) {
		if id, present := src[field].(string); present {
			if id != "" && !safeID(id) {
				return errors.New("invalid Room identifier")
			}
			body[field] = id
		}
	}
	for _, field := range strings.Fields(fields.prose) {
		if prose, present := src[field].(string); present {
			if secretText.MatchString(prose) || codeText.MatchString(prose) {
				return errors.New("Room prose was rejected by the content filter")
			}
			body[field] = prose
		}
	}
	switch post.Type {
	case "status":
		return copyPostList(src, body, "files", safeRelativePath)
	case "human":
		return copyPostList(src, body, "mentions", safeID)
	case "question":
		if value, present := src["ack_required"].(bool); present {
			body["ack_required"] = value
		}
	case "blocked":
		if needs, ok := src["needs"].(map[string]any); ok {
			allowed := make(map[string]string)
			if text, present := needs["decision"].(string); present {
				if secretText.MatchString(text) || codeText.MatchString(text) {
					return errors.New("Room prose was rejected by the content filter")
				}
				allowed["decision"] = text
			}
			if id, present := needs["request_id"].(string); present {
				if id != "" && !safeID(id) {
					return errors.New("invalid Room identifier")
				}
				allowed["request_id"] = id
			}
			body["needs"] = allowed
		}
	case "policy.denied":
		action := policyAction(src["action"].(string))
		if action == "" {
			return errors.New("invalid policy denial action")
		}
		path := src["path"].(string)
		if action == "exec" || action == "git.push" || !safeRelativePath(path) {
			path = "[omitted]"
		}
		body["action"], body["path"], body["rule"] = action, path, "denied by policy"
		if decidedAt, present := src["decided_at"]; present {
			body["decided_at"] = decidedAt
		}
	}
	return nil
}

func policyAction(action string) string {
	switch action {
	case "read", "write", "exec", "git.push":
		return action
	}
	return ""
}

func copyPostList(src, dst map[string]any, field string, valid func(string) bool) error {
	if list, present := src[field].([]any); present {
		for _, value := range list {
			if !valid(value.(string)) {
				return errors.New("invalid Room " + field)
			}
		}
		dst[field] = list
	}
	return nil
}

func safeID(id string) bool {
	return hubID.MatchString(id) && !secretText.MatchString(id)
}

func safeRelativePath(path string) bool {
	return fs.ValidPath(path) && !strings.ContainsAny(path, "\\:~") &&
		strings.IndexFunc(path, unicode.IsControl) < 0 && !secretText.MatchString(path) &&
		!codeText.MatchString(path)
}
