#!/usr/bin/env python3
"""Explicit live native-client acceptance using the shared guarded Codex workflow.

Run through scripts/test-local.sh. Requires --authenticated and existing auth;
default CI never calls models. Diagnostic screens remain in private namespaces.
"""
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import shlex
import shutil
import subprocess
import time

source = Path(__file__).resolve().parents[1] / "experiments/terminal-stack/verify_harness.py"
spec = importlib.util.spec_from_file_location("terminal_harness", source)
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)


class NativeCheck(harness.HarnessCheck):
    def fixture_parent(self):
        return str(Path(os.environ["SUPERTREE_TEST_SANDBOX"]) / "tmp")

    def attach_command(self):
        command = [self.binary, "native", "attach", "--state", self.state,
                   "--terminal", "codex", "--generation", "1"]
        if self.remote:
            command = ["env", "SUPERTREE_TEST_SANDBOX=" + self.base, *command]
        return command

    def prepare_view(self):
        # Host inspection may be ready before the outer client acquires its
        # terminal. In a bare PTY there is no emulator to answer the legacy
        # Bubble Tea package's startup color query; wait for its timeout and the
        # actual native alternate screen before typing. Real GUI terminals
        # answer that query themselves.
        if not hasattr(self, "reader_lock"):
            return
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            self.ensure_attached()
            with self.reader_lock:
                ready = b"\x1b[?1049h" in self.outer_tail
            if ready:
                return
            time.sleep(.05)
        raise RuntimeError("native client did not acquire its outer terminal")

    def desktop_runtime(self):
        # WezTerm appends its own Unix-socket basename. Keep that path short
        # while all runtime files still remain in the isolated runner.
        path = Path(os.environ["SUPERTREE_TEST_SANDBOX"]) / ("gui-" + self.root.name[-8:])
        path.mkdir(mode=0o700, exist_ok=True)
        return path

    def run(self, command):
        if self.remote:
            command = ["env", "SUPERTREE_TEST_SANDBOX=" + self.base, *command]
        return super().run(command)

    def request(self, operation, value):
        command = [self.binary, "terminal", operation, "--state", self.state]
        if self.remote:
            command = self.ssh + ["--", self.args.ssh, shlex.join(
                ["env", "SUPERTREE_TEST_SANDBOX=" + self.base, *command])]
        return json.loads(subprocess.check_output(command, input=json.dumps(value),
                          env=self.env, text=True, stderr=subprocess.PIPE, timeout=20))

    def executable(self, name):
        if self.remote:
            value = self.run(["/bin/sh", "-c", 'command -v "$1"', "fixture", name]).strip()
        else:
            value = shutil.which(name, path=self.env.get("PATH"))
        if not value or not value.startswith("/"):
            raise RuntimeError("required fixture executable is unavailable")
        return value

    def start(self):
        if self.remote:
            path = PurePosixPath(self.base)
            if not self.base.startswith("/tmp/st-") or ".." in path.parts:
                raise ValueError("explicit private /tmp/st-* remote namespace required")
            self.run(["mkdir", "-m", "700", self.workspace, self.state])
            home = self.run(["/bin/sh", "-c", 'printf "%s" "$HOME"']).strip()
            path = self.run(["/bin/sh", "-c", 'printf "%s" "$PATH"']).strip()
            child_env = dict(HOME=home, PATH=path, LANG="C.UTF-8", ST_AGENT_TOOLS="0")
        else:
            for name in ("workspace", "host", "home"):
                (self.root / name).mkdir(mode=0o700)
            keys = ("HOME", "PATH", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "CODEX_HOME",
                    "ST_AGENT_TOOLS", "SUPERTREE_TEST_SANDBOX", "SSL_CERT_FILE", "SSL_CERT_DIR",
                    "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY")
            child_env = {key: self.env[key] for key in keys if key in self.env}
        self.run(["git", "-C", self.workspace, "init", "-q", "-b", "main"])
        status = json.loads(self.run([self.binary, "terminal", "host", "start", "--state", self.state]))
        self.epoch = status["epoch"]
        self.started = True
        codex = [self.executable(self.args.codex_bin), "--no-daemon", "--sandbox", "read-only",
                 "--ask-for-approval", "on-request", "-c", 'approvals_reviewer="user"', "--cd", self.workspace]
        if self.args.model:
            codex += ["--model", self.args.model]
        programs = [("codex", codex), ("editor", [self.executable(self.args.nvim), "--clean", "--cmd", "set noswapfile"]),
                    ("shell", [self.executable("zsh"), "-f", "-i"])]
        for ident, argv in programs:
            launch = dict(request_id="fixture-" + ident, terminal_id=ident, generation=1,
                          definition=dict(workspace_id="native-fixture", executable=argv[0], args=argv[1:],
                                          directory=self.workspace, environment=child_env, columns=140, rows=40))
            result = self.request("ensure", dict(epoch=self.epoch, launch=launch))["info"]
            if result["state"] != "running":
                raise RuntimeError("native fixture program did not start")
        self.pids = [frame["PID"] for frame in self.frames()]
        self.attach()
        initial = self.wait(lambda text: "Trust this folder?" in text or (
            "Codex" in text and ("? for shortcuts" in text or "context left" in text)), "native authenticated startup")
        if "Trust this folder?" in initial:
            self.trust_fixture(initial)
            self.wait(lambda text: "Trust this folder?" not in text and (
                "? for shortcuts" in text or "context left" in text), "native composer after folder access")
        self.observe("real_harness_ui_startup")

    def frames(self):
        status = json.loads(self.run([self.binary, "terminal", "inventory", "--state", self.state]))["status"]
        return [dict(PID=info.get("pid", 0)) for info in status["terminals"]]

    def text(self):
        if self.remote:
            # One SSH round trip per observation; screen inspection remains
            # bounded to sixteen rows in each host request.
            script = '''import json,subprocess,sys
binary,state,epoch=sys.argv[1:]
cells=[]
height=40
for row in range(0,height,16):
 request=dict(epoch=epoch,target=dict(terminal_id="codex",generation=1),row=row,rows=16)
 frame=json.loads(subprocess.check_output([binary,"terminal","screen","--state",state],input=json.dumps(request).encode()))["screen"]
 cells.extend(frame["cells"])
 width=frame["columns"]
print("\\n".join("".join(cell.get("t"," ") for cell in cells[i:i+width]) for i in range(0,len(cells),width)))
'''
            return self.run(["python3", "-c", script, self.binary, self.state, self.epoch])
        cells = []
        first = self.request("screen", dict(epoch=self.epoch, target=dict(terminal_id="codex", generation=1), row=0, rows=16))["screen"]
        width, height = first["columns"], first["height"]
        cells.extend(first["cells"])
        for row in range(16, height, 16):
            cells.extend(self.request("screen", dict(epoch=self.epoch,
                         target=dict(terminal_id="codex", generation=1), row=row, rows=16))["screen"]["cells"])
        return "\n".join("".join(cell.get("t", " ") for cell in cells[i:i + width])
                         for i in range(0, len(cells), width))

    def close(self):
        self.teardown = True
        failed = len(self.observations) != 8
        if failed and self.started:
            try:
                (self.root / "last-screen.txt").write_text(NativeCheck.text(self))
            except Exception:
                pass
        try:
            if self.child is not None:
                self.detach(crash=True)
        finally:
            if self.started:
                self.run([self.binary, "terminal", "host", "stop", "--state", self.state, "--force"])
            if failed:
                destination = Path(__file__).resolve().parents[1] / ".dev" / ("native-failure-" + self.root.name)
                destination.mkdir(mode=0o700)
                for name in ("last-screen.txt", "gui.log", "cli-error.txt", "outer-tail.bin"):
                    source = self.root / name
                    if source.is_file():
                        shutil.copyfile(source, destination / name)
                        (destination / name).chmod(0o600)
                print("Private native diagnostics: " + str(destination), flush=True)
            shutil.rmtree(self.root)


class NativeDesktopCheck(harness.DesktopHarnessCheck, NativeCheck):
    def gui_command(self, *args, **kwargs):
        try:
            return super().gui_command(*args, **kwargs)
        except subprocess.CalledProcessError as error:
            (self.root / "cli-error.txt").write_text(error.stderr or "")
            raise


if __name__ == "__main__":
    harness.main(NativeCheck, NativeDesktopCheck)
