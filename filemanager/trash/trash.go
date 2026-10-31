// Package trash moves files of this computer to the trash of its
// desktop, where they can be restored: on Linux the freedesktop.org trash
// that file managers share, on Windows the recycle bin.
package trash

// Move moves the file or directory p to the trash. It never deletes: when
// there is no trash for p, it fails and p stays.
func Move(p string) error { return move(p) }
