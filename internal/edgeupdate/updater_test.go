//go:build !windows

package edgeupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/charle-z/mcp-devbox/internal/buildinfo"
	"github.com/charle-z/mcp-devbox/internal/bundle"
)

func TestOfficialReleaseBaseUsesAeontraRepository(t *testing.T) {
	if OfficialBaseURL != "https://github.com/charle-z/aeontra/releases/download" {
		t.Fatalf("official release base URL=%q", OfficialBaseURL)
	}
}

type officialRoundTripper struct{ channel, signature []byte }

func (r officialRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	body := r.channel
	if filepath.Ext(request.URL.Path) == ".sig" {
		body = r.signature
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
}

func TestStableAvailableUsesOnlySignedOfficialChannel(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	channel := bundle.Channel{Version: 1, Release: "p15.9.0", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProtocolVersion: buildinfo.EdgeBundleProtocolVersion, CatalogHash: "sha256:" + string(bytes.Repeat([]byte{'b'}, 64)), Architecture: runtime.GOARCH, ArchiveHash: "sha256:" + string(bytes.Repeat([]byte{'c'}, 64))}
	body, signature, err := bundle.SignChannel(channel, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	resolver := OfficialResolver{PublicKey: publicKey, Client: &http.Client{Transport: officialRoundTripper{channel: body, signature: signature}}}
	available, err := resolver.StableAvailable(context.Background(), "p15.8.0")
	if err != nil || !available {
		t.Fatalf("available=%t err=%v", available, err)
	}
	available, err = resolver.StableAvailable(context.Background(), "p15.9.0")
	if err != nil || available {
		t.Fatalf("current available=%t err=%v", available, err)
	}
	if _, err := resolver.StableAvailable(context.Background(), "p15.10.0"); err == nil {
		t.Fatal("signed stable-channel downgrade accepted")
	}
	resolver.Client = &http.Client{Transport: officialRoundTripper{channel: body, signature: bytes.Repeat([]byte{0}, ed25519.SignatureSize)}}
	if _, err := resolver.StableAvailable(context.Background(), "p15.8.0"); err == nil {
		t.Fatal("tampered channel accepted")
	}
}

func TestStableAvailableRejectsSignedIncompatibleProtocol(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	channel := bundle.Channel{Version: 1, Release: "p15.9.1", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProtocolVersion: "mcp-devbox.edge-bundle.v2", CatalogHash: "sha256:" + string(bytes.Repeat([]byte{'b'}, 64)), Architecture: runtime.GOARCH, ArchiveHash: "sha256:" + string(bytes.Repeat([]byte{'c'}, 64))}
	body, signature, err := bundle.SignChannel(channel, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	resolver := OfficialResolver{PublicKey: publicKey, Client: &http.Client{Transport: officialRoundTripper{channel: body, signature: signature}}}
	if _, err := resolver.StableAvailable(context.Background(), "p15.9.0"); err == nil {
		t.Fatal("signed incompatible channel accepted")
	}
}

func TestOfficialArchiveDestinationRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../manifest.json", "/manifest.json", "."} {
		if path, err := officialArchiveDestination(root, name); err == nil {
			t.Fatalf("unsafe archive path accepted: name=%q path=%q", name, path)
		}
	}
	path, err := officialArchiveDestination(root, "opencode-provider/index.js")
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.ToSlash(relative) != "opencode-provider/index.js" {
		t.Fatalf("safe archive path resolved incorrectly: path=%q relative=%q err=%v", path, relative, err)
	}
}

type fakeService struct {
	restarts int
	healthy  bool
}

func (s *fakeService) InstallUnit(string) error { return nil }
func (s *fakeService) RestartEdge() error       { s.restarts++; return nil }
func (s *fakeService) EdgeHealthy() bool        { return s.healthy }

func TestUpdaterInstallsAtomicallyIdempotentlyAndRollsBack(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeService{healthy: true}
	engine := Engine{Root: root, PublicKey: publicKey, Service: service}

	firstSource, firstCompatibility := signedRelease(t, "p15.0.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", privateKey)
	if status, err := engine.Install(firstSource, firstCompatibility); err != nil || status.Release != "p15.0.0" || status.PreviousRelease != "" {
		t.Fatalf("first install = %+v, %v", status, err)
	}
	assertCurrentRelease(t, root, "p15.0.0")
	releaseInfo, err := os.Stat(filepath.Join(root, ReleasesDirectory, "p15.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if releaseInfo.Mode().Perm() != 0o755 {
		t.Fatalf("installed release root mode = %v; want 0755", releaseInfo.Mode().Perm())
	}

	if status, err := engine.Install(firstSource, firstCompatibility); err != nil || status.Release != "p15.0.0" || service.restarts != 1 {
		t.Fatalf("idempotent install = %+v, %v, restarts=%d", status, err, service.restarts)
	}

	secondSource, secondCompatibility := signedRelease(t, "p15.0.1", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", privateKey)
	if status, err := engine.Install(secondSource, secondCompatibility); err != nil || status.Release != "p15.0.1" || status.PreviousRelease != "p15.0.0" {
		t.Fatalf("upgrade = %+v, %v", status, err)
	}
	assertCurrentRelease(t, root, "p15.0.1")

	if status, err := engine.Rollback(); err != nil || status.Release != "p15.0.0" || status.PreviousRelease != "p15.0.1" {
		t.Fatalf("rollback = %+v, %v", status, err)
	}
	assertCurrentRelease(t, root, "p15.0.0")
}

func TestUpdaterTransitionsFromLegacyReleaseToSemanticReleaseAndRollsBack(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeService{healthy: true}
	engine := Engine{Root: root, PublicKey: publicKey, Service: service}

	bridgeSource, bridgeCompatibility := signedRelease(t, "p15.0.45", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", privateKey)
	if _, err := engine.Install(bridgeSource, bridgeCompatibility); err != nil {
		t.Fatal(err)
	}
	stableSource, stableCompatibility := signedRelease(t, "v1.0.0", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", privateKey)
	status, err := engine.Install(stableSource, stableCompatibility)
	if err != nil || status.Release != "v1.0.0" || status.PreviousRelease != "p15.0.45" {
		t.Fatalf("semantic upgrade = %+v, %v", status, err)
	}
	assertCurrentRelease(t, root, "v1.0.0")

	status, err = engine.Rollback()
	if err != nil || status.Release != "p15.0.45" || status.PreviousRelease != "v1.0.0" {
		t.Fatalf("legacy rollback = %+v, %v", status, err)
	}
	assertCurrentRelease(t, root, "p15.0.45")
}

func TestUpdaterRejectsImplicitDowngradeButKeepsExplicitRollback(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	root := t.TempDir()
	service := &fakeService{healthy: true}
	engine := Engine{Root: root, PublicKey: publicKey, Service: service}

	olderSource, olderCompatibility := signedRelease(t, "v1.2.8", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", privateKey)
	newerSource, newerCompatibility := signedRelease(t, "v1.2.9", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", privateKey)
	if _, err := engine.Install(olderSource, olderCompatibility); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Install(newerSource, newerCompatibility); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Install(olderSource, olderCompatibility); err == nil {
		t.Fatal("implicit signed downgrade was accepted")
	}
	status, err := engine.Rollback()
	if err != nil || status.Release != olderCompatibility.Release {
		t.Fatalf("explicit rollback failed: status=%+v err=%v", status, err)
	}
}

func TestUpdaterRestoresPreviousReleaseWhenHealthCheckFails(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeService{healthy: true}
	engine := Engine{Root: root, PublicKey: publicKey, Service: service}
	firstSource, firstCompatibility := signedRelease(t, "p15.1.0", "cccccccccccccccccccccccccccccccccccccccc", privateKey)
	if _, err := engine.Install(firstSource, firstCompatibility); err != nil {
		t.Fatal(err)
	}
	service.healthy = false
	secondSource, secondCompatibility := signedRelease(t, "p15.1.1", "dddddddddddddddddddddddddddddddddddddddd", privateKey)
	if _, err := engine.Install(secondSource, secondCompatibility); !errors.Is(err, ErrHealthCheck) {
		t.Fatalf("got %v, want health failure", err)
	}
	assertCurrentRelease(t, root, "p15.1.0")
}

func TestUpdaterRepairsCorruptActiveReleaseAndRestoresItOnHealthFailure(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeService{healthy: true}
	engine := Engine{Root: root, PublicKey: publicKey, Service: service}
	previousSource, previousCompatibility := signedRelease(t, "p15.1.1", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", privateKey)
	if _, err := engine.Install(previousSource, previousCompatibility); err != nil {
		t.Fatal(err)
	}
	source, compatibility := signedRelease(t, "p15.1.2", "ffffffffffffffffffffffffffffffffffffffff", privateKey)
	if _, err := engine.Install(source, compatibility); err != nil {
		t.Fatal(err)
	}
	activeComponent := filepath.Join(root, ReleasesDirectory, compatibility.Release, "codex", "codex")
	if err := os.WriteFile(activeComponent, []byte("corrupt-active"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.healthy = false
	if _, err := engine.Install(source, compatibility); !errors.Is(err, ErrHealthCheck) {
		t.Fatalf("repair health failure = %v", err)
	}
	content, err := os.ReadFile(activeComponent)
	if err != nil || string(content) != "corrupt-active" {
		t.Fatalf("failed repair did not restore prior active content: %q %v", content, err)
	}
	service.healthy = true
	if _, err := engine.Install(source, compatibility); err != nil {
		t.Fatalf("repair active release: %v", err)
	}
	if _, err := bundle.LoadAndVerify(filepath.Join(root, ReleasesDirectory, compatibility.Release), publicKey, compatibility); err != nil {
		t.Fatalf("repaired active release did not verify: %v", err)
	}
	status, err := engine.Status()
	if err != nil || status.PreviousRelease != previousCompatibility.Release {
		t.Fatalf("repair changed previous release: %+v %v", status, err)
	}
	if err := os.RemoveAll(filepath.Join(root, ReleasesDirectory, compatibility.Release)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Install(source, compatibility); err != nil {
		t.Fatalf("repair missing active release: %v", err)
	}
	status, err = engine.Status()
	if err != nil || status.Release != compatibility.Release || status.PreviousRelease != previousCompatibility.Release {
		t.Fatalf("missing-release repair changed links: %+v %v", status, err)
	}
}

func TestUpdaterRejectsUnsignedOrCallerMixedBundleBeforeActivation(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeService{healthy: true}
	engine := Engine{Root: root, PublicKey: publicKey, Service: service}
	source, compatibility := signedRelease(t, "p15.2.0", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", privateKey)
	if err := os.WriteFile(filepath.Join(source, "codex", "codex"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Install(source, compatibility); err == nil {
		t.Fatal("tampered bundle was activated")
	}
	if _, err := os.Lstat(filepath.Join(root, CurrentLink)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current link exists after rejected install: %v", err)
	}
}

func TestUpdaterPrunesEveryTrustedReleaseExceptCurrentAndPrevious(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeService{healthy: true}
	engine := Engine{Root: root, PublicKey: publicKey, Service: service}

	firstSource, firstCompatibility := signedRelease(t, "v1.2.1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", privateKey)
	if _, err := engine.Install(firstSource, firstCompatibility); err != nil {
		t.Fatal(err)
	}
	secondSource, secondCompatibility := signedRelease(t, "v1.2.2", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", privateKey)
	if _, err := engine.Install(secondSource, secondCompatibility); err != nil {
		t.Fatal(err)
	}
	untrusted := filepath.Join(root, ReleasesDirectory, "v1.2.0")
	if err := os.MkdirAll(untrusted, 0o700); err != nil {
		t.Fatal(err)
	}
	thirdSource, thirdCompatibility := signedRelease(t, "v1.2.3", "cccccccccccccccccccccccccccccccccccccccc", privateKey)
	if _, err := engine.Install(thirdSource, thirdCompatibility); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(root, ReleasesDirectory))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	want := []string{"v1.2.0", "v1.2.2", "v1.2.3"}
	if !slices.Equal(got, want) {
		t.Fatalf("retained releases = %v; want %v", got, want)
	}
}

func signedRelease(t *testing.T, release, commit string, privateKey ed25519.PrivateKey) (string, bundle.Compatibility) {
	t.Helper()
	root := t.TempDir()
	for component, relative := range bundle.DefaultLayout() {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(release+":"+component), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	metadata := bundle.Metadata{
		Release: release, Commit: commit, ProtocolVersion: buildinfo.EdgeBundleProtocolVersion,
		CatalogHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Architecture: "amd64",
	}
	manifest, err := bundle.Build(root, metadata)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := bundle.Sign(manifest, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, bundle.ManifestFile), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, bundle.SignatureFile), signature, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, bundle.Compatibility{
		Release: release, Commit: commit, ProtocolVersion: metadata.ProtocolVersion,
		CatalogHash: metadata.CatalogHash, Architecture: metadata.Architecture,
	}
}

func assertCurrentRelease(t *testing.T, root, want string) {
	t.Helper()
	target, err := os.Readlink(filepath.Join(root, CurrentLink))
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(ReleasesDirectory, want) {
		t.Fatalf("current target = %q, want %q", target, filepath.Join(ReleasesDirectory, want))
	}
}
