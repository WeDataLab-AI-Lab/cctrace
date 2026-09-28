package buffer

// syncDir is a no-op on Windows. Opening a directory succeeds there but Sync on
// the handle fails with "Access is denied", so the POSIX version turned every
// spill into an error -- twelve tests in this package failed on windows-latest
// for that one call (#657).
//
// Nothing is lost in production: cctraced is the only binary that imports this
// package and it is built and run in Linux containers; cmd/cctrace does not
// import it at all. The split is a file rather than a runtime branch so that
// fact stays visible to whoever reads the durability path, and so the rest of
// the package keeps being tested on Windows instead of being skipped there.
func (d *DiskSpiller) syncDir() error { return nil }
