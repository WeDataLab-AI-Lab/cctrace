package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/syncer"

	"github.com/inconshreveable/go-update"
)

// updatePublicKeyBase64 is the independent trust root for self-update manifests.
// Release builds must set it with:
//
//	-ldflags "-X main.updatePublicKeyBase64=<base64-encoded 32-byte Ed25519 public key>"
//
// The matching private key is used only through the deploy/Dockerfile BuildKit
// secret update_signing_key and must never be copied into the build context or
// persisted in an image layer.
var updatePublicKeyBase64 string

// applyUpdateIfAvailable checks the server version and self-updates if a newer
// version is available. Prints a message and exits 0 after a successful update
// so the caller can re-run with the new binary.
func applyUpdateIfAvailable(ctx context.Context, client *syncer.Client, endpoint string, profileName string) {
	serverVer, err := client.CheckVersion(ctx)
	if err != nil {
		// Version endpoint unreachable — silently skip update.
		return
	}

	if serverVer == "" || serverVer == "dev" {
		return
	}

	if !semverGT(serverVer, version) {
		clearUpdateStall(profileName)
		return
	}

	// This leg looked manual and is not: the SessionEnd hook is
	// `cctrace sync --daemon --once`, and the child it spawns runs `sync --once`,
	// which lands here. Left ungated it refetched the artifact on every session
	// end while the daemon leg was already backing off (#623).
	//
	// syncLogToFile is the seam that tells the two apart. Both hook commands carry
	// --log-to-file and spawnSyncProcess sets it on every child, while a person
	// typing `cctrace sync` never does -- the flag is hidden. So an automated run
	// waits and a deliberate one still forces the retry, which is what somebody
	// who has just repaired their install needs.
	now := updateClock()
	if syncLogToFile && !updateStallReady(profileName, serverVer, now) {
		return
	}

	fmt.Printf("  Updating cctrace %s → %s ...\n", version, serverVer)
	if err := downloadAndApplyUpdate(ctx, endpoint, serverVer); err != nil {
		// diagf, not os.Stderr: `sync --once` runs as a spawned child whose stderr
		// is sync-crash.log, a file reset when it outgrows its cap and not the one
		// anybody reads. Routing through the log package puts the failure in
		// sync.log, which is the difference between "the update never applied" and
		// "the update applied but the resident child kept the old build" (#458).
		diagf("update: %v", err)
		// Recorded even when this leg is not gated, so `cctrace status` counts every
		// attempt rather than only the daemon leg's share of them.
		noteUpdateFailure(profileName, serverVer, err.Error(), now)
		return
	}

	// Before the exit below, or a record of a failure this run just resolved
	// outlives it -- and that record is what the next investigation reads.
	clearUpdateStall(profileName)
	fmt.Printf("  Updated to %s. Please re-run: cctrace sync\n", serverVer)
	os.Exit(0)
}

// downloadAndApplyUpdate downloads the platform binary from the server's
// /downloads endpoint and atomically replaces the running executable in place.
// It does NOT exit — the caller decides whether to re-exec (one-shot) or respawn
// (daemon parent, #103).
func downloadAndApplyUpdate(ctx context.Context, endpoint, serverVersion string) error {
	return downloadAndApplyUpdateWith(ctx, http.DefaultClient, endpoint, applyUpdate, updatePublicKeyBase64, serverVersion)
}

type updateApplier func(io.Reader) error

type updateManifest struct {
	Filename  string `json:"filename"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
	// Version and SignatureV2 bind a manifest to one release.
	//
	// Signature covers filename and checksum, which identify an artifact but not
	// which release it belongs to. Whoever controls the update response can
	// therefore advertise a new version and serve a genuinely signed older
	// artifact: every check passes and the client downgrades itself (#561).
	Version     string `json:"version"`
	SignatureV2 string `json:"signature_v2"`
}

func downloadAndApplyUpdateWith(
	ctx context.Context,
	httpClient *http.Client,
	endpoint string,
	apply updateApplier,
	publicKeyBase64 string,
	serverVersion string,
) error {
	if err := validateUpdateEndpoint(endpoint); err != nil {
		return err
	}
	publicKeys, err := parseUpdatePublicKeys(publicKeyBase64)
	if err != nil {
		return err
	}

	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	filename := fmt.Sprintf("cctrace-%s-%s%s", runtime.GOOS, runtime.GOARCH, suffix)
	downloadURL := fmt.Sprintf("%s/downloads/%s", strings.TrimRight(endpoint, "/"), filename)

	dlCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	expectedSum, err := downloadAndVerifyManifest(
		dlCtx,
		httpClient,
		downloadURL+".manifest.json",
		filename,
		publicKeys,
		serverVersion,
	)
	if err != nil {
		return err
	}

	resp, err := downloadUpdateResponse(dlCtx, httpClient, downloadURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	staged, err := os.CreateTemp("", "cctrace-update-*")
	if err != nil {
		return fmt.Errorf("stage update: %w", err)
	}
	stagedName := staged.Name()
	defer os.Remove(stagedName)
	defer staged.Close()

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(staged, hasher), resp.Body); err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	actualSum := hasher.Sum(nil)
	if !bytes.Equal(actualSum, expectedSum) {
		return fmt.Errorf("checksum mismatch for %s: got %x, want %x", downloadURL, actualSum, expectedSum)
	}
	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("prepare verified update: %w", err)
	}

	if err := apply(staged); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	return nil
}

// parseUpdatePublicKeys accepts a comma-separated list so a signing key can be
// rotated without stranding clients. With one key there is no transition: the
// moment releases are signed with a new key, every already-installed client
// rejects them and needs a manual reinstall. Shipping the incoming key alongside
// the outgoing one first gives a window where both verify, so the switch lands
// only after clients carry the new key.
//
// Order carries no meaning — a manifest is accepted if any listed key verifies it.
func parseUpdatePublicKeys(encoded string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, field := range strings.Split(encoded, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(field)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid update public key %q: expected base64-encoded %d-byte Ed25519 key", field, ed25519.PublicKeySize)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("update public key is not configured; rebuild with main.updatePublicKeyBase64")
	}
	return keys, nil
}

// allowInsecureEndpointEnv opts out of the transport check for deployments this
// client cannot recognise as internal — a routable address that is nonetheless on
// a private network, or a scheme-bearing proxy in front of the server.
//
// It relaxes transport only. Signature and checksum verification stay on: those
// are what keep a forged binary from being installed, and plain HTTP does not
// weaken them. There is deliberately no switch for those — self-update executes
// whatever it downloads, and this client writes its own env into settings.json,
// so a verification kill switch would sit on a surface the tool itself rewrites.
const allowInsecureEndpointEnv = "CCTRACE_UPDATE_ALLOW_INSECURE_ENDPOINT"

func insecureEndpointAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(allowInsecureEndpointEnv))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func validateUpdateEndpoint(endpoint string) error {
	if err := validateUpdateEndpointStrict(endpoint); err != nil {
		if !insecureEndpointAllowed() {
			return err
		}
		// Warned every time rather than once: the override weakens a default, and a
		// machine running with it should say so in the log that gets pasted into a
		// bug report. That requires diagf, not raw stderr: both callers run inside
		// the update path, which the hook-spawned daemon parent and its
		// --log-to-file child reach with the log installed and their stderr
		// discarded. diagf still writes the same bytes to stderr for an
		// interactive run, so the warning loses no visibility.
		diagf("update: %s=1 — transport check skipped for %s (signature verification still enforced)",
			allowInsecureEndpointEnv, endpoint)
		return nil
	}
	return nil
}

func validateUpdateEndpointStrict(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid update endpoint %q: %w", endpoint, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		// Loopback and private-range literals stay on HTTP. What plain HTTP exposes
		// is tampering and impersonation, and the signed manifest already covers
		// both — a forged binary fails verification whatever the transport. HTTPS
		// would add confidentiality, which a published client binary does not need.
		//
		// Restricting this to IP literals is deliberate: a hostname would extend the
		// exemption to whatever DNS happens to resolve to, which is not something the
		// client can reason about.
		if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
			return nil
		}
		return fmt.Errorf("update endpoint %q must use HTTPS; HTTP is allowed only for loopback or private-range addresses", endpoint)
	default:
		return fmt.Errorf("update endpoint %q must use HTTPS; unsupported scheme %q", endpoint, parsed.Scheme)
	}
}

func downloadAndVerifyManifest(
	ctx context.Context,
	client *http.Client,
	manifestURL string,
	expectedFilename string,
	publicKeys []ed25519.PublicKey,
	serverVersion string,
) ([]byte, error) {
	resp, err := downloadUpdateResponse(ctx, client, manifestURL)
	if err != nil {
		return nil, fmt.Errorf("download update manifest: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return nil, fmt.Errorf("read update manifest: %w", err)
	}
	if len(raw) > 4096 {
		return nil, fmt.Errorf("invalid update manifest from %s: response exceeds 4096 bytes", manifestURL)
	}

	var manifest updateManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("invalid update manifest from %s: %w", manifestURL, err)
	}
	if manifest.Filename != expectedFilename {
		return nil, fmt.Errorf("invalid update manifest filename %q, want %q", manifest.Filename, expectedFilename)
	}
	sum, err := hex.DecodeString(manifest.SHA256)
	if err != nil || len(sum) != sha256.Size {
		return nil, fmt.Errorf("invalid SHA-256 checksum in update manifest from %s", manifestURL)
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid Ed25519 signature in update manifest from %s", manifestURL)
	}
	if !verifyManifestSignature(publicKeys, manifest, signature) {
		return nil, fmt.Errorf("update manifest signature verification failed for %s", manifestURL)
	}

	// The v1 signature above says "this artifact is genuine". It does not say
	// which release it is, so a genuine older artifact served under a newer
	// advertised version passes everything up to here (#561).
	//
	// Required, not accepted-when-present: an attacker who can serve an old
	// manifest can also serve one with these fields removed, so treating their
	// absence as "no opinion" leaves the hole open. Artifacts and their manifests
	// are produced together by the release build, so a server offering a version
	// this client would upgrade to is offering one signed by a build that emits
	// them.
	if manifest.Version == "" || manifest.SignatureV2 == "" {
		return nil, fmt.Errorf("update manifest from %s does not bind a version; refusing to install an artifact that could belong to any release", manifestURL)
	}
	signatureV2, err := base64.StdEncoding.DecodeString(manifest.SignatureV2)
	if err != nil || len(signatureV2) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid version-bound signature in update manifest from %s", manifestURL)
	}
	if !verifyManifestSignatureV2(publicKeys, manifest, signatureV2) {
		return nil, fmt.Errorf("version-bound signature verification failed for %s", manifestURL)
	}
	// The signature proves the version was signed; this proves it is the version
	// the client was told it was getting. Without it a signed older release is
	// still installable by advertising itself honestly -- the caller's
	// semverGT check is what makes that a no-op, and this keeps the two in step.
	if serverVersion != "" && manifest.Version != serverVersion {
		return nil, fmt.Errorf("update manifest from %s is signed for %s but the server advertised %s", manifestURL, manifest.Version, serverVersion)
	}

	return sum, nil
}

// verifyManifestSignatureV2 verifies the version-bound message. Separate from
// verifyManifestSignature because the two cover different byte strings: a v1
// signature can never satisfy this check, which is the point.
func verifyManifestSignatureV2(publicKeys []ed25519.PublicKey, manifest updateManifest, signature []byte) bool {
	message := updateManifestMessageV2(manifest.Filename, manifest.SHA256, manifest.Version)
	for _, key := range publicKeys {
		if ed25519.Verify(key, message, signature) {
			return true
		}
	}
	return false
}

// verifyManifestSignature accepts the manifest if any configured key verifies it,
// which is what makes a rotation window possible.
func verifyManifestSignature(publicKeys []ed25519.PublicKey, manifest updateManifest, signature []byte) bool {
	message := updateManifestMessage(manifest.Filename, manifest.SHA256)
	for _, key := range publicKeys {
		if ed25519.Verify(key, message, signature) {
			return true
		}
	}
	return false
}

func updateManifestMessage(filename, checksum string) []byte {
	return []byte(filename + "\n" + checksum + "\n")
}

// updateManifestMessageV2 must stay byte-identical to manifestMessageV2 in
// scripts/sign-update-manifest.go. They are the two halves of one contract, and
// a disagreement makes every release uninstallable.
func updateManifestMessageV2(filename, checksum, version string) []byte {
	return []byte(filename + "\n" + checksum + "\n" + version + "\n")
}

func downloadUpdateResponse(ctx context.Context, client *http.Client, downloadURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", downloadURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", downloadURL, err)
	}
	if resp.Request != nil {
		if err := validateUpdateEndpoint(resp.Request.URL.String()); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("insecure update redirect: %w", err)
		}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("server returned %d for %s", resp.StatusCode, downloadURL)
	}
	return resp, nil
}

func applyUpdate(r io.Reader) error {
	return update.Apply(r, update.Options{})
}

// semverGT returns true when a > b using simple vMAJOR.MINOR.PATCH comparison.
// Non-semver strings (e.g. "dev") return false.
func semverGT(a, b string) bool {
	pa := parseSemver(a)
	pb := parseSemver(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := range pa {
		if pa[i] > pb[i] {
			return true
		}
		if pa[i] < pb[i] {
			return false
		}
	}
	return false
}

func parseSemver(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	result := make([]int, 3)
	for i, p := range parts {
		// strip pre-release suffix (e.g. "1-rc1" → "1")
		p = strings.SplitN(p, "-", 2)[0]
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		result[i] = n
	}
	return result
}

// updateTargetWritable reports whether a self-update could write its
// replacement binary.
//
// The updater creates its temporary file NEXT TO the running executable, so the
// directory is what must be writable -- not the binary. The distinction is not
// academic: a user answered "y" to `mv`'s "override rwxr-xr-x root/wheel?" and
// still got Permission denied, because /usr/local/bin is root-owned while the
// file inside it looked replaceable (#459).
//
// An unresolvable path returns true. This feeds a status line, and a false
// alarm there is worse than silence -- the updater reports its own failure with
// the real error when it actually tries.
func updateTargetWritable(executable string) bool {
	if executable == "" {
		return true
	}
	dir := filepath.Dir(executable)
	probe, err := os.CreateTemp(dir, ".cctrace-writable-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}
