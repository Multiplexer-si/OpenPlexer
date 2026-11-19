package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/jsonedit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

const floorFile = "policy-floor.json"

type floorEntry struct {
	Version int64  `json:"version"`
	SHA256  string `json:"sha256"`
}

func floorEntryFor(version int64, payload []byte) (floorEntry, error) {
	canonical, err := audit.Canonical(payload)
	if err != nil {
		return floorEntry{}, fmt.Errorf("cannot hash the bundle for the policy version floor: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return floorEntry{Version: version, SHA256: hex.EncodeToString(digest[:])}, nil
}

func (g *Gate) seedFloor(floor map[string]floorEntry, inForce *policy.Verified) error {
	var bundle *policy.Bundle
	var payload []byte
	switch {
	case inForce != nil:
		bundle, payload = inForce.Bundle, inForce.Payload
	case g.status.State == Rejected:
		// A rejected policy can constrain admission through the floor only.
		stored, err := readStoredPolicy(g.home)
		if err != nil || stored == nil {
			return nil
		}
		signed, err := policy.ReadSignedBundle(stored.Token)
		if err != nil {
			return nil
		}
		bundle, payload, err = signed.ParseUnverifiedPayload()
		if err != nil {
			return nil
		}
	default:
		return nil
	}
	entry, err := floorEntryFor(bundle.Version, payload)
	if err != nil {
		if inForce == nil {
			return nil
		}
		return err
	}
	floor[bundle.ProjectID] = entry
	return writeFloor(g.home, floor)
}

// readFloor returns nil only when the file is missing.
func readFloor(home string) (map[string]floorEntry, error) {
	path := filepath.Join(home, floorFile)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular file")
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(text) {
		return nil, errors.New("invalid UTF-8")
	}
	doc, err := jsonedit.Parse(text)
	if err != nil {
		return nil, err
	}
	root := doc.Root()
	if root.Kind != jsonedit.Object {
		return nil, errors.New("expected a JSON object")
	}
	if member, duplicate := root.DuplicateKey(); duplicate {
		return nil, fmt.Errorf("duplicate key %q", member.Key)
	}
	floor := make(map[string]floorEntry, len(root.Members))
	for _, project := range root.Members {
		entry, err := readFloorEntry(doc, project.Value)
		if err != nil {
			return nil, fmt.Errorf("project %q: %w", project.Key, err)
		}
		floor[project.Key] = entry
	}
	return floor, nil
}

func readFloorEntry(doc *jsonedit.Doc, node jsonedit.Node) (floorEntry, error) {
	if node.Kind != jsonedit.Object {
		return floorEntry{}, errors.New("expected a JSON object")
	}
	if member, duplicate := node.DuplicateKey(); duplicate {
		return floorEntry{}, fmt.Errorf("duplicate key %q", member.Key)
	}
	var entry floorEntry
	version, _, _ := node.Get("version")
	if version.Kind != jsonedit.Number || doc.Decode(version, &entry.Version) != nil {
		return floorEntry{}, errors.New("invalid version")
	}
	digest, _, _ := node.Get("sha256")
	if digest.Kind != jsonedit.String || doc.Decode(digest, &entry.SHA256) != nil {
		return floorEntry{}, errors.New("invalid sha256")
	}
	sum, err := hex.DecodeString(entry.SHA256)
	if err != nil || len(sum) != sha256.Size {
		return floorEntry{}, errors.New("invalid sha256")
	}
	entry.SHA256 = hex.EncodeToString(sum)
	if len(node.Members) != 2 {
		return floorEntry{}, errors.New("unexpected field")
	}
	return entry, nil
}

func writeFloor(home string, floor map[string]floorEntry) error {
	if err := writeJSON(home, floorFile, floor); err != nil {
		return fmt.Errorf("cannot write policy version floor (%s): %w", floorFile, err)
	}
	return nil
}
