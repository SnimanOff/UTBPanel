//go:build linux

// Package botdir keeps a bot's directory safe for the core, which works
// there as root while the bot may write to it.
package botdir

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The core runs as root inside a directory the bot can write to, so it
// never follows a path there: every entry is opened with O_NOFOLLOW and
// changed through its descriptor. A symbolic link planted by the bot is
// removed instead of being followed to core.db or the master key.

// ownDir makes path a real directory with the owner and mode (sticky bit
// included). A link or a file under that name is removed first.
func ownDir(path string, uid, gid int, mode uint32) error {
	for range 3 {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		switch {
		case errors.Is(err, unix.ENOENT):
			if err := unix.Mkdir(path, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				return err
			}
			continue
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
			if err := unix.Unlink(path); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
			continue
		case err != nil:
			return err
		}
		same, err := fixFD(fd, path, uid, gid, mode)
		unix.Close(fd)
		if err != nil {
			return err
		}
		if same {
			return nil
		}
	}
	return fmt.Errorf("botdir: %s keeps being replaced", path)
}

// ownFile gives an existing regular file to uid:gid (mode 0 keeps it). A
// link is removed; a hard link to someone else's file is refused.
func ownFile(path string, uid, gid int, mode uint32) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil
	case errors.Is(err, unix.ELOOP):
		return unlink(path)
	case err != nil:
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFDIR:
		return nil // not ours to change; nothing is written through it
	default:
		return unlink(path)
	}
	if st.Nlink != 1 {
		return fmt.Errorf("botdir: %s has %d hard links", path, st.Nlink)
	}
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return err
	}
	if mode != 0 {
		return unix.Fchmod(fd, mode)
	}
	return nil
}

// fixFD sets owner and mode through fd and reports whether path still
// names that same object (it was not swapped in the meantime).
func fixFD(fd int, path string, uid, gid int, mode uint32) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return false, err
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return false, err
	}
	var now unix.Stat_t
	if err := unix.Fstatat(unix.AT_FDCWD, path, &now, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, nil
	}
	return now.Dev == st.Dev && now.Ino == st.Ino, nil
}

func unlink(path string) error {
	if err := unix.Unlink(path); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

// entries lists a directory without following links.
func entries(dir string) ([]string, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var names []string
	buf := make([]byte, 8192)
	for {
		n, err := unix.Getdents(fd, buf)
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return names, nil
		}
		_, _, names = unix.ParseDirent(buf[:n], -1, names)
	}
}

// Secure lays out a bot directory the bot cannot turn against the
// core. The directory is root's with the sticky bit: the bot creates its
// database files there but cannot rename or remove what root owns.
//
//	bots/<id>           root:bot 1770
//	  bot.db{,-wal,-shm} bot:bot (SQLite needs to create them)
//	  files/            root:bot 0770  DATA_DIR, the bot's own space
//	  releases/         root:bot 0750  binaries 0550, metadata 0640
//	  secrets/          root:bot 0750  file secrets 0440
//	  logs/             root     0700
//
// Old installations had all of it owned by the bot; this also takes it back.
func Secure(dir string, uid int) error {
	if err := ownDir(dir, 0, uid, 0o1770); err != nil {
		return err
	}
	subdirs := []struct {
		name string
		gid  int
		mode uint32
	}{{"files", uid, 0o770}, {"releases", uid, 0o750}, {"secrets", uid, 0o750}, {"logs", 0, 0o700}}
	for _, s := range subdirs {
		if err := ownDir(filepath.Join(dir, s.name), 0, s.gid, s.mode); err != nil {
			return err
		}
	}
	// Only root writes below these from now on; take back what is inside.
	releases := filepath.Join(dir, "releases")
	versions, err := entries(releases)
	if err != nil {
		return err
	}
	for _, v := range versions {
		vdir := filepath.Join(releases, v)
		var st unix.Stat_t
		if err := unix.Lstat(vdir, &st); err != nil {
			continue
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			if err := unlink(vdir); err != nil {
				return err
			}
			continue
		}
		if err := ownDir(vdir, 0, uid, 0o750); err != nil {
			return err
		}
		names, err := entries(vdir)
		if err != nil {
			return err
		}
		for _, n := range names {
			mode := uint32(0o640)
			if n == "bot" {
				mode = 0o550
			}
			if err := ownFile(filepath.Join(vdir, n), 0, uid, mode); err != nil {
				return err
			}
		}
	}
	for _, sub := range []struct {
		name string
		gid  int
		mode uint32
	}{{"secrets", uid, 0o440}, {"logs", 0, 0o640}} {
		names, err := entries(filepath.Join(dir, sub.name))
		if err != nil {
			return err
		}
		for _, n := range names {
			if err := ownFile(filepath.Join(dir, sub.name, n), 0, sub.gid, sub.mode); err != nil {
				return err
			}
		}
	}
	// The database belongs to the bot; a link in its place is dropped.
	for _, f := range []string{"bot.db", "bot.db-wal", "bot.db-shm"} {
		if err := ownFile(filepath.Join(dir, f), uid, uid, 0); err != nil {
			return err
		}
	}
	return unlink(filepath.Join(dir, "bot.db.restore"))
}
