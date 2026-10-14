#!/usr/bin/env python3
"""Opt-in installed-Claude smoke tests. See docs/internal/claude-native-tests.md."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import struct
import subprocess
import tempfile
import time
import unittest
import uuid
import zlib

PROJECT = Path(__file__).resolve().parent.parent
ENABLED = os.environ.get("WI_TEST_CLAUDE_NATIVE") == "1"


def runtime_ready(value, mode):
    runtime = value.get("runtime") or {}
    capability = "headless" if mode == "headless" else "native_tui"
    # Lifecycle readiness can arrive before bootstrap saves capabilities.
    return bool(value.get("online") and runtime.get("readiness") == "ready"
                and runtime.get("capabilities", {}).get(capability))


class ClaudeNativeRunnerTests(unittest.TestCase):
    def test_readiness_waits_for_bootstrap_capabilities(self):
        value = {"online": True, "runtime": {"readiness": "ready", "capabilities": {}}}
        self.assertFalse(runtime_ready(value, "headless"))
        value["runtime"]["capabilities"]["headless"] = True
        self.assertTrue(runtime_ready(value, "headless"))
        self.assertFalse(runtime_ready(value, "tui"))
        value["online"] = False
        self.assertFalse(runtime_ready(value, "headless"))
        self.assertFalse(runtime_ready({"runtime": None}, "headless"))


@unittest.skipUnless(ENABLED, "set WI_TEST_CLAUDE_NATIVE=1 to use an authenticated Claude CLI")
class ClaudeNativeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        claude = os.environ.get("WI_TEST_CLAUDE_EXECUTABLE", "claude")
        cls.claude = shutil.which(claude)
        if not cls.claude or not shutil.which("git"):
            raise unittest.SkipTest("Claude and Git must be available")
        version = subprocess.run([cls.claude, "--version"], capture_output=True,
                                 text=True, timeout=10)
        match = re.search(r"(\d+)\.(\d+)\.(\d+)", version.stdout)
        if version.returncode or not match or tuple(map(int, match.groups())) < (2, 1, 299):
            raise unittest.SkipTest("Claude Code 2.1.299 or later required")
        cls.credentials = Path(os.environ.get("WI_TEST_CLAUDE_CONFIG_DIR",
                               os.environ.get("CLAUDE_CONFIG_DIR", str(Path.home() / ".claude")))) / ".credentials.json"
        if not cls.credentials.is_file() and not os.environ.get("ANTHROPIC_API_KEY"):
            raise unittest.SkipTest("a Claude credential file or ANTHROPIC_API_KEY is required")
        supplied = os.environ.get("WI_TEST_WI_BINARY")
        cls.build = tempfile.TemporaryDirectory(prefix="wi-native-build-", dir="/tmp")
        cls.addClassCleanup(cls.build.cleanup)
        if supplied:
            cls.binary = str(Path(supplied).resolve())
            if not Path(cls.binary).is_file():
                raise unittest.SkipTest("WI_TEST_WI_BINARY does not exist")
        else:
            if not shutil.which("go"):
                raise unittest.SkipTest("Go required unless WI_TEST_WI_BINARY is set")
            cls.binary = str(Path(cls.build.name) / "wi")
            subprocess.run(["go", "build", "-o", cls.binary, "./cmd/wi"], cwd=PROJECT,
                           check=True, timeout=120, capture_output=True)

    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="wcn-", dir="/tmp")).resolve()
        self.root.chmod(0o700)
        self.addCleanup(self.cleanup)
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith(("WI_", "PI_", "TMUX", "CLAUDE", "GIT_", "DIRENV_", "ANTHROPIC"))}
        # An explicit API key is optional. Never write the environment to logs.
        if os.environ.get("ANTHROPIC_API_KEY"):
            self.env["ANTHROPIC_API_KEY"] = os.environ["ANTHROPIC_API_KEY"]
        for key, suffix in (("HOME", "home"), ("XDG_CONFIG_HOME", "config"),
                            ("XDG_DATA_HOME", "data"), ("XDG_STATE_HOME", "state"),
                            ("XDG_RUNTIME_DIR", "runtime"), ("TMUX_TMPDIR", "tmux")):
            p = self.root / suffix
            p.mkdir(mode=0o700)
            self.env[key] = str(p)
        self.env.update(TMPDIR="/tmp", CLAUDE_CONFIG_DIR=str(self.root / "home/.claude"),
                        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1")
        self.config = self.root / "home/.claude"
        self.config.mkdir(mode=0o700)
        if self.credentials.is_file() and not os.environ.get("ANTHROPIC_API_KEY"):
            shutil.copyfile(self.credentials, self.config / ".credentials.json")
            (self.config / ".credentials.json").chmod(0o600)
        self.write_settings()
        wi_config = self.root / "config/wi"
        wi_config.mkdir(mode=0o700)
        (wi_config / "config.toml").write_text(
            'default_agent = "claude"\n[agents.claude]\ndriver = "claude"\n'
            '[agents.claude.config]\nexecutable = ' + json.dumps(self.claude) +
            '\nmodel = ' + json.dumps(os.environ.get("WI_TEST_CLAUDE_MODEL", "haiku")) + '\n')
        source = self.root / "source"
        source.mkdir(mode=0o700)
        for args in (("init", "-b", "main"), ("config", "user.name", "Native test"),
                     ("config", "user.email", "native-test@example.invalid")):
            self.run_process(["git", *args], cwd=source)
        (source / "fixture.txt").write_text("Synthetic native-driver test repository.\n")
        self.run_process(["git", "add", "fixture.txt"], cwd=source)
        self.run_process(["git", "commit", "-m", "fixture"], cwd=source)
        self.wi("repo", "add", "probe", "--from", str(source), "--branch", "main", "--local")
        self.description = self.root / "task.md"
        self.description.write_text("Native Claude driver smoke test. Follow only explicit test prompts. "
                                    "Do not read or modify repository files or manage Wi.\n")
        self.item = None
        self.conversation = None
        self.last_command = None
        print("Native test evidence:", self.root, flush=True)

    def write_settings(self, projects=None):
        # These are fixture defaults, not answers to running UI dialogs. The test
        # never sends keys to authentication, trust, or onboarding prompts.
        settings = {"hasCompletedOnboarding": True, "bypassPermissionsModeAccepted": True,
                    "theme": "dark", "projects": projects or {}}
        path = self.config / ".claude.json"
        path.write_text(json.dumps(settings))
        path.chmod(0o600)

    def run_process(self, args, cwd=None, timeout=30):
        p = subprocess.run(args, env=self.env, cwd=cwd or self.root, text=True,
                           capture_output=True, timeout=timeout)
        if p.returncode:
            # Native/UI errors remain in the private evidence directory.
            (self.root / "failure.log").write_text(p.stdout + p.stderr)
            self.fail(f"command failed; inspect {self.root / 'failure.log'}")
        return p.stdout

    def wi(self, *args):
        return json.loads(self.run_process([self.binary, *args, "--json"]))

    def poll(self, fn, message, timeout=45):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            value = fn()
            if value:
                return value
            time.sleep(.1)
        self.fail(f"{message}; evidence in {self.root}")

    def status(self):
        return self.wi("agent", "runtime", "status", "--item", self.item)

    def events(self):
        result = self.wi("agent", "monitor", "--item", self.item, "--limit", "0")
        (self.root / "events.json").write_text(json.dumps(result, indent=2))
        return result["events"]

    def event(self, kind, command=None):
        return self.poll(lambda: next((e for e in self.events() if e["type"] == kind
                         and (command is None or e.get("command_id") == command)), None), kind)

    def content(self):
        status = self.status()
        rid = status["runtime"]["id"]
        filename = hashlib.sha256(("content:" + hashlib.sha256(rid.encode()).hexdigest()[:16]
                                   + ".sock").encode()).hexdigest()[:16] + ".sock"
        paths = list((self.root / "runtime").rglob(filename))
        self.assertEqual(len(paths), 1)
        with socket.socket(socket.AF_UNIX) as sock:
            sock.settimeout(3)
            sock.connect(str(paths[0]))
            sock.sendall((json.dumps({"protocol_version": 1, "runtime_id": rid,
                                     "work_item_id": self.item}) + "\n").encode())
            data = b""
            while b"\n" not in data:
                block = sock.recv(65536)
                self.assertTrue(block, "content socket closed without a snapshot")
                data += block
                self.assertLess(len(data), 2 << 20)
        value = json.loads(data)
        self.assertEqual(value["runtime_id"], rid)
        self.assertEqual(value["work_item_id"], self.item)
        # Keep the last private snapshot for diagnosing a failed native probe.
        snapshot = self.root / "last-content.json"
        snapshot.write_text(json.dumps(value))
        snapshot.chmod(0o600)
        return value

    def create(self):
        result = self.wi("new", "--repo", "probe", "--agent", "claude", "--title",
                         "Native Claude test", "--desc-file", str(self.description))
        self.item = result["manifest"]["id"]
        self.slug = result["manifest"]["slug"]
        # Materialize without launching an agent so the synthetic fixture's
        # private trust configuration can be set before the first TUI starts.
        self.wi("workspace", "ensure", "--item", self.item)
        manifest = self.wi("show", "--item", self.item)["manifest"]
        path = manifest["checkout"]["claim"]["path"]
        self.write_settings({str(Path(path).resolve()): {"hasTrustDialogAccepted": True,
                                                       "hasCompletedProjectOnboarding": True}})

    def start(self, mode):
        self.wi("start", "--agent-mode", mode, self.slug)
        self.mode = mode
        def ready():
            value = self.status()
            return value if runtime_ready(value, mode) else None
        status = self.poll(ready, f"{mode} readiness")
        self.assertTrue(status["runtime"]["capabilities"]["headless" if mode == "headless" else "native_tui"])
        current = status["runtime"]["conversation_id"]
        if self.conversation is None:
            self.conversation = current
        self.assertEqual(current, self.conversation, "mode switch forked the conversation")
        return status["runtime"]["id"]

    def stop(self):
        self.wi("agent", "runtime", "stop", "--item", self.item, "--force")
        self.poll(lambda: not self.status()["online"], "runtime offline", timeout=15)

    def send(self, text, *options, settle=True):
        result = self.wi("agent", "control", "send", "--item", self.item, *options, text)
        command = result["command"]["id"]
        self.last_command = command
        receipt = self.event("command.accepted", command)
        if settle:
            if self.mode == "tui":
                # TUI Stop hooks carry turn identity internally, but ordinary
                # settlement events currently omit the Wi command identity.
                outcome = self.poll(lambda: next((e for e in self.events()
                    if e["type"] == "agent.settled" and e["runtime_id"] == receipt["runtime_id"]
                    and e["timestamp"] >= receipt["timestamp"]), None), "TUI settlement")
            else:
                outcome = self.event("agent.settled", command)
            self.assertFalse(outcome.get("failed", False), "native turn failed")
        return command

    def answer(self, expected):
        self.poll(lambda: expected in self.content()["text"], f"reply {expected}")

    def test_headless(self):
        self.create()
        token = "WI_MEMORY_" + uuid.uuid4().hex[:12]
        self.start("headless")
        self.send(f"Remember {token}. Reply SAVED. No tools or file reads.")
        self.send("What value did I ask you to remember? Reply only that value. No tools or file reads.")
        self.answer(token)
        first = self.status()["runtime"]["id"]
        self.stop()
        self.assertNotEqual(self.start("headless"), first)
        self.send("What value did I ask you to remember? Reply only that value. No tools or file reads.")
        self.answer(token)
        image = self.root / "green.png"
        def chunk(tag, data):
            return struct.pack("!I", len(data)) + tag + data + struct.pack("!I", zlib.crc32(tag + data) & 0xffffffff)
        image.write_bytes(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack("!2I5B", 32, 32, 8, 2, 0, 0, 0))
                          + chunk(b"IDAT", zlib.compress((b"\x00" + b"\x00\xff\x00" * 32) * 32)) + chunk(b"IEND", b""))
        self.send("What color is the attached image? Reply only GREEN, RED or BLUE. No tools or file reads.",
                  "--image", str(image))
        self.answer("GREEN")
        command = self.send("Write a 400-word story about a robot in one paragraph without newlines. "
                            "No tools or file reads.", settle=False)
        lengths = set()
        def streamed():
            snapshot = self.content()
            if snapshot.get("activity") == "Assistant responding" and snapshot["text"]:
                lengths.add(len(snapshot["text"]))
            return any(e["type"] == "agent.settled" and e.get("command_id") == command for e in self.events())
        self.poll(streamed, "streamed turn settlement")
        self.assertGreater(len(lengths), 3, "no incremental API output observed")
        command = self.send("Use Bash to run exactly sleep 20, then reply DONE. "
                            "Do not read or modify files or use other tools.", settle=False)
        self.poll(lambda: any(e["type"] == "tool.started" and e.get("tool_name") == "Bash"
                             for e in self.events()), "Bash started")
        steer = self.send("Reply CANCELLED_STEER after the running tool. No tools.", settle=False)
        follow = self.wi("agent", "control", "send", "--item", self.item, "--follow-up",
                         "Reply CANCELLED_FOLLOWUP. No tools.")["command"]["id"]
        abort = self.wi("agent", "control", "abort", "--item", self.item)["command"]["id"]
        self.event("command.accepted", abort)
        self.event("agent.settled", command)
        for cid in (steer, follow):
            self.assertEqual(self.event("command.rejected", cid).get("delivery_state"), "cancelled")
        self.send("Reply exactly AFTER_ABORT_OK. No tools or file reads.")
        self.answer("AFTER_ABORT_OK")
        self.assertFalse(list((self.root / "tmux").rglob("default")), "headless created tmux")
        print("PASS persistent turns, resume, images, API deltas, tools, abort and queue cancellation", flush=True)

    @unittest.skipUnless(shutil.which("tmux"), "tmux required for mode switching")
    def test_mode_switching(self):
        self.create()
        token = "WI_SWITCH_" + uuid.uuid4().hex[:12]
        ids = [self.start("tui")]
        self.send(f"Remember {token}. Reply SAVED. No tools or file reads.")
        self.stop()
        ids.append(self.start("headless"))
        self.send("What value did I ask you to remember? Reply only the value. No tools or file reads.")
        self.answer(token)
        self.send("Remember the second value SWITCH_BACK_OK. Reply SAVED. No tools or file reads.")
        self.stop()
        ids.append(self.start("tui"))
        self.send("What were the first and second values I asked you to remember? "
                  "Reply only those two values. No tools or file reads.")
        self.answer(token)
        self.answer("SWITCH_BACK_OK")
        self.assertEqual(len(set(ids)), 3)
        print("PASS TUI -> headless -> TUI, one conversation, both remembered values", flush=True)

    def cleanup(self):
        # Even setup/partial-outcome failures may have created an item runtime.
        # Discover only this fixture's items; never query the operator's roots.
        try:
            env = getattr(self, "env", None)
            if env:
                failed_cleanup = False
                for directory in (self.root / "data/wi/items").glob("*"):
                    subprocess.run([self.binary, "agent", "runtime", "stop", "--item", directory.name, "--force"],
                                   env=env, cwd=self.root, capture_output=True, timeout=20)
                    end = time.monotonic() + 15
                    while True:
                        status = subprocess.run([self.binary, "agent", "runtime", "status", "--item",
                                                 directory.name, "--json"], env=env, cwd=self.root,
                                                capture_output=True, text=True, timeout=5)
                        if status.returncode == 0 and not json.loads(status.stdout)["online"]:
                            break
                        if time.monotonic() >= end:
                            failed_cleanup = True
                            break
                        time.sleep(.1)
                if shutil.which("tmux"):
                    subprocess.run(["tmux", "kill-server"], env=env, cwd=self.root, capture_output=True, timeout=5)
                subprocess.run([self.binary, "daemon", "stop"], env=env, cwd=self.root, capture_output=True, timeout=10)
                self.assertFalse(failed_cleanup, f"owned runtime did not stop; evidence in {self.root}")
        finally:
            (self.root / "home/.claude/.credentials.json").unlink(missing_ok=True)
            # Native logs/transcripts remain private for diagnosis. No secrets
            # are printed and the temporary credential copy is always removed.


if __name__ == "__main__":
    unittest.main()
