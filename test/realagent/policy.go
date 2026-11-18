package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
)

type openplexer struct {
	bin  string
	home string
}

func (o openplexer) run(dir string, args ...string) (stdout, stderr string, exit int, err error) {
	cmd := exec.Command(o.bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "OPENPLEXER_HOME="+o.home)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), errOut.String(), ee.ExitCode(), nil
	}
	return out.String(), errOut.String(), 0, err
}

func (o openplexer) install(agent, project string) (string, error) {
	stdout, stderr, exit, err := o.run(project, "install", agent, "--project", project)
	output := stdout + stderr
	if err != nil || exit != 0 {
		return output, fmt.Errorf("openplexer install %s: exit %d, %v\n%s", agent, exit, err, output)
	}
	return output, nil
}

func (o openplexer) checkRoot(project string) error {
	recorded, err := hook.Journal{Dir: filepath.Join(o.home, "installs")}.Roots()
	if err != nil {
		return err
	}
	if !slices.Contains(recorded, project) {
		return fmt.Errorf("install recorded the project roots %q, want %s", recorded, project)
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if found, err := (hook.Roots{Home: user}).Find(project); err != nil || found != project {
		return fmt.Errorf("without the record, the root of %s would be %q (%v), want the project itself as the only candidate", project, found, err)
	}
	return nil
}

func buildOpenPlexer(binDir string) (bin, version string, err error) {
	bin = filepath.Join(binDir, "openplexer")
	if out, err := exec.Command("go", "build", "-o", bin, "./apps/openplexer").CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("go build ./apps/openplexer: %w\n%s", err, out)
	}
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		return "", "", err
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	switch {
	case revision == "":
		return bin, "unknown", nil
	case modified == "true":
		return bin, revision + "+modified", nil
	}
	return bin, revision, nil
}

const (
	memberID  = "mem-realagent"
	projectID = "prj-realagent"
)

var testBundle = map[string]any{
	"project_id": projectID,
	"version":    1,
	"repos":      []string{"app"},
	"areas": []any{
		map[string]any{"name": "project", "patterns": []string{"app:**"}},
		map[string]any{"name": "sentinel", "patterns": []string{"app:sentinel/**"}},
	},
	"roles": map[string]any{
		"dev": map[string]any{"read": []string{}, "write": []string{"project"}, "exec": []string{"ls", "pwd", "cat"}, "push": []string{}},
	},
	"members": map[string]string{memberID: "dev"},
}

func signPolicy(bundle any) (keys, message []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	b64 := base64.RawURLEncoding.EncodeToString
	payload, err := json.Marshal(bundle)
	if err != nil {
		return nil, nil, err
	}
	signingInput := b64([]byte(`{"alg":"ES256","typ":"mp-policy+jws"}`)) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return nil, nil, err
	}
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	point, err := key.PublicKey.Bytes()
	if err != nil {
		return nil, nil, err
	}
	keys, err = json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "EC", "crv": "P-256", "x": b64(point[1:33]), "y": b64(point[33:])}}})
	if err != nil {
		return nil, nil, err
	}
	message, err = json.Marshal(map[string]any{"version": 1, "bundle": bundle, "signature": signingInput + "." + b64(signature)})
	return keys, message, err
}

var wantStatusExit = map[policySetup]int{verifiedPolicy: 0, stalePolicy: 3, noPolicy: 4}

func setUpPolicy(op openplexer, s scenario, dir string) (string, error) {
	keys, message, err := signPolicy(testBundle)
	if err != nil {
		return "", err
	}
	keysPath, messagePath := filepath.Join(dir, "keys.json"), filepath.Join(dir, "message.json")
	for path, data := range map[string][]byte{keysPath: keys, messagePath: message} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return "", err
		}
	}
	configure := []string{"configure", "--keys", keysPath, "--member", memberID}
	if s.policy == stalePolicy {
		configure = append(configure, "--stale-after-days", "1")
	}
	steps := [][]string{configure}
	if s.policy != noPolicy {
		steps = append(steps, []string{"policy", "load", messagePath})
	}
	for _, args := range steps {
		if _, stderr, exit, err := op.run(dir, args...); err != nil || exit != 0 {
			return "", fmt.Errorf("openplexer %s: exit %d, %v %s", strings.Join(args, " "), exit, err, stderr)
		}
	}
	if s.policy == stalePolicy {
		if err := ageConfirmation(op.home, 48*time.Hour); err != nil {
			return "", err
		}
	}
	stdout, _, exit, err := op.run(dir, "policy", "status")
	if err != nil {
		return "", err
	}
	if exit != wantStatusExit[s.policy] {
		return "", fmt.Errorf("policy status exited %d, want %d:\n%s", exit, wantStatusExit[s.policy], stdout)
	}
	return fmt.Sprintf("%s(exit %d)", stdout, exit), nil
}

func ageConfirmation(home string, age time.Duration) error {
	path := filepath.Join(home, "policy.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	stored["confirmed_at"] = time.Now().UTC().Add(-age).Format(time.RFC3339)
	data, err = json.Marshal(stored)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
