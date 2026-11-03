import io
import hashlib
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from demo_capture import response_for, STATES
from update_release import ROOT, TAG, main, update_sources, verify_archive


class ReleaseVerificationTests(unittest.TestCase):
    def test_older_manual_release_is_rejected_before_download(self):
        with patch("sys.argv", ["update_release.py", "--tag", "v0.6.2"]), \
             patch("update_release.latest_release", return_value={"tag_name": "v0.6.3"}), \
             patch("update_release.capture") as generate:
            with self.assertRaisesRegex(ValueError, "older release"):
                main()
            generate.assert_not_called()

    def test_superseded_workflow_is_skipped_before_download(self):
        with patch("sys.argv", ["update_release.py", "--expected-commit", "a" * 40]), \
             patch("update_release.latest_release", return_value={"tag_name": "v0.6.3"}), \
             patch("update_release.run", return_value="b" * 40), \
             patch("update_release.capture") as generate:
            main()
            generate.assert_not_called()

    def test_stable_tags_exclude_prereleases_and_shell_syntax(self):
        for tag in ("v0.6.3", "v1.20.300"):
            self.assertIsNotNone(TAG.fullmatch(tag))
        for tag in ("v0.6.4-rc.1", "main", "v0.6.4\n", "v1.2.3;whoami"):
            self.assertIsNone(TAG.fullmatch(tag))

    def archive(self, root, *, link=False, duplicate=False):
        archive = root / "packetcode-linux-amd64.tar.gz"
        with tarfile.open(archive, "w:gz") as bundle:
            for _ in range(2 if duplicate else 1):
                entry = tarfile.TarInfo("packetcode")
                if link:
                    entry.type = tarfile.SYMTYPE
                    entry.linkname = "/outside"
                    bundle.addfile(entry)
                else:
                    entry.size = 7
                    bundle.addfile(entry, io.BytesIO(b"release"))
        checksums = root / "checksums.txt"
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        checksums.write_text(digest + "  " + archive.name + "\n")
        return archive, checksums

    def test_extracts_verified_binary_only(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive, checksums = self.archive(root)
            verify_archive(archive, checksums, root / "binary")
            self.assertEqual((root / "binary").read_bytes(), b"release")
            self.assertTrue((root / "binary").stat().st_mode & 0o100)

    def test_corrupt_archive_never_creates_executable(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive, checksums = self.archive(root)
            archive.write_bytes(archive.read_bytes() + b"changed")
            with self.assertRaisesRegex(ValueError, "checksum"):
                verify_archive(archive, checksums, root / "binary")
            self.assertFalse((root / "binary").exists())

    def test_linked_or_duplicate_executables_are_rejected(self):
        for options in ({"link": True}, {"duplicate": True}):
            with self.subTest(options=options), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                archive, checksums = self.archive(root, **options)
                with self.assertRaisesRegex(ValueError, "regular"):
                    verify_archive(archive, checksums, root / "binary")


class CaptureSourceTests(unittest.TestCase):
    def test_patch_requires_the_running_app_to_read_the_example_file(self):
        messages = [{"role": "user", "content": "Read README.md, then add a curl example."}]
        self.assertEqual(response_for(messages)["tool_calls"][0]["function"]["name"], "read_file")
        with self.assertRaises(ValueError):
            response_for(messages + [{"role": "tool", "content": "read_file failed"}])
        proposal = response_for(messages + [{"role": "tool", "content": "# Beacon\nactual file contents"}])
        self.assertEqual(proposal["tool_calls"][0]["function"]["name"], "patch_file")

    def test_every_release_updates_labels_and_geometry_without_changing_install_examples(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("docs/images/captures.json", "site/index.html", "README.md", "docs/website.md"):
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / name, root / name)
            output = root / "output"
            output.mkdir()
            for state in STATES:
                (output / f"packetcode-{state}.jpg").write_bytes(b"reviewed image")
            original_install = (root / "README.md").read_text().split("## Install", 1)[1]
            crops = {state: {"x": 200, "y": 20, "width": 880, "height": 600} for state in STATES}
            for version in ("8.1.0", "8.2.0"):
                manifest = {"version": version, "captured_on": "2026-11-03", "platform": "linux-amd64",
                            "commit": "a" * 40, "release_archive_sha256": "b" * 64}
                update_sources(root, manifest, output, crops)
                html = (root / "site/index.html").read_text()
                self.assertIn("PacketCode v" + version, html)
                self.assertNotIn("live Codex choices", html)
                self.assertNotIn("real Codex responses", html)
                self.assertEqual(html.count('data-screenshot-crop="200,20,880,600"'), 4)
                self.assertEqual(html.count("--shot-width:145.454545%"), 4)
                readme = (root / "README.md").read_text()
                self.assertIn("released v" + version + " executable", readme)
                self.assertEqual(readme.split("## Install", 1)[1], original_install)
                self.assertIn("local demo provider", readme)
                self.assertIn("not a live AI service", (root / "docs/images/README.md").read_text())
                self.assertEqual(json.loads((root / "docs/images/captures.json").read_text())["version"], version)

    def test_bad_crop_fails_before_replacing_any_assets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("docs/images/captures.json", "site/index.html"):
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / name, root / name)
            before = (root / "site/index.html").read_bytes()
            with self.assertRaisesRegex(ValueError, "crop"):
                update_sources(root, {"version": "9.0.0"}, root, {"chat": {"x": 0, "y": 0, "width": 2000, "height": 720}})
            self.assertEqual((root / "site/index.html").read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
