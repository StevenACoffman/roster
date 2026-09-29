package version_test

import (
	"encoding/json"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/StevenACoffman/roster/cmd/version"
)

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func equals[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// GetVersionInfoFrom takes *debug.BuildInfo as a parameter rather than calling
// debug.ReadBuildInfo itself, which is what makes these tests possible: a build
// info value can be constructed, where the real one is fixed at link time and
// differs between a test binary and a release.

func TestGetVersionInfoFromNil(t *testing.T) {
	t.Parallel()

	info := version.GetVersionInfoFrom(nil, "")

	// With no build info, the VCS fields are unknown rather than empty or zero:
	// a blank commit hash in a bug report is indistinguishable from a bug in the
	// reporting.
	for _, field := range []struct{ name, got string }{
		{"ModuleSum", info.ModuleSum},
		{"GitCommit", info.GitCommit},
		{"GitTreeState", info.GitTreeState},
		{"BuildDate", info.BuildDate},
	} {
		if field.got != "unknown" {
			t.Errorf("%s = %q, want %q", field.name, field.got, "unknown")
		}
	}

	// The runtime fields are always available, so they are always filled.
	equals(t, info.GoVersion, runtime.Version())
	equals(t, info.Compiler, runtime.Compiler)
	equals(t, info.Platform, runtime.GOOS+"/"+runtime.GOARCH)
}

func TestGetVersionInfoFromVCSSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		settings         []debug.BuildSetting
		wantCommit       string
		wantTreeState    string
		wantBuildDatePre string
	}{
		{
			name: "a clean tree",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc123"},
				{Key: "vcs.modified", Value: "false"},
				{Key: "vcs.time", Value: "2026-03-09T12:00:00Z"},
			},
			wantCommit:       "abc123",
			wantTreeState:    "clean",
			wantBuildDatePre: "2026-03-09T12:00:00",
		},
		{
			name: "a dirty tree",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "def456"},
				{Key: "vcs.modified", Value: "true"},
			},
			wantCommit:    "def456",
			wantTreeState: "dirty",
		},
		{
			name: "an unparseable vcs.time leaves the date unknown rather than wrong",
			settings: []debug.BuildSetting{
				{Key: "vcs.time", Value: "not a timestamp"},
			},
			wantCommit:       "unknown",
			wantTreeState:    "unknown",
			wantBuildDatePre: "unknown",
		},
		{
			name: "an unrecognised vcs.modified value is neither clean nor dirty",
			settings: []debug.BuildSetting{
				{Key: "vcs.modified", Value: "maybe"},
			},
			wantCommit:    "unknown",
			wantTreeState: "unknown",
		},
		{
			name:          "settings this code does not read are ignored",
			settings:      []debug.BuildSetting{{Key: "GOARCH", Value: "riscv64"}},
			wantCommit:    "unknown",
			wantTreeState: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := version.GetVersionInfoFrom(&debug.BuildInfo{Settings: tt.settings}, "")

			equals(t, info.GitCommit, tt.wantCommit)
			equals(t, info.GitTreeState, tt.wantTreeState)
			if tt.wantBuildDatePre != "" && !strings.HasPrefix(info.BuildDate, tt.wantBuildDatePre) {
				t.Errorf("BuildDate = %q, want it to start with %q",
					info.BuildDate, tt.wantBuildDatePre)
			}
		})
	}
}

// TestGetVersionInfoFromModuleVersion covers the reason Version is a var: the Go
// toolchain fills Main.Version for an installed or tagged build, and that should
// be preferred over the "dev" placeholder without needing -ldflags.
func TestGetVersionInfoFromModuleVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mainVersion   string
		wantGitPrefix string
	}{
		{
			name:          "a tagged module version is adopted",
			mainVersion:   "v1.2.3",
			wantGitPrefix: "v1.2.3",
		},
		{
			// "(devel)" is what the toolchain reports for a local build, and it
			// carries less information than the placeholder it would replace.
			name:          "the toolchain's (devel) placeholder is not adopted",
			mainVersion:   "(devel)",
			wantGitPrefix: "dev",
		},
		{
			name:          "an empty version is not adopted",
			mainVersion:   "",
			wantGitPrefix: "dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := version.GetVersionInfoFrom(&debug.BuildInfo{
				Main: debug.Module{Version: tt.mainVersion},
			}, "")

			equals(t, info.GitVersion, tt.wantGitPrefix)
		})
	}
}

func TestGetVersionInfoFromModuleSum(t *testing.T) {
	t.Parallel()

	info := version.GetVersionInfoFrom(&debug.BuildInfo{
		Main: debug.Module{Sum: "h1:abcdef"},
	}, "")
	equals(t, info.ModuleSum, "h1:abcdef")
}

func TestOptionsApplyAfterGathering(t *testing.T) {
	t.Parallel()

	info := version.GetVersionInfoFrom(nil, "",
		version.WithAppDetails("roster", "OneRoster rostering", "https://example.test"),
		version.WithBuiltBy("goreleaser"),
		version.WithASCIIName("ROSTER\n"),
	)

	equals(t, info.Name, "roster")
	equals(t, info.Description, "OneRoster rostering")
	equals(t, info.URL, "https://example.test")
	equals(t, info.BuiltBy, "goreleaser")
	equals(t, info.ASCIIName, "ROSTER\n")
}

// TestInfoStringContainsEveryField asserts content, not layout. String uses a
// tabwriter, so asserting exact output would be testing the standard library's
// column alignment rather than this package.
func TestInfoStringContainsEveryField(t *testing.T) {
	t.Parallel()

	info := version.GetVersionInfoFrom(&debug.BuildInfo{
		Main: debug.Module{Version: "v9.9.9", Sum: "h1:sum"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "cafebabe"},
			{Key: "vcs.modified", Value: "false"},
		},
	}, "", version.WithBuiltBy("ci"))

	out := info.String()

	for _, want := range []string{
		"GitVersion:", "v9.9.9",
		"GitCommit:", "cafebabe",
		"GitTreeState:", "clean",
		"BuildDate:",
		"BuiltBy:", "ci",
		"GoVersion:", runtime.Version(),
		"Compiler:", runtime.Compiler,
		"ModuleSum:", "h1:sum",
		"Platform:", runtime.GOOS + "/" + runtime.GOARCH,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("String() is missing %q:\n%s", want, out)
		}
	}
}

// TestInfoStringOmitsTheHeaderWithoutAppDetails covers the branch: the name
// block only appears when a caller supplied one, so the default output is the
// field table alone.
func TestInfoStringOmitsTheHeaderWithoutAppDetails(t *testing.T) {
	t.Parallel()

	plain := version.GetVersionInfoFrom(nil, "").String()
	if strings.Contains(plain, "OneRoster rostering") {
		t.Error("the description appeared without WithAppDetails")
	}

	named := version.GetVersionInfoFrom(nil, "",
		version.WithAppDetails("roster", "OneRoster rostering", "https://example.test")).String()
	for _, want := range []string{"roster", "OneRoster rostering", "https://example.test"} {
		if !strings.Contains(named, want) {
			t.Errorf("String() is missing %q:\n%s", want, named)
		}
	}
}

// TestInfoJSONStringIsMachineReadable pins the contract the --json flag exists
// for: a script parses this, so the keys matter and the human-only fields must
// not appear.
func TestInfoJSONStringIsMachineReadable(t *testing.T) {
	t.Parallel()

	info := version.GetVersionInfoFrom(&debug.BuildInfo{
		Main: debug.Module{Version: "v9.9.9"},
	}, "", version.WithAppDetails("roster", "desc", "url"))

	raw, err := info.JSONString()
	ok(t, err)

	var decoded map[string]any
	ok(t, json.Unmarshal([]byte(raw), &decoded))

	for _, key := range []string{
		"gitVersion", "moduleChecksum", "gitCommit", "gitTreeState",
		"buildDate", "builtBy", "goVersion", "compiler", "platform",
	} {
		if _, present := decoded[key]; !present {
			t.Errorf("the JSON output has no %q key: %s", key, raw)
		}
	}

	// Name, Description, URL and ASCIIName are tagged `json:"-"`: they are
	// presentation for the human output, and a script consuming this should not
	// find them and start depending on them.
	for _, key := range []string{"name", "description", "url", "asciiName", "ASCIIName"} {
		if _, present := decoded[key]; present {
			t.Errorf("the JSON output exposes the presentation-only field %q", key)
		}
	}

	equals(t, decoded["gitVersion"], "v9.9.9")
}
