package terminal

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func leaderStatus(pid int) (syscall.WaitStatus, bool, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false, err
	}
	f := strings.Fields(string(b)[strings.LastIndexByte(string(b), ')')+1:])
	if len(f) < 50 {
		return 0, false, fmt.Errorf("invalid leader status")
	}
	status, err := strconv.ParseUint(f[49], 10, 32)
	return syscall.WaitStatus(status), f[0] == "Z" || f[0] == "X", err
}

func processIdentity(pid int) (member, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return member{}, err
	}
	f := strings.Fields(string(b)[strings.LastIndexByte(string(b), ')')+1:])
	if len(f) < 20 {
		return member{}, fmt.Errorf("invalid process identity")
	}
	if f[0] == "Z" || f[0] == "X" {
		return member{}, os.ErrNotExist
	}
	group, e1 := strconv.Atoi(f[2])
	session, e2 := strconv.Atoi(f[3])
	if e1 != nil || e2 != nil {
		return member{}, fmt.Errorf("invalid process session")
	}
	return member{pid, group, session, f[19]}, nil
}
func sessionMembers(session int) ([]member, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var result []member
	for _, entry := range entries {
		pid, e := strconv.Atoi(entry.Name())
		if e != nil {
			continue
		}
		m, e := processIdentity(pid)
		if e == nil && m.session == session {
			result = append(result, m)
		}
	}
	return result, nil
}
