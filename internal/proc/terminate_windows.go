//go:build windows

package proc

// terminateTree is the Windows side of the graceful-cancellation boundary.
// Windows has no SIGTERM delivery to a process group, so a cancelled child
// keeps its original immediate-kill behaviour: terminateTree itself performs
// the kill and leaves nothing for the grace period to wait on.
func (p *platformProcess) terminateTree() {
	p.killTree()
}
