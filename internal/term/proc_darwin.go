package term

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// A Mac tells a process's working directory only through libproc, a C call,
// and dv is built without cgo; purego makes the call without it.
var procPidinfo = sync.OnceValue(func() func(pid, flavor int32, arg uint64, buf unsafe.Pointer, size int32) int32 {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return nil
	}
	sym, err := purego.Dlsym(lib, "proc_pidinfo")
	if err != nil {
		return nil
	}
	var f func(pid, flavor int32, arg uint64, buf unsafe.Pointer, size int32) int32
	purego.RegisterFunc(&f, sym)
	return f
})

const (
	pidVnodePathInfo = 9    // PROC_PIDVNODEPATHINFO
	vnodeInfoSize    = 152  // struct vnode_info
	maxPathLen       = 1024 // MAXPATHLEN
)

func workingDir(pid int) string {
	call := procPidinfo()
	if call == nil {
		return ""
	}
	// struct proc_vnodepathinfo: the working directory's vnode_info_path, then the root's.
	var info [2 * (vnodeInfoSize + maxPathLen)]byte
	if n := call(int32(pid), pidVnodePathInfo, 0, unsafe.Pointer(&info), int32(len(info))); n != int32(len(info)) {
		return ""
	}
	path := info[vnodeInfoSize : vnodeInfoSize+maxPathLen]
	if i := bytes.IndexByte(path, 0); i >= 0 {
		path = path[:i]
	}
	return string(path)
}

func argsOf(pid int) []string { return strings.Fields(ps("command", pid)) }

// foregroundOf is the foreground process group of pid's terminal, 0 if it can't tell.
func foregroundOf(pid int) int {
	n, _ := strconv.Atoi(strings.TrimSpace(ps("tpgid", pid)))
	return max(n, 0)
}

func ps(field string, pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "ps", "-o", field+"=", "-p", strconv.Itoa(pid)).Output()
	return string(out)
}
