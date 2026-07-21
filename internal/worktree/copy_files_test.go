package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/internal/filesystem"
)

func TestCopyFilesWithGlob(t *testing.T) {
	tests := []struct {
		name        string
		dirs        []string
		files       map[string]string
		patterns    []string
		expected    []string
		notExpected []string
	}{
		{
			name: "single file and wildcard",
			dirs: []string{"templates", "config"},
			files: map[string]string{
				"templates/.env.example": "env",
				"config/a.json":          "a",
				"config/b.json":          "b",
			},
			patterns: []string{"templates/.env.example", "config/*.json"},
			expected: []string{
				"templates/.env.example",
				"config/a.json",
				"config/b.json",
			},
		},
		{
			name: "double star recursive",
			dirs: []string{"configs", "configs/dev", "configs/dev/secrets", "other"},
			files: map[string]string{
				"configs/base.yaml":           "base",
				"configs/dev/app.yaml":        "app",
				"configs/dev/secrets/db.yaml": "db",
				"other/ignore.txt":            "ignore",
			},
			patterns: []string{"configs/**"},
			expected: []string{
				"configs/base.yaml",
				"configs/dev/app.yaml",
				"configs/dev/secrets/db.yaml",
			},
			notExpected: []string{"other/ignore.txt"},
		},
		{
			name: "double star with suffix filter",
			dirs: []string{"templates/layouts", "templates/partials/common", "src"},
			files: map[string]string{
				"templates/base.tmpl":                "base",
				"templates/layouts/main.tmpl":        "main",
				"templates/partials/common/nav.tmpl": "nav",
				"templates/README.md":                "readme",
				"src/main.go":                        "go",
			},
			patterns: []string{"templates/**/*.tmpl"},
			expected: []string{
				"templates/base.tmpl",
				"templates/layouts/main.tmpl",
				"templates/partials/common/nav.tmpl",
			},
			notExpected: []string{"templates/README.md", "src/main.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srcDir := t.TempDir()
			dstDir := t.TempDir()

			// Create directories
			for _, dir := range tt.dirs {
				if err := os.MkdirAll(filepath.Join(srcDir, dir), 0755); err != nil {
					t.Fatalf("failed to create dir %s: %v", dir, err)
				}
			}

			// Create files
			for path, content := range tt.files {
				if err := os.WriteFile(filepath.Join(srcDir, path), []byte(content), 0644); err != nil {
					t.Fatalf("failed to write %s: %v", path, err)
				}
			}

			fs := filesystem.NewStandardFileSystem()
			errs := CopyFilesWithGlob(fs, srcDir, dstDir, tt.patterns)
			if len(errs) != 0 {
				t.Errorf("expected no errors, got %v", errs)
			}

			// Check expected files exist
			for _, rel := range tt.expected {
				path := filepath.Join(dstDir, rel)
				if _, err := os.Stat(path); err != nil {
					t.Errorf("expected %s to be copied, err: %v", rel, err)
				}
			}

			// Check notExpected files don't exist
			for _, rel := range tt.notExpected {
				path := filepath.Join(dstDir, rel)
				if _, err := os.Stat(path); err == nil {
					t.Errorf("expected %s to NOT be copied", rel)
				}
			}
		})
	}
}

func TestCopyFilesWithGlobRejectsDestinationSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		symlinkRel string
		copyRel    string
	}{
		{name: "final component", symlinkRel: "config/token", copyRel: "config/token"},
		{name: "parent component", symlinkRel: "config", copyRel: "config/token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srcRoot := t.TempDir()
			dstRoot := t.TempDir()
			outside := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(srcRoot, "config"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(srcRoot, tc.copyRel), []byte("replacement"), 0o600))
			if tc.symlinkRel == tc.copyRel {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dstRoot, tc.symlinkRel)), 0o755))
				victim := filepath.Join(outside, "victim")
				require.NoError(t, os.WriteFile(victim, []byte("original"), 0o600))
				if err := os.Symlink(victim, filepath.Join(dstRoot, tc.symlinkRel)); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else {
				if err := os.Symlink(outside, filepath.Join(dstRoot, tc.symlinkRel)); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}

			errs := CopyFilesWithGlob(filesystem.NewStandardFileSystem(), srcRoot, dstRoot, []string{tc.copyRel})

			require.NotEmpty(t, errs)
			assert.Contains(t, strings.ToLower(errs[0].Error()), "symlink")
			if tc.symlinkRel == tc.copyRel {
				contents, err := os.ReadFile(filepath.Join(outside, "victim"))
				require.NoError(t, err)
				assert.Equal(t, "original", string(contents))
			} else {
				assert.NoFileExists(t, filepath.Join(outside, "token"))
			}
		})
	}
}
