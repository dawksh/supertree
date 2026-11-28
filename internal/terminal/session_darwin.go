package terminal

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func leaderStatus(pid int) (syscall.WaitStatus, bool, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, false, err
	}
	if p.Proc.P_pid != int32(pid) {
		return 0, false, os.ErrNotExist
	}
	return syscall.WaitStatus(p.Proc.P_xstat), p.Proc.P_stat == 5, nil
}

func processIdentity(pid int) (member, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return member{}, err
	}
	if p.Proc.P_pid != int32(pid) || p.Proc.P_stat == 5 {
		return member{}, os.ErrNotExist
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		return member{}, err
	}
	return member{pid, int(p.Eproc.Pgid), sid, fmt.Sprintf("%d:%d", p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec)}, nil
}
func sessionMembers(session int) ([]member, error) {
	ps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	var result []member
	for _, p := range ps {
		if p.Eproc.Ucred.Uid != uint32(os.Getuid()) {
			continue
		}
		m, e := processIdentity(int(p.Proc.P_pid))
		if e == nil && m.session == session {
			result = append(result, m)
		}
	}
	return result, nil
}
