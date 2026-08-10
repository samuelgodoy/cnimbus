package rootfs

import (
	"path/filepath"
)

type entryKind int

const (
	entryDir entryKind = iota
	entryFile
	entrySymlink
)

type treeEntry struct {
	path       string // cpio path, no leading slash, "/"-separated
	kind       entryKind
	perm       uint32
	data       []byte
	linkTarget string
}

type fileTree struct {
	entries []*treeEntry
	seen    map[string]bool
}

func newFileTree() *fileTree {
	return &fileTree{seen: map[string]bool{}}
}

func (t *fileTree) addDir(path string) {
	path = cleanPath(path)
	if path == "" || t.seen[path] {
		return
	}
	// Every ancestor directory must exist as its own cpio entry, or the
	// kernel's initramfs unpacker rejects the archive outright.
	t.addDir(filepath.ToSlash(filepath.Dir(path)))
	t.seen[path] = true
	t.entries = append(t.entries, &treeEntry{path: path, kind: entryDir})
}

func (t *fileTree) addFile(path string, perm uint32, data []byte) {
	path = cleanPath(path)
	t.addDir(filepath.ToSlash(filepath.Dir(path)))
	if t.seen[path] {
		return
	}
	t.seen[path] = true
	t.entries = append(t.entries, &treeEntry{path: path, kind: entryFile, perm: perm, data: data})
}

func (t *fileTree) addSymlink(path, target string) {
	path = cleanPath(path)
	t.addDir(filepath.ToSlash(filepath.Dir(path)))
	if t.seen[path] {
		return
	}
	t.seen[path] = true
	t.entries = append(t.entries, &treeEntry{path: path, kind: entrySymlink, linkTarget: target})
}

func cleanPath(p string) string {
	p = filepath.ToSlash(p)
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if p == "." {
		return ""
	}
	return p
}
