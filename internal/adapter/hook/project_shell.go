package hook

import "strings"

func projectShellTargets(command string) []WriteTarget {
	var targets []WriteTarget
	walkShellWrites(command, func(path string) {
		targets = append(targets, WriteTarget{Path: path})
	}, func(verb string, args []string) {
		switch verb {
		case "git":
			var dir string
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				if gitValueOptions[args[0]] && len(args) > 1 {
					if args[0] == "-C" {
						dir = rawJoin(dir, args[1])
					}
					args = args[1:]
				}
				args = args[1:]
			}
			if len(args) > 0 && args[0] == "mv" {
				targets = append(targets, destinationTargets("mv", dir, args[1:])...)
			}
		case "mv", "cp", "ln", "install":
			targets = append(targets, destinationTargets(verb, "", args)...)
		case "rm", "rmdir", "unlink":
			for _, path := range argTargets(verb, args) {
				targets = append(targets, WriteTarget{Path: path, Kind: WriteEntry})
			}
		default:
			if mutatingVerbs[verb] || verb == "sed" {
				for _, path := range argTargets(verb, args) {
					targets = append(targets, WriteTarget{Path: path})
				}
			}
		}
	})
	return targets
}

func destinationTargets(verb, dir string, args []string) []WriteTarget {
	parsed := parseOperands(verb, args)
	sources := parsed.paths
	if parsed.dirsOnly {
		var targets []WriteTarget
		for _, path := range sources {
			targets = append(targets, WriteTarget{Path: path})
		}
		return targets
	}
	destination := ""
	requiresDirectory := parsed.targetDir != nil
	switch {
	case requiresDirectory:
		destination = *parsed.targetDir
	case len(sources) >= 2:
		destination = sources[len(sources)-1]
		sources = sources[:len(sources)-1]
		requiresDirectory = len(sources) > 1
	}
	kind := WriteThrough
	if verb == "mv" || verb == "ln" {
		kind = WriteEntry
	}
	var targets []WriteTarget
	for _, src := range sources {
		if verb == "mv" {
			targets = append(targets, WriteTarget{Path: movePath(dir, src), Kind: WriteEntry})
		}
		if destination != "" {
			target := WriteTarget{Path: movePath(dir, destination), Kind: kind, RequiresDirectory: requiresDirectory}
			if !parsed.noTargetDir {
				target.Source = src
			}
			targets = append(targets, target)
		}
	}
	return targets
}

func movePath(dir, path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		return path
	}
	return rawJoin(dir, path)
}
