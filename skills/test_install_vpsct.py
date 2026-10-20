"""Installer tests run only in private temporary directories, without network."""

import base64
import contextlib
import hashlib
import importlib.util
import io
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import warnings
import zipfile

spec = importlib.util.spec_from_file_location("vpsct_installer", Path(__file__).with_name("install_vpsct.py"))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)
FILES = {
    "SKILL.md": b"---\nname: vpsct\ndescription: Fixture only.\n---\n# VpsCT\n",
    "scripts/vpsct.py": b"print('fixture')\n",
    "references/api.md": b"# API fixture\n",
}


def package(files=None, entries=None):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        if entries is None:
            entries = [("vpsct/" + name, body) for name, body in (files or FILES).items()]
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", UserWarning)
            for name, body in entries:
                archive.writestr(name, body)
    data = stream.getvalue()
    return base64.b64encode(data).decode("ascii"), hashlib.sha256(data).hexdigest()


class InstallerTest(unittest.TestCase):
    def setUp(self):
        # resolve() only canonicalizes the isolated test root, never a user's
        # real skill directory; it avoids macOS's /var -> /private/var alias.
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.parent = self.root / "skills"

    def tearDown(self):
        self.temp.cleanup()

    def invoke(self, *args, files=None, encoded=None, sha=None, environ=None):
        if encoded is None:
            encoded, sha = package(files)
        stdout, stderr = io.StringIO(), io.StringIO()
        with patch.object(installer, "BUNDLE_BASE64", encoded), patch.object(installer, "BUNDLE_SHA256", sha):
            with patch.dict(os.environ, environ or {}), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                result = installer.main(list(args) or ["--dir", str(self.parent)])
        return result, stdout.getvalue(), stderr.getvalue()

    def contents(self, target=None):
        target = target or self.parent / "vpsct"
        return {p.relative_to(target).as_posix(): p.read_bytes() for p in target.rglob("*") if p.is_file()}

    def test_install_idempotency_and_private_files(self):
        self.assertEqual(self.invoke()[0], 0)
        self.assertEqual(self.contents(), FILES)
        before = (self.parent / "vpsct" / "SKILL.md").stat().st_mtime_ns
        self.assertEqual(self.invoke()[0], 0)
        self.assertEqual((self.parent / "vpsct" / "SKILL.md").stat().st_mtime_ns, before)
        for path in (self.parent / "vpsct").rglob("*"):
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o700 if path.is_dir() else 0o600)
        self.assertFalse(list(self.parent.glob(".vpsct-stage-*")))

    def test_explicit_update_replaces_only_recognized_skill(self):
        self.assertEqual(self.invoke()[0], 0)
        changed = dict(FILES, **{"references/api.md": b"# New API fixture\n"})
        self.assertEqual(self.invoke(files=changed)[0], 1)
        self.assertEqual(self.contents(), FILES)
        self.assertEqual(self.invoke("--dir", str(self.parent), "--update", files=changed)[0], 0)
        self.assertEqual(self.contents(), changed)

    def test_invalid_hash_and_oversized_archive_do_not_create_destination(self):
        encoded, sha = package()
        self.assertEqual(self.invoke(encoded=encoded, sha="0" * 64)[0], 1)
        self.assertFalse(self.parent.exists())
        self.assertEqual(self.invoke(encoded="not base64", sha=sha)[0], 1)
        self.assertFalse(self.parent.exists())
        with patch.object(installer, "MAX_ZIP_BYTES", 1):
            self.assertEqual(self.invoke()[0], 1)
        self.assertFalse(self.parent.exists())

    def test_non_zip_and_wrong_skill_identity_do_not_create_destination(self):
        data = b"not a ZIP archive"
        self.assertEqual(self.invoke(encoded=base64.b64encode(data).decode(),
                                     sha=hashlib.sha256(data).hexdigest())[0], 1)
        wrong = dict(FILES, **{"SKILL.md": b"---\nname: another-product\n---\n"})
        self.assertEqual(self.invoke(files=wrong)[0], 1)
        self.assertFalse(self.parent.exists())

    def test_zip_whitelist_rejects_path_traversal_duplicate_and_extra_file(self):
        original = [("vpsct/" + name, body) for name, body in FILES.items()]
        bad_entries = (
            original + [("vpsct/unexpected.txt", b"x")],
            original[:2] + [("vpsct/SKILL.md", FILES["SKILL.md"])],
            original[:2] + [("vpsct/../../escaped", b"x")],
            original[:2] + [("/vpsct/references/api.md", b"x")],
            original[:2] + [("vpsct\\references\\api.md", b"x")],
        )
        for entries in bad_entries:
            with self.subTest(entries=[name for name, _ in entries]):
                encoded, sha = package(entries=entries)
                self.assertEqual(self.invoke(encoded=encoded, sha=sha)[0], 1)
                self.assertFalse(self.parent.exists())
        self.assertFalse((self.root / "escaped").exists())

    def test_zip_symlink_and_oversized_inflated_file_are_rejected(self):
        entry = zipfile.ZipInfo("vpsct/references/api.md")
        entry.create_system = 3
        entry.external_attr = (stat.S_IFLNK | 0o777) << 16
        encoded, sha = package(entries=[("vpsct/SKILL.md", FILES["SKILL.md"]),
                                        ("vpsct/scripts/vpsct.py", FILES["scripts/vpsct.py"]), (entry, b"../../outside")])
        self.assertEqual(self.invoke(encoded=encoded, sha=sha)[0], 1)
        with patch.object(installer, "MAX_FILE_BYTES", 8):
            self.assertEqual(self.invoke()[0], 1)
        self.assertFalse(self.parent.exists())

    def test_symlink_destination_ancestor_and_lock_are_rejected(self):
        real = self.root / "real"
        real.mkdir()
        self.parent.symlink_to(real, target_is_directory=True)
        self.assertEqual(self.invoke()[0], 1)
        self.assertEqual(list(real.iterdir()), [])
        self.parent.unlink()
        self.parent.mkdir()
        (self.parent / "vpsct").symlink_to(real, target_is_directory=True)
        self.assertEqual(self.invoke("--dir", str(self.parent), "--update")[0], 1)
        self.assertTrue((self.parent / "vpsct").is_symlink())
        self.assertEqual(list(real.iterdir()), [])
        (self.parent / "vpsct").unlink()
        (self.parent / ".vpsct-install.lock").unlink()
        (self.parent / ".vpsct-install.lock").symlink_to(real / "lock")
        self.assertEqual(self.invoke()[0], 1)
        self.assertFalse((real / "lock").exists())

    def test_update_never_deletes_arbitrary_directory_or_extra_content(self):
        target = self.parent / "vpsct"
        target.mkdir(parents=True)
        (target / "keep.txt").write_text("preserve me")
        self.assertEqual(self.invoke("--dir", str(self.parent), "--update")[0], 1)
        self.assertEqual((target / "keep.txt").read_text(), "preserve me")
        (target / "keep.txt").unlink()
        target.rmdir()
        self.assertEqual(self.invoke()[0], 0)
        (target / "custom.txt").write_text("preserve custom")
        self.assertEqual(self.invoke("--dir", str(self.parent), "--update")[0], 1)
        self.assertEqual((target / "custom.txt").read_text(), "preserve custom")

    def test_update_failure_restores_old_skill(self):
        self.assertEqual(self.invoke()[0], 0)
        changed = dict(FILES, **{"references/api.md": b"# New fixture\n"})
        original_rename = installer.os.rename

        def rename(source, target, **kwargs):
            if Path(source).name == "vpsct" and Path(source).parent.name.startswith(".vpsct-stage-"):
                raise OSError("simulated publication failure")
            return original_rename(source, target, **kwargs)

        with patch.object(installer.os, "rename", side_effect=rename):
            code, _, error = self.invoke("--dir", str(self.parent), "--update", files=changed)
        self.assertEqual(code, 1)
        self.assertIn("原 skill 已恢复", error)
        self.assertEqual(self.contents(), FILES)
        self.assertFalse(list(self.parent.glob(".vpsct-stage-*")))

    def test_rollback_failure_preserves_recoverable_backup(self):
        self.assertEqual(self.invoke()[0], 0)
        changed = dict(FILES, **{"references/api.md": b"# New fixture\n"})
        original_rename = installer.os.rename

        def rename(source, target, **kwargs):
            if "dst_dir_fd" in kwargs:
                raise OSError("simulated publication and rollback failure")
            return original_rename(source, target, **kwargs)

        with patch.object(installer.os, "rename", side_effect=rename):
            code, _, error = self.invoke("--dir", str(self.parent), "--update", files=changed)
        self.assertEqual(code, 1)
        backups = list(self.parent.glob(".vpsct-stage-*/previous-vpsct"))
        self.assertEqual(len(backups), 1)
        self.assertEqual(self.contents(backups[0]), FILES)
        self.assertIn(str(backups[0]), error)

    def test_interrupt_during_update_restores_old_skill(self):
        self.assertEqual(self.invoke()[0], 0)
        changed = dict(FILES, **{"references/api.md": b"# New fixture\n"})
        original_rename = installer.os.rename

        def rename(source, target, **kwargs):
            if Path(source).name == "vpsct" and Path(source).parent.name.startswith(".vpsct-stage-"):
                raise KeyboardInterrupt()
            return original_rename(source, target, **kwargs)

        with patch.object(installer.os, "rename", side_effect=rename):
            code, _, error = self.invoke("--dir", str(self.parent), "--update", files=changed)
        self.assertEqual(code, 1)
        self.assertIn("原 skill 已恢复", error)
        self.assertEqual(self.contents(), FILES)

    def test_fifo_inside_existing_skill_is_rejected_without_blocking(self):
        self.assertEqual(self.invoke()[0], 0)
        entry = self.parent / "vpsct" / "references" / "api.md"
        entry.unlink()
        os.mkfifo(entry)
        self.assertEqual(self.invoke("--dir", str(self.parent), "--update")[0], 1)
        self.assertTrue(stat.S_ISFIFO(entry.lstat().st_mode))

    def test_client_paths_and_custom_dir_without_real_home(self):
        fake_home = self.root / "home"
        codex_root = self.root / "custom-codex"
        with patch.object(installer.Path, "home", return_value=fake_home):
            self.assertEqual(self.invoke("--client", "codex", environ={"CODEX_HOME": str(codex_root)})[0], 0)
            self.assertEqual(self.contents(codex_root / "skills" / "vpsct"), FILES)
            self.assertEqual(self.invoke("--client", "claude")[0], 0)
            self.assertEqual(self.contents(fake_home / ".claude" / "skills" / "vpsct"), FILES)
            with patch.dict(os.environ, {"CODEX_HOME": ""}):
                self.assertEqual(installer.destination("codex", None), fake_home / ".codex" / "skills")
        self.assertEqual(installer.destination("claude", str(self.parent)), self.parent)

    def test_rendered_standalone_template_never_captures_key(self):
        encoded, sha = package()
        template = Path(__file__).with_name("install_vpsct.py").read_text()
        rendered = template.replace("__VPSCT_SKILL_ZIP_BASE64__", encoded).replace("__VPSCT_SKILL_ZIP_SHA256__", sha)
        program = self.root / "install.py"
        program.write_text(rendered)
        result = subprocess.run([sys.executable, str(program), "--dir", str(self.parent)],
                                env=dict(os.environ, VPSCT_API_KEY="fixture_key_never_store"),
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("fixture_key_never_store", result.stdout + result.stderr)
        self.assertEqual(self.contents(), FILES)
        self.assertNotIn(b"fixture_key_never_store", b"".join(self.contents().values()))

    def test_concurrent_standalone_installs_publish_one_complete_package(self):
        encoded, sha = package()
        template = Path(__file__).with_name("install_vpsct.py").read_text()
        rendered = template.replace("__VPSCT_SKILL_ZIP_BASE64__", encoded).replace("__VPSCT_SKILL_ZIP_SHA256__", sha)
        program = self.root / "install.py"
        program.write_text(rendered)
        command = [sys.executable, str(program), "--dir", str(self.parent)]
        processes = [subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                     for _ in range(2)]
        results = []
        try:
            for process in processes:
                stdout, stderr = process.communicate(timeout=10)
                results.append((process.returncode, stdout, stderr))
        finally:
            for process in processes:
                if process.poll() is None:
                    process.kill()
                    process.communicate()
        for code, _, stderr in results:
            self.assertEqual(code, 0, stderr)
        self.assertEqual(self.contents(), FILES)
        self.assertFalse(list(self.parent.glob(".vpsct-stage-*")))


if __name__ == "__main__":
    unittest.main()
