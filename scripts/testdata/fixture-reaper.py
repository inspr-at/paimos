# SPDX-License-Identifier: AGPL-3.0-only
"""Reap this test command's orphan descendants in Linux PID namespaces."""
import ctypes
import os
from pathlib import Path
import signal
import sys
import time


def main():
    # PR_SET_CHILD_SUBREAPER from linux/prctl.h. Unlike PID 1 in some test
    # namespaces, this parent waits for the fixtures that deliberately orphan.
    # https://man7.org/linux/man-pages/man2/PR_SET_CHILD_SUBREAPER.2const.html
    libc = ctypes.CDLL(None, use_errno=True)
    libc.prctl.argtypes = [ctypes.c_int] + [ctypes.c_ulong] * 4
    if libc.prctl(36, 1, 0, 0, 0) != 0:
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error))

    root = os.fork()
    if root == 0:
        try:
            os.execvp(sys.argv[1], sys.argv[1:])
        except OSError:
            os._exit(127)
    root_fd = os.pidfd_open(root)

    def forward(signum, _frame):
        try:
            signal.pidfd_send_signal(root_fd, signum)
        except ProcessLookupError:
            pass

    for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(signum, forward)
    try:
        while True:
            child, status = os.waitpid(-1, 0)
            if child == root:
                code = os.waitstatus_to_exitcode(status)
                break

        deadline = time.monotonic() + 5
        while True:
            try:
                while os.waitpid(-1, os.WNOHANG)[0]:
                    pass
            except ChildProcessError:
                break
            # Only our unreaped direct children can appear here. Their PIDs
            # cannot be reused until this single-threaded parent waits them.
            children = Path(f'/proc/self/task/{os.getpid()}/children').read_text().split()
            if children and code == 0:
                print('fixture runner left live descendants: ' + ','.join(children), file=sys.stderr)
                code = 1
            for child in children:
                try:
                    os.kill(int(child), signal.SIGKILL)
                except ProcessLookupError:
                    pass
            if time.monotonic() >= deadline:
                raise RuntimeError('fixture descendants could not be reaped')
            time.sleep(0.01)
        return code if code >= 0 else 128 - code
    finally:
        os.close(root_fd)


if __name__ == '__main__':
    sys.exit(main())
