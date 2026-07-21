package worktree

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"go.kenn.io/kwt/internal/filesystem"
)

// CopyFilesWithGlob copies files from srcRoot to dstRoot, supporting glob patterns and preserving directory structure.
// Errors are returned for each failed copy, but copying continues for all files.
func CopyFilesWithGlob(fs filesystem.FileSystemInterface, srcRoot, dstRoot string, patterns []string) []error {
	var errs []error
	for _, pattern := range patterns {
		patternErrs := copyFilesForPattern(fs, srcRoot, dstRoot, pattern)
		errs = append(errs, patternErrs...)
	}
	return errs
}

// copyFilesForPattern processes a single glob pattern and copies matching files.
func copyFilesForPattern(fs filesystem.FileSystemInterface, srcRoot, dstRoot, pattern string) []error {
	var errs []error

	// matches are relative paths from srcRoot
	matches, err := doublestar.Glob(os.DirFS(srcRoot), pattern)
	if err != nil {
		return []error{fmt.Errorf("invalid glob pattern %q: %w", pattern, err)}
	}

	for _, relPath := range matches {
		srcPath := filepath.Join(srcRoot, relPath)
		info, err := fs.Stat(srcPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("stat %q: %w", srcPath, err))
			continue
		}
		if info.IsDir() {
			continue
		}

		if err := copySingleFile(fs, srcRoot, dstRoot, srcPath); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// copySingleFile copies a single file from srcPath to the corresponding path under dstRoot.
func copySingleFile(fs filesystem.FileSystemInterface, srcRoot, dstRoot, srcPath string) (retErr error) {
	relPath, err := filepath.Rel(srcRoot, srcPath)
	if err != nil {
		return fmt.Errorf("compute relative path for %q: %w", srcPath, err)
	}
	if !filepath.IsLocal(relPath) {
		return fmt.Errorf("copy destination %q escapes destination root", relPath)
	}

	dstPath := filepath.Join(dstRoot, relPath)
	rootInfo, err := os.Lstat(dstRoot)
	if err != nil {
		return fmt.Errorf("inspect destination root %q: %w", dstRoot, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination root %q is a symlink", dstRoot)
	}
	dst, err := os.OpenRoot(dstRoot)
	if err != nil {
		return fmt.Errorf("open destination root %q: %w", dstRoot, err)
	}
	defer func() {
		if closeErr := dst.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close destination root %q: %w", dstRoot, closeErr)
		}
	}()
	if err := mkdirAllWithoutSymlinks(dst, filepath.Dir(relPath), 0o755); err != nil {
		return fmt.Errorf("create directory for %q: %w", dstPath, err)
	}

	srcFile, err := fs.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source file %q: %w", srcPath, err)
	}
	defer func() {
		if closeErr := srcFile.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close source file %q: %w", srcPath, closeErr)
		}
	}()

	if info, statErr := dst.Lstat(relPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination %q is a symlink", dstPath)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("destination %q is not a regular file", dstPath)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect destination %q: %w", dstPath, statErr)
	}

	dstFile, err := dst.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return fmt.Errorf("create destination file %q: %w", dstPath, err)
	}
	defer func() {
		if closeErr := dstFile.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close destination file %q: %w", dstPath, closeErr)
		}
	}()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy %q to %q: %w", srcPath, dstPath, err)
	}

	return nil
}

func mkdirAllWithoutSymlinks(root *os.Root, relative string, perm os.FileMode) error {
	if relative == "." || relative == "" {
		return nil
	}
	if !filepath.IsLocal(relative) {
		return fmt.Errorf("directory %q escapes destination root", relative)
	}
	current := ""
	for _, component := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			if err := root.Mkdir(current, perm); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination directory %q is a symlink", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("destination component %q is not a directory", current)
		}
	}
	return nil
}
