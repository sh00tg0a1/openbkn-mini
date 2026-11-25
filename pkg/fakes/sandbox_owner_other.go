//go:build !unix

package fakes

// fileOwner has no owner to report off Unix: files stay as they are written.
func fileOwner(string) (uid, gid int, ok bool) { return 0, 0, false }
