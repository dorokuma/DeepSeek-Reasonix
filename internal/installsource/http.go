package installsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultFetchTimeout caps the lifetime of a single HTTP fetch. Without it a
// slow CDN can hold the agent tool call open until the user gives up. The
// value is generous (30s) so large SKILL.md bodies still load, but bounded so
// a hung server is not an open-ended wait.
const defaultFetchTimeout = 30 * time.Second

// defaultFetchLimit is the maximum body size we will accept from a remote
// manifest. SKILL.md / .mcp.json files are normally a few KB; 2 MiB is a
// safety cap that prevents an untrusted mirror from streaming gigabytes into
// our parser.
const defaultFetchLimit = 2 << 20

// fetchText performs a bounded GET on sourceURL using the tool's HTTP client.
// It applies defaultFetchTimeout unless the caller's context already has a
// tighter deadline, and never reads more than defaultFetchLimit bytes.
func (t *installSourceTool) fetchText(ctx context.Context, sourceURL string) (string, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultFetchTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", newErr(ErrSourceUnreadable, "%s: %v", sourceURL, err)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "reasonix-install/1.0")
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", newErr(ErrSourceUnreadable, "%s: %v", sourceURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", newErr(ErrAuthRequired, "%s: HTTP %d", sourceURL, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newErr(ErrSourceUnreadable, "%s: HTTP %d", sourceURL, resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, defaultFetchLimit)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", newErr(ErrSourceUnreadable, "%s: read body: %v", sourceURL, err)
	}
	return string(body), nil
}

// verifyContent checks that body matches the expected sha256 hash.
// expectedHash must be in "sha256:<hex>" format. An empty expectedHash
// skips verification (no-op). Returns nil on match, error on mismatch
// or bad format.
func verifyContent(body string, expectedHash string) error {
	if expectedHash == "" {
		return nil
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(expectedHash, prefix) {
		return fmt.Errorf("invalid hash format %q: must be sha256:<hex>", expectedHash)
	}
	expected, err := hex.DecodeString(expectedHash[len(prefix):])
	if err != nil {
		return fmt.Errorf("invalid hash hex in %q: %v", expectedHash, err)
	}
	if len(expected) != sha256.Size {
		return fmt.Errorf("invalid hash length in %q: got %d bytes, want %d", expectedHash, len(expected), sha256.Size)
	}
	h := sha256.Sum256([]byte(body))
	got := hex.EncodeToString(h[:])
	if got != hex.EncodeToString(expected) {
		return newErr(ErrInvalidManifest, "content hash mismatch: expected %s, got sha256:%s", expectedHash, got)
	}
	return nil
}
