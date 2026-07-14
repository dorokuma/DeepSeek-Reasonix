package repair

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/fileutil"
)

const updateTransactionVersion = 1

var repairExecutable = os.Executable

type UpdateTransaction struct {
	SchemaVersion int    `json:"schemaVersion"`
	FromVersion   string `json:"fromVersion,omitempty"`
	ToVersion     string `json:"toVersion"`
	Platform      string `json:"platform"`
	TargetKind    string `json:"targetKind"` // file | app-bundle
	TargetPath    string `json:"targetPath"`
	BackupPath    string `json:"backupPath"`
	BackupSHA256  string `json:"backupSha256,omitempty"`
	// Files lists every binary of the release unit the update replaces
	// (main executable first, then Guard/launcher siblings). Rollback must
	// restore all of them together: restoring only the main binary would
	// leave a mixed old-desktop/new-Guard install. Empty on transactions
	// recorded by kinds that back up a single unit (macOS app bundles).
	Files     []UpdateTransactionFile `json:"files,omitempty"`
	CreatedAt string                  `json:"createdAt"`
}

type UpdateTransactionFile struct {
	TargetPath string `json:"targetPath"`
	BackupPath string `json:"backupPath"`
	SHA256     string `json:"sha256,omitempty"`
}

type UpdateRollbackResult struct {
	RolledBack  bool   `json:"rolledBack"`
	FromVersion string `json:"fromVersion,omitempty"`
	ToVersion   string `json:"toVersion,omitempty"`
	TargetPath  string `json:"targetPath,omitempty"`
	// MixedInstall reports that a failed rollback could not be compensated:
	// the install now mixes binaries from two releases. Launchers must not
	// start the desktop in this state.
	MixedInstall bool `json:"mixedInstall,omitempty"`
}

func PendingUpdatePath() string {
	root := config.MemoryUserDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "repair", "pending-update.json")
}

// PrepareFileUpdate snapshots the current desktop executable — plus any sibling
// binaries of the release unit the installer also replaces (Guard, launcher,
// update helper) — and records an update transaction before an updater applies
// the replacement. Sibling paths that do not exist are skipped so older
// installs without those artifacts still update.
func PrepareFileUpdate(fromVersion, toVersion, targetPath string, siblingPaths ...string) (*UpdateTransaction, error) {
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if targetPath == "" || targetPath == "." {
		return nil, fmt.Errorf("prepare update: empty target path")
	}
	root := config.MemoryUserDir()
	if root == "" {
		return nil, fmt.Errorf("prepare update: Reasonix state directory is unavailable")
	}
	backupDir := filepath.Join(root, "repair", "updates")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return nil, err
	}
	tx := &UpdateTransaction{
		SchemaVersion: updateTransactionVersion,
		FromVersion:   fromVersion,
		ToVersion:     toVersion,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		TargetKind:    "file",
		TargetPath:    targetPath,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	seen := map[string]bool{}
	for i, path := range append([]string{targetPath}, siblingPaths...) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" || path == "." || seen[path] {
			continue
		}
		seen[path] = true
		if i > 0 {
			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("prepare update backup: %w", err)
			}
		}
		backupPath := filepath.Join(backupDir, filepath.Base(path)+".previous")
		hash, err := copyFileWithHash(path, backupPath, 0o700)
		if err != nil {
			return nil, fmt.Errorf("prepare update backup: %w", err)
		}
		tx.Files = append(tx.Files, UpdateTransactionFile{TargetPath: path, BackupPath: backupPath, SHA256: hash})
		if i == 0 {
			tx.BackupPath = backupPath
			tx.BackupSHA256 = hash
		}
	}
	if err := WritePendingUpdate(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// PrepareAppBundleUpdate records the sibling bundle backup that the macOS
// handoff script creates. The script performs the directory move after exit.
func PrepareAppBundleUpdate(fromVersion, toVersion, appPath, backupPath string) (*UpdateTransaction, error) {
	tx := &UpdateTransaction{
		SchemaVersion: updateTransactionVersion,
		FromVersion:   fromVersion,
		ToVersion:     toVersion,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		TargetKind:    "app-bundle",
		TargetPath:    filepath.Clean(strings.TrimSpace(appPath)),
		BackupPath:    filepath.Clean(strings.TrimSpace(backupPath)),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if !strings.HasSuffix(strings.ToLower(tx.TargetPath), ".app") || tx.BackupPath != tx.TargetPath+".reasonix-update-backup" {
		return nil, fmt.Errorf("prepare update: invalid macOS bundle paths")
	}
	if err := WritePendingUpdate(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

func WritePendingUpdate(tx *UpdateTransaction) error {
	if tx == nil {
		return fmt.Errorf("pending update: nil transaction")
	}
	path := PendingUpdatePath()
	if path == "" {
		return fmt.Errorf("pending update: Reasonix state directory is unavailable")
	}
	b, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(path, append(b, '\n'), 0o600)
}

func ReadPendingUpdate() (*UpdateTransaction, error) {
	path := PendingUpdatePath()
	if path == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tx UpdateTransaction
	if err := json.Unmarshal(b, &tx); err != nil {
		return nil, err
	}
	if err := validateUpdateTransaction(&tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

func HasPendingUpdate() bool {
	_, err := ReadPendingUpdate()
	return err == nil
}

// MarkUpdateHealthy commits a probationary update and removes its backup. A
// version mismatch is ignored so an older process cannot bless a newer update.
func MarkUpdateHealthy(runningVersion string) error {
	tx, err := ReadPendingUpdate()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(runningVersion) != strings.TrimSpace(tx.ToVersion) {
		return nil
	}
	if err := os.Remove(PendingUpdatePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	removeUpdateBackups(tx)
	return nil
}

// CancelPendingUpdate removes a transaction that failed before control was
// handed to the replacement build. A version mismatch is intentionally inert.
func CancelPendingUpdate(toVersion string) error {
	tx, err := ReadPendingUpdate()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(toVersion) != strings.TrimSpace(tx.ToVersion) {
		return nil
	}
	if err := os.Remove(PendingUpdatePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	removeUpdateBackups(tx)
	return nil
}

func removeUpdateBackups(tx *UpdateTransaction) {
	if tx == nil {
		return
	}
	if tx.TargetKind == "app-bundle" {
		if tx.BackupPath != "" {
			_ = os.RemoveAll(tx.BackupPath)
		}
		return
	}
	if tx.BackupPath != "" {
		_ = os.Remove(tx.BackupPath)
	}
	for _, f := range tx.Files {
		if f.BackupPath != "" {
			_ = os.Remove(f.BackupPath)
		}
	}
}

func RollbackPendingUpdate() (UpdateRollbackResult, error) {
	tx, err := ReadPendingUpdate()
	if err != nil {
		if os.IsNotExist(err) {
			return UpdateRollbackResult{}, nil
		}
		return UpdateRollbackResult{}, err
	}
	result := UpdateRollbackResult{FromVersion: tx.ToVersion, ToVersion: tx.FromVersion, TargetPath: tx.TargetPath}
	switch tx.TargetKind {
	case "file":
		files := tx.Files
		if len(files) == 0 {
			files = []UpdateTransactionFile{{TargetPath: tx.TargetPath, BackupPath: tx.BackupPath, SHA256: tx.BackupSHA256}}
		}
		// Verify every backup before touching any binary: a partial restore
		// would recreate exactly the mixed-version install rollback exists to
		// prevent. A missing hash is a validation failure, not a bypass —
		// ReadPendingUpdate already rejects hashless file transactions, so
		// this guards hand-crafted callers.
		for _, f := range files {
			if strings.TrimSpace(f.SHA256) == "" {
				return result, fmt.Errorf("rollback update: backup hash missing for %s", filepath.Base(f.TargetPath))
			}
			got, hashErr := hashFile(f.BackupPath)
			if hashErr != nil || !strings.EqualFold(got, f.SHA256) {
				return result, fmt.Errorf("rollback update: backup hash mismatch for %s", filepath.Base(f.TargetPath))
			}
		}
		mixed, restoreErr := restoreReleaseUnit(files)
		if restoreErr != nil {
			result.MixedInstall = mixed
			return result, fmt.Errorf("rollback update: %w", restoreErr)
		}
	case "app-bundle":
		if _, err := os.Stat(tx.BackupPath); err != nil {
			return result, fmt.Errorf("rollback update: backup bundle: %w", err)
		}
		failed := tx.TargetPath + ".reasonix-failed-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(tx.TargetPath, failed); err != nil {
			return result, fmt.Errorf("rollback update: move failed bundle: %w", err)
		}
		if err := os.Rename(tx.BackupPath, tx.TargetPath); err != nil {
			_ = os.Rename(failed, tx.TargetPath)
			return result, fmt.Errorf("rollback update: restore bundle: %w", err)
		}
	default:
		return result, fmt.Errorf("rollback update: unsupported target kind %q", tx.TargetKind)
	}
	result.RolledBack = true
	_ = os.Remove(PendingUpdatePath())
	return result, nil
}

// Rename/copy indirection so tests can inject mid-unit failures.
var (
	rollbackStageCopy  = copyFileWithHash
	rollbackSwapRename = os.Rename
)

// restoreReleaseUnit swaps every backup into place with compensation, so a
// failed rollback never leaves a mixed old/new install. Phase 1 stages each
// backup next to its target — a copy can fail halfway (disk full, unreadable
// backup) and staging keeps the live binaries untouched until every byte is
// on the target filesystem. Phase 2 swaps via renames only: each target moves
// aside first (renaming works even for the running executable, where
// overwriting does not), so a failure renames the asides back and the unit
// stays coherent on the new version for a retried rollback. Only when that
// unwinding itself fails is the install reported as mixed.
func restoreReleaseUnit(files []UpdateTransactionFile) (mixed bool, err error) {
	stages := make([]string, len(files))
	defer func() {
		for _, stage := range stages {
			if stage != "" {
				_ = os.Remove(stage)
			}
		}
	}()
	for i, f := range files {
		mode := os.FileMode(0o700)
		if st, statErr := os.Stat(f.TargetPath); statErr == nil {
			mode = st.Mode().Perm()
		}
		stage := f.TargetPath + ".reasonix-rollback-stage"
		if _, copyErr := rollbackStageCopy(f.BackupPath, stage, mode); copyErr != nil {
			return false, fmt.Errorf("stage %s: %w", filepath.Base(f.TargetPath), copyErr)
		}
		stages[i] = stage
	}
	asides := make([]string, len(files))
	swapped := 0
	var swapErr error
	for i, f := range files {
		aside := f.TargetPath + ".reasonix-rollback-aside"
		if renameErr := rollbackSwapRename(f.TargetPath, aside); renameErr != nil {
			if os.IsNotExist(renameErr) {
				// A rollback interrupted between renames may have consumed
				// this target; placing the staged copy resumes that swap.
				aside = ""
			} else {
				swapErr = fmt.Errorf("retain %s: %w", filepath.Base(f.TargetPath), renameErr)
				break
			}
		}
		asides[i] = aside
		if renameErr := rollbackSwapRename(stages[i], f.TargetPath); renameErr != nil {
			swapErr = fmt.Errorf("restore %s: %w", filepath.Base(f.TargetPath), renameErr)
			break
		}
		stages[i] = ""
		swapped = i + 1
	}
	if swapErr == nil {
		for _, aside := range asides {
			if aside != "" {
				// Best-effort: on Windows the running executable's aside
				// lingers until the process exits, which is harmless.
				_ = os.Remove(aside)
			}
		}
		return false, nil
	}
	// Compensate: rename the new-version binaries back over the restored old
	// ones. swapped == the failed index, whose target either still holds the
	// new binary (retain failed) or sits aside untouched (restore failed).
	for j := 0; j <= swapped && j < len(files); j++ {
		if j == swapped {
			if asides[j] != "" {
				if _, statErr := os.Lstat(files[j].TargetPath); statErr == nil {
					_ = os.Remove(asides[j])
				} else if os.Rename(asides[j], files[j].TargetPath) != nil {
					mixed = true
				}
			} else if _, statErr := os.Lstat(files[j].TargetPath); statErr != nil {
				// An interrupted-rollback resume failed to place the staged
				// copy and no aside exists: the unit is missing this binary.
				mixed = true
			}
			continue
		}
		if asides[j] == "" || os.Rename(asides[j], files[j].TargetPath) != nil {
			// No new-version copy exists to put back (an interrupted rollback
			// consumed it) or the rename failed; the unit mixes releases.
			mixed = true
		}
	}
	return mixed, swapErr
}

// allowedUpdateTargetBase whitelists the packaged binaries an update
// transaction may name. The main executable names are only valid as the
// primary target; Guard/launcher artifacts only as release-unit siblings.
func allowedUpdateTargetBase(base string, primary bool) bool {
	switch strings.ToLower(base) {
	case "reasonix-desktop", "reasonix-desktop.exe", "reasonix.exe":
		return primary
	case "reasonix-guard", "reasonix-guard.exe", "reasonix-launcher.exe", "reasonix-update-helper.exe":
		return !primary
	default:
		return false
	}
}

func validateUpdateTransaction(tx *UpdateTransaction) error {
	if tx == nil || tx.SchemaVersion != updateTransactionVersion || strings.TrimSpace(tx.ToVersion) == "" {
		return fmt.Errorf("pending update metadata is incomplete")
	}
	tx.TargetPath = filepath.Clean(tx.TargetPath)
	tx.BackupPath = filepath.Clean(tx.BackupPath)
	launcher, err := repairExecutable()
	if err != nil {
		return fmt.Errorf("pending update launcher path is unavailable")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(launcher); resolveErr == nil {
		launcher = resolved
	}
	launcher = filepath.Clean(launcher)
	switch tx.TargetKind {
	case "file":
		if !allowedUpdateTargetBase(filepath.Base(tx.TargetPath), true) {
			return fmt.Errorf("pending update target is not a Reasonix executable")
		}
		if filepath.Dir(launcher) != filepath.Dir(tx.TargetPath) {
			return fmt.Errorf("pending update target is outside the current Guard installation")
		}
		root := filepath.Clean(filepath.Join(config.MemoryUserDir(), "repair"))
		insideRepairDir := func(path string) bool {
			rel, err := filepath.Rel(root, path)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		if !insideRepairDir(tx.BackupPath) {
			return fmt.Errorf("pending update backup is outside the repair directory")
		}
		// Every restorable file must carry a hash — rollback promises to
		// verify all backups before touching any binary, so an unhashed entry
		// would silently weaken that gate.
		if strings.TrimSpace(tx.BackupSHA256) == "" {
			return fmt.Errorf("pending update backup hash is missing")
		}
		primaryListed := len(tx.Files) == 0
		for i := range tx.Files {
			f := &tx.Files[i]
			f.TargetPath = filepath.Clean(f.TargetPath)
			f.BackupPath = filepath.Clean(f.BackupPath)
			primary := f.TargetPath == tx.TargetPath
			primaryListed = primaryListed || primary
			if !allowedUpdateTargetBase(filepath.Base(f.TargetPath), primary) {
				return fmt.Errorf("pending update lists an unexpected release file")
			}
			if filepath.Dir(f.TargetPath) != filepath.Dir(tx.TargetPath) {
				return fmt.Errorf("pending update release file is outside the current Guard installation")
			}
			if !insideRepairDir(f.BackupPath) {
				return fmt.Errorf("pending update backup is outside the repair directory")
			}
			if strings.TrimSpace(f.SHA256) == "" {
				return fmt.Errorf("pending update release file hash is missing")
			}
		}
		if !primaryListed {
			return fmt.Errorf("pending update release unit omits the primary executable")
		}
	case "app-bundle":
		if !strings.HasSuffix(strings.ToLower(tx.TargetPath), ".app") || tx.BackupPath != tx.TargetPath+".reasonix-update-backup" {
			return fmt.Errorf("pending update bundle paths are invalid")
		}
		inside := tx.TargetPath + string(filepath.Separator)
		if !strings.HasPrefix(launcher, inside) {
			return fmt.Errorf("pending update bundle is not the current Guard installation")
		}
	default:
		return fmt.Errorf("pending update target kind is invalid")
	}
	return nil
}

func copyFileWithHash(src, dst string, mode os.FileMode) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".repair-copy-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), in); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := fileutil.ReplaceFile(tmpPath, dst); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
