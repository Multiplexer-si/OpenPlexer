package main

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/hub"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

const invitePath = adminPath + "/invites"

var publicInviteID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func runInvite(args []string, stdin io.Reader, stdout, stderr io.Writer, transport http.RoundTripper) int {
	var command string
	switch args[0] {
	case "invite":
		args = args[1:]
		if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
			fmt.Fprintln(stdout, "Usage: openplexer-hub invite create|list|approve|revoke [flags]")
			if len(args) == 0 {
				return 2
			}
			return 0
		}
		command = args[0]
		if strings.HasPrefix(command, "-") {
			command = "create"
		} else {
			args = args[1:]
		}
	case "invites":
		command, args = "list", args[1:]
	case "approve-invite":
		command, args = "approve", args[1:]
	case "revoke-invite":
		command, args = "revoke", args[1:]
	}

	flags := flag.NewFlagSet("invite "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		fmt.Fprintf(stdout, "Usage: openplexer-hub invite %s [flags]\n", command)
		flags.SetOutput(stdout)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	dir := flags.String("data-dir", "./hub-data", "Hub data directory")
	address := flags.String("hub", "", "Remote hub origin")
	tokenFile := flags.String("token-file", "-", "Private lead credential file or - for stdin with --hub")
	var opts inviteOptions
	switch command {
	case "create":
		flags.StringVar(&opts.input.ProjectID, "project", "", "Project ID")
		flags.StringVar(&opts.input.MemberID, "member", "", "New team member ID")
		flags.Func("role", "Hub role (default member)", func(text string) error {
			opts.input.Role = hub.Role(text)
			return nil
		})
		opts.input.Role = hub.Member
		flags.StringVar(&opts.expiry, "expires-in", "48h", "Expiry as a duration or whole seconds")
		flags.BoolVar(&opts.input.RequireApproval, "require-approval", false, "Require lead approval")
		flags.StringVar(&opts.origin, "origin", "", "Public hub origin for a local invite")
	case "list":
		flags.StringVar(&opts.after, "after", "", "List after this public invite ID")
		flags.IntVar(&opts.limit, "limit", hub.DefaultInvitePage, "Maximum invites to list")
	case "approve", "revoke":
		flags.StringVar(&opts.id, "id", "", "Public invite ID")
	default:
		fmt.Fprintln(stderr, "Expected invite create, list, approve, or revoke")
		return 2
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "Invalid invite flags")
		return 2
	}
	used := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { used[f.Name] = true })
	if used["hub"] && (used["data-dir"] || used["origin"]) || !used["hub"] && used["token-file"] {
		fmt.Fprintln(stderr, "Use --hub with a lead credential, or --data-dir with --origin for local creation")
		return 2
	}
	if err := opts.validate(command, used["after"]); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	var err error
	if used["hub"] {
		err = runRemote(*address, *tokenFile, "invite", stdin, transport, func(h remoteAdmin) error {
			return opts.remote(h, command, stdout, stderr)
		})
	} else {
		err = opts.local(hub.Open(*dir), command, stdout, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "openplexer-hub invite %s: %v\n", command, err)
		return 1
	}
	return 0
}

type inviteOptions struct {
	input     hub.InviteInput
	expiry    string
	origin    string
	after, id string
	limit     int
}

func (o *inviteOptions) validate(command string, afterSet bool) error {
	switch command {
	case "create":
		seconds, err := strconv.ParseInt(o.expiry, 10, 32)
		if err != nil {
			d, err := time.ParseDuration(o.expiry)
			if err != nil || d%time.Second != 0 {
				return errors.New("--expires-in requires a whole number of seconds")
			}
			seconds = int64(d / time.Second)
		}
		o.input.ExpiresIn = float64(seconds)
		return o.input.Validate()
	case "list":
		if (afterSet && o.after == "") || !hub.ValidInvitePage(o.after, o.limit) {
			return errors.New("invalid invite page")
		}
	default:
		if !publicInviteID.MatchString(o.id) {
			return errors.New("--id requires a public invite ID")
		}
	}
	return nil
}

func (o inviteOptions) local(store *hub.Store, command string, stdout, stderr io.Writer) error {
	switch command {
	case "create":
		origin, err := remoteOrigin(o.origin)
		if err != nil {
			return err
		}
		keys, err := store.PolicyKeys()
		if err != nil {
			return err
		}
		text, err := json.Marshal(keys)
		if err != nil {
			return err
		}
		return o.createLink(origin, text, stdout, stderr, func() (hub.CreatedInvite, error) {
			return store.CreateInvite(o.input)
		})
	case "list":
		page, err := store.ListInvites(o.after, o.limit)
		return printInviteJSON(stdout, page, err)
	case "approve":
		result, err := store.ApproveInvite(o.id)
		return printInviteJSON(stdout, result, err)
	default:
		result, err := store.RevokeInvite(o.id)
		return printInviteJSON(stdout, result, err)
	}
}

func (o inviteOptions) remote(h remoteAdmin, command string, stdout, stderr io.Writer) error {
	switch command {
	case "create":
		public := h
		public.token = ""
		keys, err := public.request(http.MethodGet, "/.well-known/jwks.json", nil)
		if err != nil {
			return err
		}
		defer keys.Body.Close()
		text, err := io.ReadAll(io.LimitReader(keys.Body, policy.MaxKeySetBytes+1))
		if err != nil {
			return errors.New("could not read the policy keys")
		}
		return o.createLink(h.origin, text, stdout, stderr, func() (created hub.CreatedInvite, err error) {
			body, err := json.Marshal(o.input)
			if err != nil {
				return created, err
			}
			err = h.inviteReply(http.MethodPost, invitePath, body, http.StatusCreated, &created)
			return created, err
		})
	case "list":
		query := url.Values{"limit": {strconv.Itoa(o.limit)}}
		if o.after != "" {
			query.Set("after", o.after)
		}
		var page hub.InvitePage
		err := h.inviteReply(http.MethodGet, invitePath+"?"+query.Encode(), nil, http.StatusOK, &page)
		return printInviteJSON(stdout, page, err)
	default:
		method, path := http.MethodDelete, invitePath+"/"+o.id
		if command == "approve" {
			method, path = http.MethodPost, path+"/approve"
		}
		var result hub.InviteResult
		err := h.inviteReply(method, path, nil, http.StatusOK, &result)
		return printInviteJSON(stdout, result, err)
	}
}

func (o inviteOptions) createLink(origin string, keys []byte, stdout, stderr io.Writer, create func() (hub.CreatedInvite, error)) error {
	fp, err := policy.FingerprintKeySet(keys)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stderr, "Project %s, member %s, hub role %s\n", o.input.ProjectID, o.input.MemberID, o.input.Role); err != nil {
		return errors.New("could not write invite details")
	}
	created, err := create()
	if err != nil {
		return err
	}
	if !validMemberToken(created.Code) {
		return errors.New("invalid invite response from hub")
	}
	if _, err := fmt.Fprintln(stdout, origin+"/join/"+created.Code+"#fp="+fp); err != nil {
		return errors.New("could not write the invite link")
	}
	return nil
}

func printInviteJSON(w io.Writer, result any, err error) error {
	if err != nil {
		return err
	}
	return json.NewEncoder(w).Encode(result)
}

func (h remoteAdmin) inviteReply(method, path string, body []byte, status int, result any) error {
	res, err := h.requestStatus(method, path, body, status)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, remoteAdminLimit+1))
	if err != nil || len(data) > remoteAdminLimit || jsonv2.Unmarshal(data, result) != nil {
		return errors.New("invalid invite response from hub")
	}
	return nil
}
