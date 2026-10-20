"""Offline regression of the actual command helper; no Docker or database calls."""
import ast
import hashlib
import json
import queue
import subprocess
import sys
import tempfile
import threading
import types
import unittest
from pathlib import Path
from unittest.mock import Mock

RUNNER = Path(__file__).with_name("run-isolated.py")


class RunnerTests(unittest.TestCase):
    def setUp(self):
        # Load the exact helper's AST without executing the CLI's top-level
        # arguments/lifecycle. No duplicate implementation and no live target.
        tree = ast.parse(RUNNER.read_text())
        nodes = [n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name in ("run", "phase_budget", "measure")]
        module = ast.Module(body=nodes, type_ignores=[])
        namespace = {"subprocess": subprocess}
        exec(compile(module, str(RUNNER), "exec"), namespace)
        self.run_command = namespace["run"]
        self.phase_budget = namespace["phase_budget"]
        self.namespace = namespace

    def test_only_capacity50_receives_120_minute_budget(self):
        self.assertEqual(self.phase_budget("capacity50"), ("120m", 7200))
        for phase in ("smoke", "capacity20", "fault"):
            with self.subTest(phase=phase):
                self.assertEqual(self.phase_budget(phase), ("20m", 1500))

    def test_capacity50_wall_abort_retains_failure_and_bounds_owned_cleanup(self):
        for actual_nonce in ("owned", "foreign"):
            with self.subTest(nonce=actual_nonce), tempfile.TemporaryDirectory() as directory:
                proc = Mock()
                proc.stdout = iter(())
                proc.poll.return_value = -9
                metadata = {"Config": {"Labels": {"indexcore.r4.nonce": actual_nonce}}}
                run = Mock(side_effect=[types.SimpleNamespace(returncode=0, stdout=json.dumps([metadata])), types.SimpleNamespace(returncode=0)])
                fake_subprocess = types.SimpleNamespace(Popen=Mock(return_value=proc), run=run, PIPE=subprocess.PIPE, STDOUT=subprocess.STDOUT, DEVNULL=subprocess.DEVNULL, TimeoutExpired=subprocess.TimeoutExpired)
                clock = Mock(side_effect=[0] + [7201] * 20)
                persist = Mock()
                self.namespace.update({"W": Path(directory), "owned": Mock(), "container_args": lambda *args: [], "time": types.SimpleNamespace(monotonic=clock, time_ns=lambda: 1), "queue": queue, "threading": threading, "subprocess": fake_subprocess, "clean": lambda value: value, "persist": persist, "emit": Mock(), "source_digest": lambda: "frozen-fixture", "hashlib": hashlib, "json": json})
                with self.assertRaisesRegex(RuntimeError, "phase wall-time budget exceeded"):
                    self.namespace["measure"]({"network": "owned-r4", "nonce": "owned"}, [], {}, "capacity50", 7200)
                receipt = persist.call_args.args[1]
                self.assertEqual(receipt["status"], "FAIL_OR_BLOCKED")
                self.assertEqual(receipt["cleanup_confirmed"], actual_nonce == "owned")
                self.assertEqual(receipt["wall_budget_seconds"], 7200)
                self.assertEqual(receipt["source_fingerprint"], "frozen-fixture")
                self.assertEqual(run.call_count, 2 if actual_nonce == "owned" else 1)
                for call in run.call_args_list:
                    self.assertEqual(call.kwargs["timeout"], 60)
                if actual_nonce == "owned":
                    self.assertEqual(run.call_args_list[1].args[0], ["docker", "stop", "--time", "0", "owned-r4-runner"])
                proc.wait.assert_called_once_with(timeout=15)
                self.assertLessEqual(60 + 60 + 15, 300)

    def test_cold_pull_progress_cannot_pollute_container_identity(self):
        cid = "a" * 64
        code = "import sys; print('Pulling image, progress only',file=sys.stderr); print('" + cid + "')"
        self.assertEqual(self.run_command([sys.executable, "-c", code]), cid)

    def test_failed_command_does_not_export_output(self):
        marker = "DO_NOT_LOG_SYNTHETIC_CREDENTIAL"
        code = "import sys; print('" + marker + "'); print('" + marker + "',file=sys.stderr); sys.exit(1)"
        with self.assertRaises(RuntimeError) as result:
            self.run_command([sys.executable, "-c", code])
        self.assertNotIn(marker, str(result.exception))
        self.assertNotIn(code, str(result.exception))

    def test_optimized_python_refuses_before_workspace_access(self):
        result = subprocess.run([sys.executable, "-O", str(RUNNER), "status", "--workspace", "/"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stderr.strip(), "REFUSED: R4 safety requires non-optimized Python")
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
