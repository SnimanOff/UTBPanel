package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type InstallItem struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Original bool   `json:"original"`
}

// Install replaces closed databases/directories on one volume. Originals are
// preserved until every rename succeeds; a partial failure rolls them back.
// A journal also allows the next process to roll back an interrupted install.
func Install(dataDir string, items []InstallItem) error {
	return InstallThen(dataDir, items, nil)
}
func InstallThen(dataDir string, items []InstallItem, ready func() error) error {
	if err := checkInstallPaths(dataDir, items); err != nil {
		return err
	}
	journal := filepath.Join(dataDir, "restore-install.json")
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		return fmt.Errorf("an interrupted restore must be recovered first")
	}
	for index := range items {
		_, err := os.Lstat(items[index].Target)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		items[index].Original = err == nil
		// With no journal, these are leftovers of a committed installation.
		// Remove them before a new journal can make them rollback candidates.
		if err := os.RemoveAll(items[index].Target + ".restore-old"); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(journal, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		os.Remove(journal)
		return err
	}
	if closeErr != nil {
		os.Remove(journal)
		return closeErr
	}
	rollback := func(cause error) error {
		if err := RecoverInstall(dataDir); err != nil {
			return fmt.Errorf("restore: %v; rollback: %w", cause, err)
		}
		return cause
	}
	for _, item := range items {
		if err := os.Rename(item.Target, item.Target+".restore-old"); err != nil && !os.IsNotExist(err) {
			return rollback(err)
		}
		if err := os.Rename(item.Source, item.Target); err != nil {
			return rollback(err)
		}
	}
	if ready != nil {
		if err := ready(); err != nil {
			return rollback(err)
		}
	}
	// Removing the journal is the commit point: leftover originals can be
	// discarded after this, while startup never rolls back a committed state.
	if err := os.Remove(journal); err != nil {
		return err
	}
	for _, item := range items {
		os.RemoveAll(item.Target + ".restore-old")
	}
	return nil
}
func RecoverInstall(dataDir string) error {
	journal := filepath.Join(dataDir, "restore-install.json")
	raw, err := os.ReadFile(journal)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var items []InstallItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	if err := checkInstallPaths(dataDir, items); err != nil {
		return err
	}
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		old := item.Target + ".restore-old"
		if _, err := os.Lstat(old); err == nil {
			if err := os.RemoveAll(item.Target); err != nil {
				return err
			}
			if err := os.Rename(old, item.Target); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if !item.Original {
			if err := os.RemoveAll(item.Target); err != nil {
				return err
			}
		}
	}
	return os.Remove(journal)
}

func checkInstallPaths(dataDir string, items []InstallItem) error {
	seen := map[string]bool{}
	base, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	for _, item := range items {
		for _, target := range []string{item.Source, item.Target} {
			abs, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(base, abs)
			if err != nil || relative == ".." || !filepath.IsLocal(relative) {
				return fmt.Errorf("restore journal path escapes data directory")
			}
		}
		targetAbs, err := filepath.Abs(item.Target)
		if err != nil {
			return err
		}
		target, _ := filepath.Rel(base, targetAbs)
		if seen[target] {
			return fmt.Errorf("duplicate restore target")
		}
		seen[target] = true
		switch target {
		case "core.db", "panel.db", "bots", "pki":
		default:
			return fmt.Errorf("invalid restore target")
		}
	}
	return nil
}
