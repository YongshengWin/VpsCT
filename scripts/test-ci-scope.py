#!/usr/bin/env python3
"""Exercise the advisory scope report against real Git histories."""
import os
from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest

scope = runpy.run_path(str(Path(__file__).with_name('ci-scope.py')))


class ScopeTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.addCleanup(os.chdir, os.getcwd())
        os.chdir(tmp.name)
        self.git('init', '-q', '-b', 'base')
        self.base = self.commit('README.md', 'base')

    def git(self, *args):
        return subprocess.check_output(['git', '-c', 'user.name=CI', '-c', 'user.email=ci@example.invalid',
                                        '-c', 'commit.gpgsign=false', *args], text=True).strip()

    def save(self):
        self.git('commit', '-qm', 'fixture')
        return self.git('rev-parse', 'HEAD')

    def commit(self, path, data='changed'):
        p = Path(path)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(data)
        self.git('add', '--', path)
        return self.save()

    def report(self, head, base=None, *, event='pull_request', release='false'):
        return scope['report'](event, self.base if base is None else base, head, release)

    def test_docs_only_and_deleted_doc(self):
        self.assertIn('**docs**', self.report(self.commit('docs/network.md')))
        self.git('rm', '-q', 'README.md')
        self.assertIn('**docs**', self.report(self.save()))

    def test_frontend_and_docs_union(self):
        self.commit('README.md')
        report = self.report(self.commit('scripts/security-browser.mjs'))
        for expected in ('**ui**', '同时检查文档', 'fuzz', '浏览器', '完整安全门禁'):
            self.assertIn(expected, report)

    def test_whole_branch_not_last_commit(self):
        self.commit('internal/store/schema.go')
        self.assertIn('**full**', self.report(self.commit('web/src/components/logo.tsx')))

    def test_base_only_changes_excluded(self):
        self.git('switch', '-qc', 'feature')
        head = self.commit('README.md')
        self.git('switch', '-q', 'base')
        self.assertIn('**docs**', self.report(head, self.commit('go.mod')))

    def test_renamed_sensitive_path_is_not_docs_only(self):
        base = self.commit('script.sh')
        self.git('mv', 'script.sh', 'CHANGELOG.md')
        report = self.report(self.save(), base)
        for expected in ('**full**', 'script.sh', 'CHANGELOG.md'):
            self.assertIn(expected, report)

    def test_unknown_security_and_control_paths(self):
        for path in ('.github/workflows/ci.yml', 'scripts/ci-scope.py', 'skills/vpsct/SKILL.md',
                     'web/src/pages/servers.tsx', 'web/src/lib/auth.tsx', 'web/package-lock.json', 'unknown'):
            base = self.git('rev-parse', 'HEAD')
            with self.subTest(path=path):
                self.assertIn('**full**', self.report(self.commit(path), base))

    def test_mode_and_symlink(self):
        self.git('update-index', '--chmod=+x', 'README.md')
        self.assertIn('**full**', self.report(self.save()))
        Path('README.md').unlink()
        Path('README.md').symlink_to('target')
        self.git('add', '--', 'README.md')
        self.assertIn('**full**', self.report(self.save()))

    def test_fallbacks_and_release(self):
        head = self.commit('README.md')
        for report in (self.report(head, base=''), self.report('0' * 40), self.report(self.base),
                       self.report(head, event='workflow_dispatch'), self.report(head, release='true')):
            self.assertIn('**full**', report)

    def test_hostile_filename_only_printed_as_data(self):
        report = self.report(self.commit('unexpected\n<img>'))
        self.assertIn('**full**', report)
        self.assertNotIn('<img>', report)
        self.assertIn('\\n&lt;img&gt;', report)


if __name__ == '__main__':
    unittest.main()
