"""Offline regression of the actual command helper; no Docker or database calls."""
import ast
import subprocess
import sys
import unittest
from pathlib import Path

RUNNER = Path(__file__).with_name("run-isolated.py")


class RunnerTests(unittest.TestCase):
    def setUp(self):
        # Load the exact helper's AST without executing the CLI's top-level
        # arguments/lifecycle. No duplicate implementation and no live target.
        tree = ast.parse(RUNNER.read_text())
        node = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "run")
        module = ast.Module(body=[node], type_ignores=[])
        namespace = {"subprocess": subprocess}
        exec(compile(module, str(RUNNER), "exec"), namespace)
        self.run_command = namespace["run"]

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
