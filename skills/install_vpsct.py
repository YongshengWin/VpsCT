#!/usr/bin/env python3
"""Install the VpsCT skill embedded by the serving control-plane version.

No network requests, credentials, sudo, or non-standard Python dependencies.
The one-line shell entrypoint targets macOS/Linux with Python 3.9 or newer.
"""

import argparse
import base64
import binascii
import fcntl
import hashlib
import io
import os
from pathlib import Path
import re
import shutil
import stat
import sys
import tempfile
import zipfile

BUNDLE_BASE64 = "__VPSCT_SKILL_ZIP_BASE64__"
BUNDLE_SHA256 = "__VPSCT_SKILL_ZIP_SHA256__"
MAX_ZIP_BYTES = 4 << 20
MAX_FILE_BYTES = 2 << 20
MAX_TOTAL_BYTES = 4 << 20
ALLOWED_FILES = frozenset(("SKILL.md", "scripts/vpsct.py", "references/api.md"))
ALLOWED_DIRS = frozenset(("scripts", "references"))


class InstallError(Exception):
    pass


def bundle_files(encoded, expected_sha):
    if not re.fullmatch(r"[0-9a-f]{64}", expected_sha):
        raise InstallError("安装器未包含有效的发行包 SHA256")
    if len(encoded) > ((MAX_ZIP_BYTES + 2) // 3) * 4:
        raise InstallError("技能包超过大小限制")
    try:
        archive_bytes = base64.b64decode(encoded, validate=True)
    except (binascii.Error, ValueError) as exc:
        raise InstallError("技能包 base64 无效") from exc
    if len(archive_bytes) > MAX_ZIP_BYTES:
        raise InstallError("技能包超过大小限制")
    if hashlib.sha256(archive_bytes).hexdigest() != expected_sha:
        raise InstallError("技能包 SHA256 校验失败")
    files = {}
    try:
        with zipfile.ZipFile(io.BytesIO(archive_bytes)) as archive:
            entries = archive.infolist()
            if len(entries) != len(ALLOWED_FILES):
                raise InstallError("技能包必须恰好包含三个产品文件")
            total = 0
            for entry in entries:
                name = entry.filename
                relative = name[len("vpsct/"):] if name.startswith("vpsct/") else name
                mode = stat.S_IFMT(entry.external_attr >> 16)
                if (name != "vpsct/" + relative or relative not in ALLOWED_FILES
                        or relative in files or entry.is_dir()
                        or mode not in (0, stat.S_IFREG) or entry.flag_bits & 1
                        or entry.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED)):
                    raise InstallError("技能包包含重复、路径越界、链接或不支持的文件")
                total += entry.file_size
                if entry.file_size > MAX_FILE_BYTES or total > MAX_TOTAL_BYTES:
                    raise InstallError("技能包解压大小超过限制")
                with archive.open(entry) as stream:
                    data = stream.read(MAX_FILE_BYTES + 1)
                if len(data) != entry.file_size or len(data) > MAX_FILE_BYTES:
                    raise InstallError("技能包文件大小不一致")
                files[relative] = data
    except (zipfile.BadZipFile, OSError, ValueError, RuntimeError, EOFError) as exc:
        raise InstallError("技能包 ZIP 结构或校验无效") from exc
    if set(files) != ALLOWED_FILES or not skill_identity(files["SKILL.md"]):
        raise InstallError("技能包不是完整的 VpsCT skill")
    return files


def skill_identity(data):
    try:
        text = data.decode("utf-8")
    except UnicodeError:
        return False
    if not text.startswith("---\n"):
        return False
    frontmatter, separator, _ = text[4:].partition("\n---")
    return bool(separator and re.search(r"^name:\s*(?:vpsct|\"vpsct\"|'vpsct')\s*$", frontmatter, re.M))


def destination(client, directory):
    if directory:
        parent = Path(directory).expanduser()
    elif client == "codex":
        codex_root = os.environ.get("CODEX_HOME")
        parent = (Path(codex_root).expanduser() if codex_root else Path.home() / ".codex") / "skills"
    else:
        parent = Path.home() / ".claude" / "skills"
    return Path(os.path.abspath(parent))


def open_parent(parent):
    """Create/open each component without following a filesystem symlink."""
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    descriptor = os.open(parent.anchor, flags)
    try:
        for part in parent.parts[1:]:
            try:
                next_descriptor = os.open(part, flags, dir_fd=descriptor)
            except FileNotFoundError:
                try:
                    os.mkdir(part, mode=0o700, dir_fd=descriptor)
                except FileExistsError:
                    pass
                next_descriptor = os.open(part, flags, dir_fd=descriptor)
            os.close(descriptor)
            descriptor = next_descriptor
        return descriptor
    except OSError as exc:
        os.close(descriptor)
        raise InstallError("目标父目录不能包含符号链接或非目录组件") from exc


def existing_files(target):
    """Only a complete, plain three-file VpsCT tree is eligible for update."""
    try:
        metadata = target.lstat()
    except FileNotFoundError:
        return None
    if not stat.S_ISDIR(metadata.st_mode):
        raise InstallError("vpsct 目标已存在且不是普通目录；不会覆盖文件或符号链接")
    files = {}
    directories = set()
    for root, subdirs, filenames in os.walk(target, followlinks=False):
        root = Path(root)
        for name in subdirs:
            entry = root / name
            relative = entry.relative_to(target).as_posix()
            if not stat.S_ISDIR(entry.lstat().st_mode) or relative not in ALLOWED_DIRS:
                raise InstallError("现有目录含链接或非产品内容；不会覆盖，请先手动备份或选择 --dir")
            directories.add(relative)
        for name in filenames:
            entry = root / name
            relative = entry.relative_to(target).as_posix()
            if relative not in ALLOWED_FILES or not stat.S_ISREG(entry.lstat().st_mode):
                raise InstallError("现有目录含链接或非产品文件；不会覆盖，请先手动备份或选择 --dir")
            descriptor = os.open(entry, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            try:
                metadata = os.fstat(descriptor)
                if not stat.S_ISREG(metadata.st_mode) or relative not in ALLOWED_FILES or metadata.st_size > MAX_FILE_BYTES:
                    raise InstallError("现有目录含非产品文件；不会覆盖，请先手动备份或选择 --dir")
                with os.fdopen(descriptor, "rb", closefd=False) as stream:
                    files[relative] = stream.read(MAX_FILE_BYTES + 1)
            finally:
                os.close(descriptor)
    if set(files) != ALLOWED_FILES or directories != ALLOWED_DIRS or not skill_identity(files["SKILL.md"]):
        raise InstallError("现有目录不是完整 VpsCT skill；不会覆盖，请先手动备份或选择 --dir")
    return files


def install(files, parent, update=False):
    parent_fd = open_parent(parent)
    lock_fd = None
    staging = None
    preserve_backup = False
    try:
        try:
            lock_fd = os.open(".vpsct-install.lock", os.O_CREAT | os.O_EXCL | os.O_RDWR | os.O_NOFOLLOW,
                              0o600, dir_fd=parent_fd)
        except FileExistsError:
            # Separate atomic creation from opening an existing lock. On
            # macOS concurrent O_CREAT|O_NOFOLLOW may spuriously return ENOENT.
            lock_fd = os.open(".vpsct-install.lock", os.O_RDWR | os.O_NOFOLLOW, dir_fd=parent_fd)
        if not stat.S_ISREG(os.fstat(lock_fd).st_mode):
            raise InstallError("安装锁不是普通文件")
        fcntl.flock(lock_fd, fcntl.LOCK_EX)
        target = parent / "vpsct"
        previous = existing_files(target)
        if previous == files:
            return "已安装相同版本，无需更改", target
        if previous is not None and not update:
            raise InstallError("已有不同版本或修改过的 VpsCT skill；如需替换，请显式传入 --update")
        staging = Path(tempfile.mkdtemp(prefix=".vpsct-stage-", dir=parent))
        staged_target = staging / "vpsct"
        staged_target.mkdir(mode=0o700)
        for directory in sorted(ALLOWED_DIRS):
            (staged_target / directory).mkdir(mode=0o700)
        for name, content in files.items():
            descriptor = os.open(staged_target / name, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
        # Rename only after every new file has been written and checked. The
        # lock serializes cooperating installers; old files stay in staging
        # until publication succeeds, and remain recoverable on rollback error.
        backup = staging / "previous-vpsct"
        try:
            if previous is not None:
                os.rename("vpsct", backup, src_dir_fd=parent_fd)
            if os.path.lexists(target):
                raise InstallError("目标在安装中发生变化，已停止")
            os.rename(staged_target, "vpsct", dst_dir_fd=parent_fd)
        except BaseException as exc:
            # Interrupts during the two renames must preserve the old tree too.
            # Inspect the backup rather than a flag: a signal can arrive just
            # after rename completes and before Python assigns a flag.
            if previous is not None and os.path.lexists(backup):
                try:
                    if os.path.lexists(target):
                        raise InstallError("目标被其他进程创建")
                    os.rename(backup, "vpsct", dst_dir_fd=parent_fd)
                except BaseException:
                    preserve_backup = True
                    raise InstallError("安装未完成，原 skill 完整保留于 " + str(backup) + "，请恢复后再试") from exc
            raise InstallError("安装未完成；原 skill 已恢复，未替换为新版本" if previous is not None
                               else "安装未完成，未发布新的 skill") from exc
        return "VpsCT skill 已更新" if previous is not None else "VpsCT skill 已安装", target
    finally:
        if staging is not None and not preserve_backup:
            shutil.rmtree(staging)
        if lock_fd is not None:
            os.close(lock_fd)
        os.close(parent_fd)


def main(argv=None):
    parser = argparse.ArgumentParser(description="Python 3.9+ 在 macOS/Linux 安装自带 VpsCT skill 包，不联网、不读取 API key")
    parser.add_argument("--client", choices=("codex", "claude"), default="codex", help="客户端，默认 codex")
    parser.add_argument("--dir", help="自定义 skill 父目录；最终目录为其下的 vpsct/")
    parser.add_argument("--update", action="store_true", help="显式替换现有完整 VpsCT skill；不覆盖任意目录或链接")
    args = parser.parse_args(argv)
    try:
        files = bundle_files(BUNDLE_BASE64, BUNDLE_SHA256)
        message, target = install(files, destination(args.client, args.dir), args.update)
        print(message + "：" + str(target))
        print("请重新加载客户端的技能，并通过秘密管理工具设置 VPSCT_URL 与 VPSCT_API_KEY。")
        return 0
    except (InstallError, OSError, ValueError) as exc:
        message = str(exc) if isinstance(exc, InstallError) else "文件系统操作失败；请检查目录权限，勿使用 sudo"
        print(message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
