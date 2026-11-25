package hubconformance

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCommaBearerBodyOrder(t *testing.T) {
	const replay = "OPENPLEXER_TEST_COMMA_BEARER_REPLAY"
	external := []string{"-hub-url=https://hub.example.test"}
	wrangler := []string{"-hub-url=https://hub.example.test", "-wrangler-dev"}
	for _, tc := range []struct {
		name       string
		flags      []string
		status     int
		omitHeader string
		failure    string
	}{
		{name: "go-401", status: 401, failure: "status 401, want 413"},
		{name: "go-413", status: 413},
		{name: "wrangler-401", flags: wrangler, status: 401},
		{name: "wrangler-413", flags: wrangler, status: 413, failure: "status 413, want 401"},
		{name: "external-401", flags: external, status: 401},
		{name: "external-413", flags: external, status: 413},
		{name: "external-500", flags: external, status: 500, failure: "status 500, want 401 or 413"},
		{name: "external-401-no-challenge", flags: external, status: 401, omitHeader: "WWW-Authenticate", failure: "missing WWW-Authenticate: Bearer"},
		{name: "external-413-no-cache-control", flags: external, status: 413, omitHeader: "Cache-Control", failure: "missing Cache-Control: no-store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Getenv(replay) == "1" {
				replayCommaBearer(t, tc.status, tc.omitHeader)
				return
			}
			// A rejected response fails the conformance test, so observe it in a subprocess.
			args := []string{"-test.run=^TestCommaBearerBodyOrder$/^" + tc.name + "$/^publish-policy$/^auth-and-body-order$"}
			cmd := exec.CommandContext(t.Context(), os.Args[0], append(args, tc.flags...)...)
			cmd.Env = append(os.Environ(), replay+"=1")
			out, err := cmd.CombinedOutput()
			if tc.failure == "" {
				if err != nil {
					t.Fatalf("conformance rejected the response: %v\n%s", err, out)
				}
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), tc.failure) {
				t.Fatalf("conformance should fail with %q: %v\n%s", tc.failure, err, out)
			}
		})
	}
}

func replayCommaBearer(t *testing.T, replayStatus int, omitHeader string) {
	t.Helper()
	const commaBearer = "Bearer aaaaaaaaaaaaaaaaaaaaa,aaaaaaaaaaaaaaaaaaaaa"
	wantSizes := []int64{policyLimit + 1, 2}
	if *wranglerDev {
		wantSizes = wantSizes[1:]
	}
	target, err := targetFromFlags(*hubURL, *wranglerDev)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{target: target, origin: "https://hub.example.test", unknown: strings.Repeat("b", 43)}
	commaRequests := 0
	f.client = &http.Client{Transport: commaBearerTransport(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		status := http.StatusUnauthorized
		switch req.Header.Get("Authorization") {
		case commaBearer:
			if len(req.Header.Values("Authorization")) != 1 {
				t.Fatal("comma bearer must use a single Authorization field")
			}
			if commaRequests >= len(wantSizes) || req.ContentLength != wantSizes[commaRequests] {
				t.Fatalf("comma request %d has body length %d, want sizes %v", commaRequests+1, req.ContentLength, wantSizes)
			}
			commaRequests++
			if commaRequests == 1 {
				status = replayStatus
			}
		case "Bearer " + f.unknown:
			if len(req.Header.Values("Authorization")) == 1 {
				status = http.StatusRequestEntityTooLarge
			}
		}
		header := http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
		if status == http.StatusUnauthorized {
			header.Set("WWW-Authenticate", "Bearer")
		}
		if req.Header.Get("Authorization") == commaBearer && commaRequests == 1 {
			header.Del(omitHeader)
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(`{"error":"replayed response"}`))}, nil
	})}
	f.auth(t)
	if !t.Failed() && commaRequests != len(wantSizes) {
		t.Errorf("sent %d comma bearer requests, want %d", commaRequests, len(wantSizes))
	}
}

type commaBearerTransport func(*http.Request) (*http.Response, error)

func (f commaBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
