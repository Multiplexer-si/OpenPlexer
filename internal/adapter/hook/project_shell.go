package hook

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

func projectShellTargets(command string) ([]WriteTarget, error) {
	var targets []WriteTarget
	var err error
	walkShellWrites(command, func(path string) {
		targets = append(targets, WriteTarget{Path: path})
	}, func(verb string, args []string) {
		if err != nil {
			return
		}
		writes, parseErr := projectCommandTargets(verb, args)
		if parseErr != nil {
			err = fmt.Errorf("cannot determine the write targets of %s: %w", verb, parseErr)
			return
		}
		targets = append(targets, writes...)
	})
	return targets, err
}

func projectCommandTargets(verb string, args []string) ([]WriteTarget, error) {
	var dir string
	if verb == "git" {
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			switch args[0] {
			case "-C":
				if len(args) < 2 {
					return nil, nil
				}
				if args[1] != "" {
					dir = rawJoin(dir, args[1])
				}
				args = args[1:]
			case "-p", "-P", "--paginate", "--no-pager":
			default:
				if slices.Contains(args[1:], "mv") {
					return nil, errProjectShellOption
				}
				return nil, nil
			}
			args = args[1:]
		}
		if len(args) == 0 || args[0] != "mv" {
			return nil, nil
		}
		verb, args = "git mv", args[1:]
	}
	optionSet, covered := projectShellOptions[verb]
	if !covered {
		return nil, nil
	}
	parsed, err := optionSet.parse(verb, args)
	if err != nil {
		return nil, err
	}
	if verb == "git mv" || destLastVerbs[verb] && !parsed.dirsOnly {
		return destinationTargets(verb, dir, parsed)
	}
	paths := parsed.paths
	if verb == "sed" {
		if !parsed.inPlace {
			return nil, nil
		}
		if !parsed.hasScript && len(paths) > 0 {
			paths = paths[1:]
		}
	}
	var targets []WriteTarget
	for _, path := range paths {
		if path != "" && !dynamic.MatchString(path) {
			targets = append(targets, WriteTarget{Path: path, Kind: parsed.kind})
		}
	}
	return targets, nil
}

func destinationTargets(verb, dir string, parsed projectOperands) ([]WriteTarget, error) {
	sources := parsed.paths
	destination := ""
	requiresDirectory := parsed.targetDir != nil
	switch {
	case requiresDirectory:
		destination = *parsed.targetDir
	case len(sources) >= 2:
		destination = sources[len(sources)-1]
		sources = sources[:len(sources)-1]
		requiresDirectory = len(sources) > 1
	case verb == "ln" && len(sources) == 1:
		destination = "."
		requiresDirectory = true
	}
	if parsed.noTargetDir && (requiresDirectory || len(sources) != 1) {
		return nil, errProjectShellOption
	}
	if verb == "ln" && runtime.GOOS == "darwin" && parsed.forceDir && parsed.symbolic &&
		parsed.targetDir == nil && len(parsed.paths) >= 2 &&
		!strings.HasSuffix(destination, "/") && filepath.Base(destination) != "." {
		return []WriteTarget{{Path: destination, Kind: WriteEntry}}, nil
	}
	var targets []WriteTarget
	for _, src := range sources {
		src = movePath(dir, src)
		if verb == "mv" || verb == "git mv" {
			targets = append(targets, WriteTarget{Path: src, Kind: WriteEntry})
		}
		if destination == "" {
			continue
		}
		target := WriteTarget{
			Path:              movePath(dir, destination),
			Kind:              parsed.kind,
			Source:            src,
			RequiresDirectory: requiresDirectory,
			NoTargetDir:       parsed.noTargetDir,
			NoFollowDir:       parsed.noFollowDir,
			CopyLink:          parsed.copyLink,
		}
		targets = append(targets, target)
		if parsed.mayReplace && target.Kind == WriteThrough {
			target.Kind, target.CopyLink = WriteEntry, false
			targets = append(targets, target)
		}
	}
	return targets, nil
}

func movePath(dir, path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		return path
	}
	return rawJoin(dir, path)
}

type projectOptions struct {
	shortFlags, shortValues string
	longFlags, longValues   string
}

// Accept options whose effects fit the write model. For example, a backup or
// a recursive copy can write paths besides the destination entry.
var projectShellOptions = map[string]projectOptions{
	"mv":       {"fhinTuv", "t", "force interactive no-clobber no-target-directory update verbose no-copy exchange", "target-directory"},
	"git mv":   {"fknv", "", "force dry-run verbose", ""},
	"cp":       {"cdfHilLnNpPsTvX", "t", "force interactive no-clobber dereference no-dereference link symbolic-link no-target-directory verbose remove-destination", "target-directory"},
	"ln":       {"dFfhinLPrsTvw", "t", "directory force interactive logical no-dereference physical relative symbolic no-target-directory verbose", "target-directory"},
	"install":  {"CcdpsTUv", "fglmo", "compare directory preserve-timestamps strip no-target-directory verbose", "group mode owner target-directory"},
	"rm":       {"dfiIPRrvWx", "", "dir force interactive recursive verbose one-file-system no-preserve-root preserve-root", ""},
	"rmdir":    {"v", "", "verbose ignore-fail-on-non-empty", ""},
	"unlink":   {},
	"touch":    {"acfmh", "Adrt", "no-create no-dereference", "date reference time"},
	"tee":      {"aip", "", "append ignore-interrupts", ""},
	"mkdir":    {"pv", "m", "parents verbose", "mode"},
	"truncate": {"co", "rs", "no-create io-blocks", "reference size"},
	"sed":      {"abEHnrsuUz", "ef", "binary debug null-data posix quiet regexp-extended sandbox separate silent unbuffered follow-symlinks", "expression file line-length"},
}

type projectOperands struct {
	paths                 []string
	targetDir             *string
	kind                  WriteKind
	dirsOnly, noTargetDir bool
	noFollowDir, copyLink bool
	inPlace, hasScript    bool
	mayReplace            bool
	forceDir, symbolic    bool
}

var errProjectShellOption = errors.New("the command uses an unsupported option or option form")

func (s projectOptions) parse(verb string, args []string) (projectOperands, error) {
	var out projectOperands
	switch verb {
	case "git mv":
		out.noFollowDir = true
		fallthrough
	case "mv", "ln", "install", "rm", "rmdir", "unlink", "sed":
		out.kind = WriteEntry
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// BSD and GNU utilities can interpret a flag after an operand differently.
		if len(out.paths) > 0 && len(arg) > 1 && arg[0] == '-' {
			return projectOperands{}, errProjectShellOption
		}
		if arg == "--" {
			out.paths = append(out.paths, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			out.paths = append(out.paths, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, assigned := strings.Cut(arg[2:], "=")
			switch {
			case verb == "sed" && name == "in-place" && value == "":
				out.inPlace = true
			case slices.Contains(strings.Fields(s.longValues), name):
				if !assigned {
					i++
					if i >= len(args) {
						return projectOperands{}, errProjectShellOption
					}
					value = args[i]
				}
				out.apply(verb, "--"+name, value)
			case !assigned && slices.Contains(strings.Fields(s.longFlags), name):
				out.apply(verb, "--"+name, "")
			default:
				return projectOperands{}, errProjectShellOption
			}
			continue
		}
		for j := 1; j < len(arg); j++ {
			flag := arg[j]
			name := "-" + string(flag)
			switch {
			case verb == "sed" && (flag == 'i' || flag == 'I'):
				if j+1 < len(arg) {
					return projectOperands{}, errProjectShellOption
				}
				if flag == 'I' || runtime.GOOS != "linux" {
					if i+1 == len(args) || args[i+1] != "" {
						return projectOperands{}, errProjectShellOption
					}
					i++
				}
				out.inPlace = true
			case strings.IndexByte(s.shortValues, flag) >= 0:
				value := arg[j+1:]
				if value == "" {
					i++
					if i >= len(args) {
						return projectOperands{}, errProjectShellOption
					}
					value = args[i]
				}
				out.apply(verb, name, value)
				j = len(arg)
			case strings.IndexByte(s.shortFlags, flag) >= 0:
				if verb == "install" && flag == 'T' && runtime.GOOS != "linux" {
					return projectOperands{}, errProjectShellOption
				}
				if verb == "ln" && flag == 'F' && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
					return projectOperands{}, errProjectShellOption
				}
				out.apply(verb, name, "")
			default:
				return projectOperands{}, errProjectShellOption
			}
		}
	}
	return out, nil
}

func (p *projectOperands) apply(verb, option, value string) {
	switch verb {
	case "mv":
		p.noFollowDir = p.noFollowDir || option == "-h"
	case "ln":
		switch option {
		case "-h", "-n", "--no-dereference":
			p.noFollowDir = true
		case "-F":
			p.forceDir = true
		case "-s", "--symbolic":
			p.symbolic = true
		}
	case "cp":
		switch option {
		case "-P", "-d", "--no-dereference":
			p.copyLink = true
		case "-H", "-L", "--dereference":
			p.copyLink = false
		case "-c", "-l", "-s", "--link", "--symbolic-link", "--remove-destination":
			p.kind = WriteEntry
		case "-f", "--force":
			p.mayReplace = true
		}
	case "install":
		if option == "-d" || option == "--directory" {
			p.dirsOnly = true
			p.kind = WriteThrough
		}
	case "touch":
		if option == "-h" || option == "--no-dereference" {
			p.kind = WriteLink
		}
	case "sed":
		switch option {
		case "-e", "-f", "--expression", "--file":
			p.hasScript = true
		case "--follow-symlinks":
			p.kind = WriteThrough
		}
	}
	if destLastVerbs[verb] {
		switch option {
		case "-t", "--target-directory":
			p.targetDir = &value
		case "-T", "--no-target-directory":
			p.noTargetDir = true
		}
	}
}
