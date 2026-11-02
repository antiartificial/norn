import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


def load(name):
    path = Path(__file__).with_name(name)
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


builder = load("mini-m5-historical-bundle")
prepare = load("mini-m5-guest-prepare")


class HistoricalBundleTests(unittest.TestCase):
    def test_bundle_installs_only_reviewed_tools(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "historical.tar"
            builder.build(Path(__file__).parent, archive)
            with patch.object(prepare.os, "chown"):
                prepare.install_historical_bundle(archive, root / "installed")
            self.assertEqual({path.name for path in (root / "installed").iterdir()},
                             set(builder.TOOLS))

    def test_modified_tool_blocks_bundle_before_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in builder.TOOLS:
                (root / name).write_bytes((Path(__file__).parent / name).read_bytes())
            first = next(iter(builder.TOOLS))
            (root / first).write_bytes(b"modified")
            output = root / "historical.tar"
            with self.assertRaisesRegex(ValueError, "differs"):
                builder.build(root, output)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
