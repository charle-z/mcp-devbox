//go:build !windows

// Package edgeupdate performs the narrow privileged filesystem transaction used by
// the MCP Devbox bundle updater. Network release resolution is deliberately outside
// this package; callers may pass only a locally staged release plus identity resolved
// from the official signed release channel.
package edgeupdate

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/charle-z/mcp-devbox/internal/bundle"
)

const (
	ReleasesDirectory = "releases"
	CurrentLink       = "current"
	PreviousLink      = "previous"
	updateLockFile    = ".bundle-update.lock"
)

var ErrHealthCheck = errors.New("edge health check failed; previous bundle restored")

type Service interface {
	InstallUnit(string) error
	RestartEdge() error
	EdgeHealthy() bool
}

type Engine struct {
	Root      string
	PublicKey ed25519.PublicKey
	Service   Service
}

type Status struct {
	Release         string `json:"release"`
	PreviousRelease string `json:"previous_release,omitempty"`
	ServiceActive   bool   `json:"service_active"`
}

func (e Engine) Install(source string, expected bundle.Compatibility) (Status, error) {
	root, err := e.validRoot()
	if err != nil {
		return Status{}, err
	}
	unlock, err := lockUpdater(root)
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	if _, err := bundle.LoadAndVerify(source, e.PublicKey, expected); err != nil {
		return Status{}, err
	}
	desiredTarget := filepath.Join(ReleasesDirectory, expected.Release)
	rawCurrentTarget, _ := os.Readlink(filepath.Join(root, CurrentLink))
	before, _ := statusFromLinks(root, e.Service)
	if before.Release != "" {
		order, compareErr := bundle.CompareRelease(expected.Release, before.Release)
		if compareErr != nil || order < 0 {
			return Status{}, errors.New("automatic release installation cannot downgrade the active release")
		}
	}
	replaceInvalidActive := rawCurrentTarget == desiredTarget
	if before.Release == expected.Release {
		activeRoot := filepath.Join(root, ReleasesDirectory, expected.Release)
		if _, err := bundle.LoadAndVerify(activeRoot, e.PublicKey, expected); err == nil {
			return before, nil
		}
		replaceInvalidActive = true
	}
	releases := filepath.Join(root, ReleasesDirectory)
	if err := os.MkdirAll(releases, 0o755); err != nil {
		return Status{}, errors.New("release directory unavailable")
	}
	target := filepath.Join(releases, expected.Release)
	replacedBackup := ""
	if info, statErr := os.Lstat(target); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Status{}, errors.New("existing release path is unsafe")
		}
		if _, err := bundle.LoadAndVerify(target, e.PublicKey, expected); err != nil {
			if !replaceInvalidActive {
				return Status{}, err
			}
			staging, stageErr := stageSignedRelease(source, releases, expected, e.PublicKey)
			if stageErr != nil {
				return Status{}, stageErr
			}
			backup, backupErr := os.MkdirTemp(releases, ".replaced-"+expected.Release+"-")
			if backupErr != nil {
				_ = os.RemoveAll(staging)
				return Status{}, errors.New("release repair backup unavailable")
			}
			if removeErr := os.Remove(backup); removeErr != nil {
				_ = os.RemoveAll(staging)
				return Status{}, errors.New("release repair backup unavailable")
			}
			if renameErr := os.Rename(target, backup); renameErr != nil {
				_ = os.RemoveAll(staging)
				return Status{}, errors.New("invalid release backup failed")
			}
			if renameErr := os.Rename(staging, target); renameErr != nil {
				_ = os.Rename(backup, target)
				_ = os.RemoveAll(staging)
				return Status{}, errors.New("release repair activation failed")
			}
			replacedBackup = backup
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		staging, err := stageSignedRelease(source, releases, expected, e.PublicKey)
		if err != nil {
			return Status{}, err
		}
		if err := os.Rename(staging, target); err != nil {
			_ = os.RemoveAll(staging)
			return Status{}, errors.New("release activation staging failed")
		}
	} else {
		return Status{}, errors.New("release path unavailable")
	}
	restoreReplaced := func() {
		if replacedBackup == "" {
			return
		}
		_ = os.RemoveAll(target)
		_ = os.Rename(replacedBackup, target)
		replacedBackup = ""
	}

	oldTarget, _ := os.Readlink(filepath.Join(root, CurrentLink))
	if !replaceInvalidActive {
		if oldTarget != "" {
			if err := atomicLink(root, PreviousLink, oldTarget); err != nil {
				return Status{}, err
			}
		}
		if err := atomicLink(root, CurrentLink, desiredTarget); err != nil {
			return Status{}, err
		}
	}
	if e.Service == nil || e.Service.InstallUnit(target) != nil {
		_ = restoreLink(root, oldTarget)
		restoreReplaced()
		if oldTarget != "" && e.Service != nil {
			_ = e.Service.InstallUnit(filepath.Join(root, oldTarget))
		}
		return Status{}, errors.New("edge unit activation failed; previous bundle restored")
	}
	if e.Service == nil || e.Service.RestartEdge() != nil {
		_ = restoreLink(root, oldTarget)
		restoreReplaced()
		if oldTarget != "" && e.Service != nil {
			_ = e.Service.InstallUnit(filepath.Join(root, oldTarget))
			_ = e.Service.RestartEdge()
		}
		return Status{}, errors.New("edge restart failed; previous bundle restored")
	}
	if !e.Service.EdgeHealthy() {
		_ = restoreLink(root, oldTarget)
		restoreReplaced()
		if oldTarget != "" {
			_ = e.Service.InstallUnit(filepath.Join(root, oldTarget))
		}
		_ = e.Service.RestartEdge()
		return Status{}, ErrHealthCheck
	}
	status, err := statusFromLinks(root, e.Service)
	if err == nil {
		if replacedBackup != "" {
			_ = os.RemoveAll(replacedBackup)
		}
		_ = pruneOldReleases(root, e.PublicKey)
	}
	return status, err
}

func stageSignedRelease(source, releases string, expected bundle.Compatibility, publicKey ed25519.PublicKey) (string, error) {
	staging, err := os.MkdirTemp(releases, ".staging-"+expected.Release+"-")
	if err != nil {
		return "", errors.New("release staging unavailable")
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		_ = os.RemoveAll(staging)
		return "", errors.New("release staging permissions failed")
	}
	if err := copySignedRelease(source, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	if _, err := bundle.LoadAndVerify(staging, publicKey, expected); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	return staging, nil
}

func (e Engine) Rollback() (Status, error) {
	root, err := e.validRoot()
	if err != nil {
		return Status{}, err
	}
	unlock, err := lockUpdater(root)
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	current, err := os.Readlink(filepath.Join(root, CurrentLink))
	if err != nil {
		return Status{}, errors.New("current signed release is unavailable")
	}
	previous, err := os.Readlink(filepath.Join(root, PreviousLink))
	if err != nil {
		return Status{}, errors.New("previous signed release is unavailable")
	}
	if err := validateReleaseLink(root, previous); err != nil {
		return Status{}, err
	}
	if _, err := bundle.LoadTrusted(filepath.Join(root, previous), e.PublicKey); err != nil {
		return Status{}, err
	}
	if err := atomicLink(root, CurrentLink, previous); err != nil {
		return Status{}, err
	}
	if err := atomicLink(root, PreviousLink, current); err != nil {
		_ = atomicLink(root, CurrentLink, current)
		return Status{}, err
	}
	if e.Service == nil || e.Service.InstallUnit(filepath.Join(root, previous)) != nil {
		_ = atomicLink(root, CurrentLink, current)
		_ = atomicLink(root, PreviousLink, previous)
		return Status{}, errors.New("edge unit rollback failed")
	}
	if e.Service == nil || e.Service.RestartEdge() != nil || !e.Service.EdgeHealthy() {
		_ = atomicLink(root, CurrentLink, current)
		_ = atomicLink(root, PreviousLink, previous)
		if e.Service != nil {
			_ = e.Service.InstallUnit(filepath.Join(root, current))
			_ = e.Service.RestartEdge()
		}
		return Status{}, ErrHealthCheck
	}
	return statusFromLinks(root, e.Service)
}

func (e Engine) Status() (Status, error) {
	root, err := e.validRoot()
	if err != nil {
		return Status{}, err
	}
	return statusFromLinks(root, e.Service)
}

func (e Engine) validRoot() (string, error) {
	root := filepath.Clean(strings.TrimSpace(e.Root))
	if !filepath.IsAbs(root) || root == string(os.PathSeparator) || len(e.PublicKey) != ed25519.PublicKeySize {
		return "", errors.New("bundle updater configuration is invalid")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", errors.New("bundle updater root is unavailable")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("bundle updater root is unsafe")
	}
	return root, nil
}

func copySignedRelease(source, destination string) error {
	files := bundle.DefaultLayout()
	files[bundle.ManifestFile] = bundle.ManifestFile
	files[bundle.SignatureFile] = bundle.SignatureFile
	for _, relative := range files {
		sourcePath := filepath.Join(source, filepath.FromSlash(relative))
		info, err := os.Lstat(sourcePath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("signed release contains an unsafe component")
		}
		destinationPath := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return errors.New("release staging directory failed")
		}
		mode := os.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		if err := copyFile(sourcePath, destinationPath, mode); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return errors.New("release component unavailable")
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return errors.New("release component staging failed")
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return errors.New("release component staging failed")
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return errors.New("release component staging failed")
	}
	return output.Close()
}

func atomicLink(root, name, target string) error {
	if err := validateReleaseLink(root, target); err != nil {
		return err
	}
	temporary := filepath.Join(root, "."+name+".next")
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return errors.New("release link staging failed")
	}
	if err := os.Rename(temporary, filepath.Join(root, name)); err != nil {
		_ = os.Remove(temporary)
		return errors.New("release link activation failed")
	}
	return nil
}

func restoreLink(root, target string) error {
	if target == "" {
		return os.Remove(filepath.Join(root, CurrentLink))
	}
	return atomicLink(root, CurrentLink, target)
}

func validateReleaseLink(root, target string) error {
	clean := filepath.Clean(target)
	if filepath.IsAbs(clean) || clean == ReleasesDirectory || filepath.Dir(clean) != ReleasesDirectory || strings.Contains(filepath.Base(clean), string(os.PathSeparator)) {
		return errors.New("release link target is invalid")
	}
	path := filepath.Join(root, clean)
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("signed release target is unavailable")
	}
	return nil
}

func statusFromLinks(root string, service Service) (Status, error) {
	status := Status{}
	current, err := os.Readlink(filepath.Join(root, CurrentLink))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, errors.New("current release link is unsafe")
	}
	if current != "" {
		if err := validateReleaseLink(root, current); err != nil {
			return Status{}, err
		}
		status.Release = filepath.Base(current)
	}
	previous, err := os.Readlink(filepath.Join(root, PreviousLink))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, errors.New("previous release link is unsafe")
	}
	if previous != "" {
		if err := validateReleaseLink(root, previous); err != nil {
			return Status{}, err
		}
		status.PreviousRelease = filepath.Base(previous)
	}
	status.ServiceActive = service != nil && service.EdgeHealthy()
	return status, nil
}

func lockUpdater(root string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(root, updateLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("bundle updater lock unavailable")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, errors.New("bundle updater lock unavailable")
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func pruneOldReleases(root string, publicKey ed25519.PublicKey) error {
	status, err := statusFromLinks(root, nil)
	if err != nil {
		return err
	}
	keep := map[string]struct{}{status.Release: {}, status.PreviousRelease: {}}
	delete(keep, "")
	entries, err := os.ReadDir(filepath.Join(root, ReleasesDirectory))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !bundle.ValidRelease(entry.Name()) {
			continue
		}
		if _, retained := keep[entry.Name()]; retained {
			continue
		}
		path := filepath.Join(root, ReleasesDirectory, entry.Name())
		if _, err := bundle.LoadTrusted(path, publicKey); err != nil {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func (s Status) String() string {
	return fmt.Sprintf("release=%s previous=%s active=%t", s.Release, s.PreviousRelease, s.ServiceActive)
}
