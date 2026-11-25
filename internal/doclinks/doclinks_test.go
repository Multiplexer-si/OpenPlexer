package doclinks

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const moduleRoot = "../.."

var link = regexp.MustCompile(`\]\(([^)\s]+)\)`)

var heading = regexp.MustCompile(`^#{1,6} (.+)$`)
var headingPunctuation = regexp.MustCompile(`[^\p{L}\p{M}\p{N} _-]`)
var codeFence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

func external(target string) bool {
	return strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:")
}

func TestRelativeLinksResolve(t *testing.T) {
	pages := 0
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != moduleRoot && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		pages++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fenced := false
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fenced = !fenced
				continue
			}
			if fenced {
				continue
			}
			for _, match := range link.FindAllStringSubmatch(line, -1) {
				target, _, _ := strings.Cut(match[1], "#")
				if target == "" || external(target) {
					continue
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(path), target)); err != nil {
					page, _ := filepath.Rel(moduleRoot, path)
					t.Errorf("%s links to %s, which does not exist", filepath.ToSlash(page), match[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pages == 0 {
		t.Error("found no Markdown files")
	}
}

func TestPrivacyGuideTrustModelAnchors(t *testing.T) {
	const guidePath = "docs/architecture-and-privacy.md"
	const trustPath = "docs/trust-model.md"
	guide, err := os.ReadFile(filepath.Join(moduleRoot, guidePath))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := os.ReadFile(filepath.Join(moduleRoot, trustPath))
	if err != nil {
		t.Fatal(err)
	}
	anchors := headingAnchors(string(trust))
	checked := 0
	for _, match := range link.FindAllStringSubmatch(string(guide), -1) {
		file, anchor, hasAnchor := strings.Cut(match[1], "#")
		if !hasAnchor || filepath.ToSlash(filepath.Join(filepath.Dir(guidePath), file)) != trustPath {
			continue
		}
		checked++
		if !anchors[anchor] {
			t.Errorf("%s links to missing trust model anchor %q", guidePath, anchor)
		}
	}
	if checked == 0 {
		t.Errorf("%s links to no trust model anchors", guidePath)
	}
}

func headingAnchors(markdown string) map[string]bool {
	// The trust model uses Markdown headings with unique slugs.
	anchors := make(map[string]bool)
	var fence string
	for line := range strings.SplitSeq(markdown, "\n") {
		marker := codeFence.FindStringSubmatch(line)
		if fence != "" {
			if marker != nil && strings.HasPrefix(marker[1], fence) && strings.Trim(marker[2], " \t\r") == "" {
				fence = ""
			}
			continue
		}
		if marker != nil && (marker[1][0] == '~' || !strings.ContainsRune(marker[2], '`')) {
			fence = marker[1]
			continue
		}
		if match := heading.FindStringSubmatch(line); match != nil {
			title := headingPunctuation.ReplaceAllString(strings.ToLower(match[1]), "")
			anchors[strings.ReplaceAll(strings.TrimSpace(title), " ", "-")] = true
		}
	}
	return anchors
}

func TestHeadingAnchorsIgnoreFencedCode(t *testing.T) {
	markdown := "## Before\n" +
		"````md\n### Fenced heading\n```\n### After shorter fence\n" +
		"````md\n### After fence with info\n````\n" +
		"  ~~~\n### Tilde fenced heading\n```\n### After different fence\n  ~~~~ \n" +
		"## After\n" +
		"```\n### Unclosed fence\n"
	anchors := headingAnchors(markdown)
	if len(anchors) != 2 || !anchors["before"] || !anchors["after"] {
		t.Errorf("heading anchors = %v, want only before and after", anchors)
	}
}
