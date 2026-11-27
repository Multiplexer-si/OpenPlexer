package hook

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestProjectShellWriteOptions(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []WriteTarget
	}{
		{"mv -fh src dest", []WriteTarget{{Path: "src", Kind: WriteEntry}, {Path: "dest", Source: "src", Kind: WriteEntry, NoFollowDir: true}}},
		{"mv --no-target-directory src dest", []WriteTarget{{Path: "src", Kind: WriteEntry}, {Path: "dest", Source: "src", Kind: WriteEntry, NoTargetDir: true}}},
		{"git -C sub mv -f src dest", []WriteTarget{{Path: "sub/src", Kind: WriteEntry}, {Path: "sub/dest", Source: "sub/src", Kind: WriteEntry, NoFollowDir: true}}},
		{"ln -sfh src dest", []WriteTarget{{Path: "dest", Source: "src", Kind: WriteEntry, NoFollowDir: true}}},
		{"ln --no-dereference --force src dest", []WriteTarget{{Path: "dest", Source: "src", Kind: WriteEntry, NoFollowDir: true}}},
		{"ln -fntdest src", []WriteTarget{{Path: "dest", Source: "src", Kind: WriteEntry, RequiresDirectory: true, NoFollowDir: true}}},
		{"ln -s dir/src", []WriteTarget{{Path: ".", Source: "dir/src", Kind: WriteEntry, RequiresDirectory: true}}},
		{"cp --no-dereference src dest", []WriteTarget{{Path: "dest", Source: "src", CopyLink: true}}},
		{"cp -PH src dest", []WriteTarget{{Path: "dest", Source: "src"}}},
		{"cp --remove-destination src dest", []WriteTarget{{Path: "dest", Source: "src", Kind: WriteEntry}}},
		{"install -m0644 src dest", []WriteTarget{{Path: "dest", Source: "src", Kind: WriteEntry}}},
		{"install -d -m 0755 dir", []WriteTarget{{Path: "dir"}}},
		{"touch -h -- -path", []WriteTarget{{Path: "-path", Kind: WriteLink}}},
		{"truncate -cs0 target", []WriteTarget{{Path: "target"}}},
		{"sed --in-place -e 's/foo/bar/' path", []WriteTarget{{Path: "path", Kind: WriteEntry}}},
		{"sed --in-place --follow-symlinks 's/foo/bar/' path", []WriteTarget{{Path: "path"}}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			got, err := projectShellTargets(tc.command)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("write targets = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestProjectShellSedInPlaceOption(t *testing.T) {
	want := []WriteTarget{{Path: "second", Kind: WriteEntry}}
	if runtime.GOOS == "linux" {
		want = append([]WriteTarget{{Path: "p", Kind: WriteEntry}}, want...)
	}
	got, err := projectShellTargets("sed -i '' p second")
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("write targets = %+v, %v; want %+v", got, err, want)
	}
}

func TestProjectShellRejectsUnsupportedOptions(t *testing.T) {
	for _, command := range []string{
		"mv --unmodeled src dest", "cp -fZ src dest", "ln -Q src dest", "install -Q src dest",
		"rm -Q path", "rmdir -Q path", "unlink -Q path", "touch -Q path", "tee -Q path",
		"mkdir -Q path", "truncate -Q path", "sed -Q path", "git --unmodeled mv a b",
		"git mv -Q a b", "mv src -h dest", "rm path -- other", "cp -R src dest",
		"sed --in-place=.backup 's/foo/bar/' path", "mv --unsupported=private-value src dest",
	} {
		t.Run(strings.Fields(command)[0], func(t *testing.T) {
			_, err := projectShellTargets(command)
			if !errors.Is(err, errProjectShellOption) {
				t.Fatalf("error = %v; want unsupported option", err)
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatal("the error includes the option value")
			}
		})
	}
}

func TestProjectShellLNForceDirectory(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS ln uses -F to replace a directory")
	}
	for _, tc := range []struct {
		command string
		want    WriteTarget
	}{
		{"ln -sF src dest", WriteTarget{Path: "dest", Kind: WriteEntry}},
		{"ln -sF src dest/", WriteTarget{Path: "dest/", Source: "src", Kind: WriteEntry}},
		{"ln -sF src dest/.", WriteTarget{Path: "dest/.", Source: "src", Kind: WriteEntry}},
		{"ln -sF src .", WriteTarget{Path: ".", Source: "src", Kind: WriteEntry}},
		{"ln -F src dest", WriteTarget{Path: "dest", Source: "src", Kind: WriteEntry}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			got, err := projectShellTargets(tc.command)
			if err != nil || !slices.Equal(got, []WriteTarget{tc.want}) {
				t.Fatalf("write targets = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}
