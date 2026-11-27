import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from backup_files import extract, pack


class BackupFilesTest(unittest.TestCase):
    def test_round_trip_and_no_links(self):
        with tempfile.TemporaryDirectory() as base:
            root = Path(base)
            source = root / "source"
            (source / "secrets").mkdir(parents=True)
            (source / "secrets" / "master.key").write_bytes(b"k" * 32)
            archive = root / "data.tar"
            pack(source, archive)
            destination = root / "restored"
            extract(archive, destination)
            self.assertEqual((destination / "secrets" / "master.key").read_bytes(), b"k" * 32)
            self.assertEqual((destination / "secrets" / "master.key").stat().st_mode & 0o777, 0o600)
            (source / "linked").symlink_to(source / "secrets", target_is_directory=True)
            with self.assertRaises(ValueError):
                pack(source, root / "linked.tar")

    def test_rejects_traversal_before_extracting(self):
        with tempfile.TemporaryDirectory() as base:
            root = Path(base)
            archive = root / "bad.tar"
            with tarfile.open(archive, "w") as output:
                body = b"bad"
                entry = tarfile.TarInfo("../outside")
                entry.size = len(body)
                output.addfile(entry, io.BytesIO(body))
            with self.assertRaises(ValueError):
                extract(archive, root / "restored")
            self.assertFalse((root / "restored").exists())
            self.assertFalse((root / "outside").exists())


if __name__ == "__main__":
    unittest.main()
