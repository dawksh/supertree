"""Explicit live Codex acceptance; uses existing auth and real, bounded model turns.

Run through scripts/test-local.sh with --authenticated and an existing
--codex-home. No credentials are copied, and every test program is disposable.
Only pass/fail observations are printed; screen captures stay private on failure.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import shutil
import signal
import struct
import subprocess
import tempfile
import termios
import threading
import time


class QuotaBlocked(RuntimeError):
    pass


class HarnessCheck:
    def __init__(self, args):
        self.args = args
        self.root = Path(tempfile.mkdtemp(prefix="st-stack-harness-", dir=self.fixture_parent())).resolve()
        self.remote = bool(args.ssh)
        self.base = args.remote_directory if self.remote else str(self.root)
        self.state = self.base + "/host"
        self.workspace = self.base + "/workspace"
        self.binary = args.binary
        self.child = None
        self.started = False
        self.observations = {}
        self.teardown = False
        self.allow_low_quota = args.allow_low_quota
        print(json.dumps({"fixture": self.base, "transport": "ssh" if self.remote else "local PTY"}), flush=True)
        self.env = os.environ.copy()
        if not self.remote:
            self.env["HOME"] = str(self.root / "home")
            self.env["CODEX_HOME"] = args.codex_home
        for key in list(self.env):
            if key.startswith("CODEX_") and key != "CODEX_HOME":
                self.env.pop(key)
        self.env["ST_AGENT_TOOLS"] = "0"
        self.ssh = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
                    "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "ForwardAgent=no", "-S", "none"]

    def fixture_parent(self):
        return "/tmp"

    def attach_command(self):
        return [self.binary, "attach", "-state", self.state]

    def prepare_view(self):
        self.send(b"\x1b1\x1bz")

    def desktop_runtime(self):
        return self.root

    def run(self, command):
        if self.remote:
            command = self.ssh + ["--", self.args.ssh, shlex.join(command)]
        return subprocess.check_output(command, env=self.env, text=True, stderr=subprocess.PIPE, timeout=20)

    def start(self):
        if self.remote:
            if not self.base or not self.base.startswith("/tmp/st-stack-"):
                raise ValueError("remote directory must be an explicitly owned /tmp/st-stack-* namespace")
            self.run(["mkdir", "-m", "700", self.workspace, self.state])
        else:
            for name in ("workspace", "host", "home"):
                (self.root / name).mkdir(mode=0o700)
        self.run(["git", "-C", self.workspace, "init", "-q", "-b", "main"])
        codex = [self.args.codex_bin, "--no-daemon", "--sandbox", "read-only", "--ask-for-approval", "on-request", "-c", 'approvals_reviewer="user"',
                 "--cd", self.workspace]
        if self.args.model:
            codex += ["--model", self.args.model]
        commands = ["exec " + shlex.join(codex), "exec " + shlex.join([self.args.nvim, "--clean", "--cmd", "set noswapfile"]), "exec /bin/zsh -f -i"]
        command = [self.binary, "start", "-state", self.state]
        for item in commands:
            command += ["-command", item]
        self.run(command)
        self.started = True
        self.pids = [f["PID"] for f in self.frames()]
        self.attach()
        initial = self.wait(lambda text: "Trust this folder?" in text or ("Codex" in text and ("? for shortcuts" in text or "context left" in text)), "authenticated startup")
        if "Trust this folder?" in initial:
            self.trust_fixture(initial)
            self.wait(lambda text: "Trust this folder?" not in text and ("? for shortcuts" in text or "context left" in text), "composer after folder access")
        self.observe("real_harness_ui_startup")

    def trust_fixture(self, text):
        compact = "".join(text.split())
        if self.workspace not in compact or "Trustandcontinue" not in compact:
            raise RuntimeError("unexpected folder-access request outside the owned fixture")
        self.send(b"\r")

    def frames(self):
        return json.loads(self.run([self.binary, "inspect", "-state", self.state]))["Frames"]

    def text(self):
        frame = self.frames()[0]
        width = frame["Width"]
        return "\n".join("".join(c.get("t", " ") for c in frame["Cells"][i:i + width]) for i in range(0, len(frame["Cells"]), width))

    def observe(self, name):
        self.observations[name] = True
        print(json.dumps({"check": name, "passed": True}), flush=True)

    def guard_quota(self, text):
        text = " ".join(text.split()).lower()
        footer = re.search(r"\blimit:\s*(?:only\s*)?(\d+)%\s*left", text)
        low_footer = footer is not None and int(footer[1]) <= 25
        low_warning = low_footer or "of your 5h limit left" in text or "of your weekly limit left" in text
        chooser = "approaching rate limits" in text or "switch to gpt-" in text
        if not self.teardown and (chooser or "usage limit" in text or (low_warning and not getattr(self, "allow_low_quota", False))):
            raise QuotaBlocked("Codex reported a low/exhausted quota; stop live calls without interacting with a model chooser")

    def wait(self, predicate, label, timeout=90):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.drain()
            self.ensure_attached()
            text = self.text()
            self.guard_quota(text)
            if predicate(text):
                return text
            time.sleep(.15)
        capture = self.root / "last-screen.txt"
        capture.write_text(self.text())
        capture.chmod(0o600)
        raise RuntimeError("Timed out: " + label + "; private screen: " + str(capture))

    def attach(self):
        command = self.attach_command()
        if self.remote:
            command = self.ssh + ["-tt", "--", self.args.ssh, "TERM=xterm-256color " + shlex.join(command)]
        child, fd = pty.fork()
        if child == 0:
            os.execvpe(command[0], command, dict(self.env, TERM="xterm-256color"))
        self.child, self.fd = child, fd
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 140, 0, 0))
        os.set_blocking(fd, False)
        self.start_reader()
        time.sleep(.3)
        self.prepare_view()

    def drain(self):
        # The reader must run while a slow SSH inspection is in flight too.
        # Polling stdout only between inspections can block attach's paint loop
        # on a full PTY and prevent it from forwarding queued input.
        pass

    def start_reader(self):
        self.reader_stop = threading.Event()
        self.reader_lock = threading.Lock()
        self.outer_tail = b""

        def read():
            while not self.reader_stop.is_set():
                if not select.select([self.fd], [], [], .1)[0]:
                    continue
                try:
                    data = os.read(self.fd, 65536)
                except BlockingIOError:
                    continue
                except OSError:
                    return  # PTY EOF is EIO on supported systems.
                if not data:
                    return
                with self.reader_lock:
                    self.outer_tail = (self.outer_tail + data)[-65536:]

        self.reader = threading.Thread(target=read, daemon=True)
        self.reader.start()

    def stop_reader(self):
        self.reader_stop.set()
        self.reader.join(timeout=2)
        if self.reader.is_alive():
            raise RuntimeError("outer PTY reader failed to stop")

    def ensure_attached(self):
        if self.child is None:
            raise RuntimeError("attach is disconnected")
        pid, status = os.waitpid(self.child, os.WNOHANG)
        if pid:
            self.child = None
            self.stop_reader()
            os.close(self.fd)
            raise RuntimeError("attach exited while awaiting input, status " + str(status))

    def send(self, data, paste=False):
        if not self.teardown:
            self.guard_quota(self.text())
        self.drain()
        if isinstance(data, str):
            data = data.encode()
        if paste:
            data = b"\x1b[200~" + data + b"\x1b[201~"
        view = memoryview(data)
        while view:
            try:
                view = view[os.write(self.fd, view):]
            except BlockingIOError:
                self.drain()
                time.sleep(.01)

    def detach(self, crash=False):
        if self.child is None:
            return
        child = self.child
        if crash:
            os.kill(child, signal.SIGKILL)
        else:
            self.send(b"\x1bq")
        deadline = time.monotonic() + 8
        while True:
            self.drain()
            result, status = os.waitpid(child, os.WNOHANG)
            if result:
                if not crash and status != 0:
                    raise RuntimeError("attach exited unsuccessfully")
                break
            if time.monotonic() > deadline:
                os.kill(child, signal.SIGKILL)
                os.waitpid(child, 0)
                raise RuntimeError("attach failed to detach")
            time.sleep(.05)
        self.child = None
        self.stop_reader()
        os.close(self.fd)

    def compose_initial_draft(self):
        for attempt in range(2):
            self.send("Reply with STAGE2 and COMPLETE joined by a hyphen. Do not call tools.", paste=True)
            time.sleep(.3)
            self.send(b"\x0a")  # Legacy Ctrl-j: native Codex newline, not submit.
            self.send("Literal test text: 界 e\u0301 👩\u200d💻", paste=True)
            text = self.wait(lambda text: "Trust this folder?" in text or (
                "Reply with STAGE2" in text and "Literal test text" in text and "STAGE2-COMPLETE" not in text), "multiline draft or folder access")
            if "Trust this folder?" not in text:
                break
            self.trust_fixture(text)
            self.wait(lambda text: "Trust this folder?" not in text, "composer after deferred folder access")
            # Access may appear after the first ready frame, consuming draft
            # keystrokes. No model submission has occurred in this phase.
            self.send(b"\x03")
        else:
            raise RuntimeError("folder access repeatedly interrupted the disposable draft")

    def check(self):
        self.compose_initial_draft()
        self.observe("multiline_and_unicode_paste_without_submit")
        time.sleep(.3)
        self.send(b"\r")
        # Some Codex versions defer fixture folder access until first submit.
        text=self.wait(lambda text:"Trust this folder?" in text or "STAGE2-COMPLETE" in text,"first submission or folder access")
        if "Trust this folder?" in text:
            self.trust_fixture(text)
            text = self.wait(lambda text: "Trust this folder?" not in text and (
                "STAGE2-COMPLETE" in text or re.search(r"^\s*› Reply with STAGE2", text, re.M)),
                "composer or reply after folder access")
            # Newer harness versions restore the draft after accepting folder
            # access. Submit only when that exact draft is still in the composer;
            # never repeat a submission merely because a reply is taking time.
            if re.search(r"^\s*› Reply with STAGE2", text, re.M):
                self.send(b"\r")
        self.wait(lambda text: "STAGE2-COMPLETE" in text, "real model submission")
        self.observe("real_model_submission")
        self.send("Produce exactly sixty lines. Each line is ROW, a hyphen, and that line's three-digit number from 001 through 060. No tools or extra text.", paste=True)
        time.sleep(.3)
        self.send(b"\r")
        self.wait(lambda text: "ROW-060" in text, "long real transcript")
        self.send(b"\x14")  # Codex's own transcript screen.
        time.sleep(.3)
        self.send(b"\x1b[5~\x1b[5~\x1b[5~")
        older = self.wait(lambda text: "ROW-001" in text, "scroll to older transcript text")
        self.send(b"\x1b[6~\x1b[6~\x1b[6~")
        newer = self.wait(lambda text: "ROW-060" in text, "scroll back to newest transcript text")
        if older == newer:
            raise RuntimeError("transcript scroll keys did not move the viewport")
        # Codex can have a details layer above transcript browsing; return from
        # both before testing the live composer. Wait for the mode hint to leave.
        for _ in range(3):
            self.send(b"\x1b")
            time.sleep(.3)
            text = self.text()
            if "Browsing transcript" not in text and "ROW-060" in text:
                break
        self.wait(lambda text: "Browsing transcript" not in text and "ROW-060" in text, "return from transcript")
        self.observe("harness_transcript_keyboard_navigation")
        self.detach(crash=True)
        self.attach()
        self.wait(lambda text: "ROW-060" in text, "same conversation after client death")
        if [f["PID"] for f in self.frames()] != self.pids:
            raise RuntimeError("client reconnect replaced a program")
        self.observe("same_authenticated_conversation_after_client_death")
        self.send("Run the read-only command sleep 30. Do not write files or make network requests.", paste=True)
        time.sleep(.3)
        self.send(b"\r")
        time.sleep(3)
        self.send(b"\x03")
        self.wait(lambda text: "interrupted" in text.lower() or "cancelled" in text.lower(), "real turn interruption", timeout=20)
        self.observe("real_turn_interruption")
        marker = self.workspace + "/approval-marker"
        prompt = "For this terminal approval test, request explicit human approval to run exactly " + shlex.join(["sh", "-c", "printf approved > " + shlex.quote(marker)]) + ". The sandbox is read-only. Do not use apply_patch or any alternative command. If declined, stop."
        self.send(prompt, paste=True)
        time.sleep(.3)
        self.send(b"\r")
        self.wait(lambda text: "Yes, proceed" in text and "esc" in text.lower() and ("printf" in text or "approval-marker" in text), "real harness approval prompt")
        self.observe("real_harness_approval_prompt")
        self.send(b"\x1b")  # Decline the fixture request; never auto-approve it.
        time.sleep(.3)
        self.send(b"\x03")
        if self.remote:
            self.run(["test", "!", "-e", marker])
        elif Path(marker).exists():
            raise RuntimeError("declined approval wrote the marker")
        self.observe("declined_approval_performs_no_write")
        self.detach()
        if [f["PID"] for f in self.frames()] != self.pids:
            raise RuntimeError("detach replaced a program")

    def close(self):
        self.teardown = True
        if self.child is not None:
            self.detach(crash=True)
        if self.started:
            self.run([self.binary, "stop", "-state", self.state])
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                try:
                    self.frames()
                except (OSError, subprocess.CalledProcessError):
                    break
                time.sleep(.1)
        # Preserve failure diagnostics privately; successful fixtures are removed.
        if not (self.root / "last-screen.txt").exists():
            shutil.rmtree(self.root)


class DesktopHarnessCheck(HarnessCheck):
    """Address only the GUI socket/window created by this fixture."""
    def attach(self):
        for name in ("pane", "socket"):
            (self.root / name).unlink(missing_ok=True)
        self.gui_env = self.env.copy()
        for key in list(self.gui_env):
            if key.startswith(("KITTY_", "WEZTERM_")):
                self.gui_env.pop(key)
        runtime = self.desktop_runtime()
        gui_home = runtime / "home"
        gui_home.mkdir(mode=0o700, exist_ok=True)
        self.gui_env["HOME"] = str(gui_home)
        self.gui_env.update(XDG_RUNTIME_DIR=str(runtime), XDG_DATA_HOME=str(runtime / "data"), XDG_CACHE_HOME=str(runtime / "cache"), XDG_CONFIG_HOME=str(runtime / "config"))
        if self.args.outer == "wezterm":
            self.outer = self.args.outer_bin or shutil.which("wezterm")
            gui = str(Path(self.outer).resolve().with_name("wezterm-gui"))
            config = self.root / "wezterm.lua"
            config.write_text('return { initial_cols=140, initial_rows=40, check_for_updates=false, automatically_reload_config=false, quit_when_all_windows_are_closed=true, window_close_confirmation="NeverPrompt", enable_tab_bar=false, unix_domains={}, ssh_domains={} }\n')
            command = [gui, "--config-file", str(config), "start", "--always-new-process", "--no-auto-connect", "--cwd", str(self.root), "--"]
            pane_var, socket_var = "WEZTERM_PANE", "WEZTERM_UNIX_SOCKET"
        else:
            self.outer = self.args.outer_bin or shutil.which("kitty")
            command = [self.outer, "--config", "NONE", "--listen-on", "unix:" + str(self.root / "kitty.sock"),
                       "-o", "allow_remote_control=socket-only", "-o", "confirm_os_window_close=0", "-o", "enable_audio_bell=no",
                       "-o", "macos_quit_when_last_window_closed=yes",
                       "-o", "remember_window_size=no", "-o", "initial_window_width=140c", "-o", "initial_window_height=40c", "--directory", str(self.root)]
            pane_var, socket_var = "KITTY_WINDOW_ID", "KITTY_LISTEN_ON"
        script = 'umask 077\nfixture=$1\nshift\nprintf "%s\\n" "$' + pane_var + '" > "$fixture/pane"\nprintf "%s\\n" "$' + socket_var + '" > "$fixture/socket"\nexec "$@"'
        self.gui_log = (self.root / "gui.log").open("ab")
        self.gui = subprocess.Popen(command + ["/bin/sh", "-c", script, "check", str(self.root), *self.attach_command()], env=self.gui_env, stdout=self.gui_log, stderr=self.gui_log)
        self.child = self.gui.pid
        deadline = time.monotonic() + 20
        while True:
            self.pane = (self.root / "pane").read_text().strip() if (self.root / "pane").exists() else ""
            self.gui_socket = (self.root / "socket").read_text().strip() if (self.root / "socket").exists() else ""
            if self.pane.isdigit() and self.gui_socket:
                break
            if self.gui.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError("isolated GUI failed to start; private log: " + str(self.root / "gui.log"))
            time.sleep(.1)
        self.gui_env["WEZTERM_UNIX_SOCKET"] = self.gui_socket
        time.sleep(.3)
        self.prepare_view()

    def gui_command(self, name, *args, data=None):
        if self.args.outer == "wezterm":
            command = [self.outer, "cli", "--no-auto-start", name, "--pane-id", self.pane, *args]
        else:
            command = [self.outer, "@", "--to", self.gui_socket, "--use-password", "never", name, "--match", "id:" + self.pane, *args]
        return subprocess.check_output(command, input=data, env=self.gui_env, timeout=15, text=True, stderr=subprocess.PIPE)

    def text(self):
        return self.gui_command("get-text")

    def send(self, data, paste=False):
        if not self.teardown:
            self.guard_quota(self.text())
        if isinstance(data, bytes):
            data = data.decode("utf-8")
        if self.args.outer == "wezterm":
            self.gui_command("send-text", *([] if paste else ["--no-paste"]), "--", data)
        else:
            self.gui_command("send-text", "--stdin", "--bracketed-paste", "enable" if paste else "disable", data=data)

    def drain(self):
        pass

    def ensure_attached(self):
        status = self.gui.poll()
        if status is not None:
            raise RuntimeError("GUI exited while awaiting input, status " + str(status))

    def detach(self, crash=False):
        if crash:
            self.gui.kill()
        else:
            self.send(b"\x1bq")
        try:
            self.gui.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.gui.kill()
            self.gui.wait(timeout=5)
            raise RuntimeError("GUI did not close after detach")
        finally:
            self.gui_log.close()
            self.child = None


def main(pty_check=HarnessCheck, desktop_check=DesktopHarnessCheck):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--authenticated", action="store_true")
    parser.add_argument("--codex-home", default="")
    parser.add_argument("--binary", required=True)
    parser.add_argument("--ssh", default="")
    parser.add_argument("--remote-directory", default="")
    parser.add_argument("--nvim", default="nvim")
    parser.add_argument("--codex-bin", default="codex", help="existing complete Codex installation to test; default uses PATH")
    parser.add_argument("--outer", choices=("pty", "wezterm", "kitty"), default="pty")
    parser.add_argument("--outer-bin", default="")
    parser.add_argument("--model", default="", help="explicit model for this disposable session only; default inherits the normal profile")
    parser.add_argument("--allow-low-quota", action="store_true", help="operator explicitly accepts low-quota notices with the specified test model; choosers/exhaustion still stop input")
    args = parser.parse_args()
    if not args.authenticated or (not args.ssh and not args.codex_home):
        parser.error("explicit --authenticated and existing --codex-home required for a local live-model check")
    if not os.environ.get("SUPERTREE_TEST_SANDBOX"):
        parser.error("run through scripts/test-local.sh")
    if args.allow_low_quota and not args.model:
        parser.error("--allow-low-quota requires an explicit --model")
    if args.ssh and args.outer != "pty":
        parser.error("SSH check owns its local PTY; GUI checks run locally")
    check = pty_check(args) if args.outer == "pty" else desktop_check(args)
    try:
        check.start()
        check.check()
        print(json.dumps({"authenticated_harness": "passed", "transport": "ssh" if args.ssh else args.outer, "checks": check.observations}), flush=True)
    except QuotaBlocked as error:
        print(json.dumps({"authenticated_harness":"blocked","reason":str(error),"checks":check.observations}),flush=True)
        raise SystemExit(2)
    except Exception:
        if check.started:
            try:
                capture = check.root / "last-screen.txt"
                capture.write_text(check.text())
                capture.chmod(0o600)
                print("Private failure screen: " + str(capture), flush=True)
                if hasattr(check, "reader_lock"):
                    with check.reader_lock:
                        tail = check.outer_tail
                    capture = check.root / "outer-tail.bin"
                    capture.write_bytes(tail)
                    capture.chmod(0o600)
            except Exception:
                pass
        raise
    finally:
        check.close()


if __name__ == "__main__":
    main()
