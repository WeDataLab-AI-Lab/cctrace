package desktop

type daemonLockState uint8

const (
	daemonLockUnknown daemonLockState = iota
	daemonLockMissing
	daemonLockFree
	daemonLockHeld
)
