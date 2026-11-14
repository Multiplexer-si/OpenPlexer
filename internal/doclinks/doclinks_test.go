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
			if path != moduleRoot && strings.HasPrefix(d.Name(), ".") {
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
