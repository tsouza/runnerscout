import subprocess
import tempfile
import unittest
from pathlib import Path

from prepare_release import (
    bump_chart_version,
    categorize,
    commit_subjects_since,
    latest_tag,
    next_version,
    prepend_changelog,
    render_changelog_section,
)


def git_repo(tmp: Path) -> Path:
    subprocess.run(['git', 'init', '--quiet'], cwd=tmp, check=True)
    subprocess.run(['git', 'config', 'user.email', 'test@example.com'], cwd=tmp, check=True)
    subprocess.run(['git', 'config', 'user.name', 'test'], cwd=tmp, check=True)
    return tmp


def commit(repo: Path, message: str, tag: str = None) -> None:
    (repo / 'f').write_text(message)
    subprocess.run(['git', 'add', '.'], cwd=repo, check=True)
    subprocess.run(['git', 'commit', '--quiet', '-m', message], cwd=repo, check=True)
    if tag:
        subprocess.run(['git', 'tag', tag], cwd=repo, check=True)


class LatestTag(unittest.TestCase):
    def test_picks_highest_semver_not_lexical_or_creation_order(self):
        with tempfile.TemporaryDirectory() as raw:
            repo = git_repo(Path(raw))
            commit(repo, 'a', tag='v1.9.0')
            commit(repo, 'b', tag='v1.10.0')
            commit(repo, 'c', tag='v1.2.0')
            self.assertEqual(latest_tag(repo), 'v1.10.0')

    def test_none_when_no_tags_exist(self):
        with tempfile.TemporaryDirectory() as raw:
            repo = git_repo(Path(raw))
            commit(repo, 'a')
            self.assertIsNone(latest_tag(repo))

    def test_ignores_tags_that_are_not_bare_semver(self):
        with tempfile.TemporaryDirectory() as raw:
            repo = git_repo(Path(raw))
            commit(repo, 'a', tag='chart-v1.0.0')
            commit(repo, 'b', tag='v2.0.0-rc1')
            commit(repo, 'c', tag='v1.5.0')
            self.assertEqual(latest_tag(repo), 'v1.5.0')


class NextVersion(unittest.TestCase):
    def test_patch_minor_major_bumps(self):
        self.assertEqual(next_version('v1.2.3', 'patch'), 'v1.2.4')
        self.assertEqual(next_version('v1.2.3', 'minor'), 'v1.3.0')
        self.assertEqual(next_version('v1.2.3', 'major'), 'v2.0.0')

    def test_no_prior_tag_seeds_v0_1_0_unless_major_requested(self):
        self.assertEqual(next_version(None, 'patch'), 'v0.1.0')
        self.assertEqual(next_version(None, 'minor'), 'v0.1.0')
        self.assertEqual(next_version(None, 'major'), 'v1.0.0')

    def test_unknown_bump_level_rejected(self):
        with self.assertRaises(ValueError):
            next_version('v1.0.0', 'sideways')


class ChangelogCategorization(unittest.TestCase):
    def test_prefixes_map_to_expected_sections(self):
        sections = categorize([
            'fix: stop a hang',
            'mitigate: space out requests',
            'spec: add a new model',
            'docs: update releases.md',
            'chore: bump a dependency',
            'refactor(operator): extract a helper',
            'no colon here at all',
        ])
        self.assertEqual(sections['Fixed'], ['stop a hang', 'space out requests'])
        self.assertEqual(sections['Added'], ['add a new model'])
        self.assertEqual(sections['Documentation'], ['update releases.md'])
        self.assertEqual(sections['Chores'], ['bump a dependency'])
        self.assertEqual(sections['Changed'], ['extract a helper'])
        self.assertEqual(sections['Other'], ['no colon here at all'])

    def test_render_orders_sections_and_drops_empty_ones(self):
        rendered = render_changelog_section('v1.2.3', {'Fixed': ['a bug'], 'Added': [], 'Other': []})
        self.assertEqual(rendered, '## v1.2.3\n\n### Fixed\n- a bug\n')

    def test_render_with_nothing_says_so_rather_than_an_empty_section(self):
        rendered = render_changelog_section('v1.2.3', {name: [] for name in ['Added', 'Fixed']})
        self.assertIn('No changes recorded.', rendered)


class ChangelogFile(unittest.TestCase):
    def test_prepends_new_section_above_existing_content(self):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / 'CHANGELOG.md'
            path.write_text('# Changelog\n\n## v1.0.0\n\n### Fixed\n- old thing\n')
            prepend_changelog('## v1.1.0\n\n### Added\n- new thing\n', path)
            text = path.read_text()
            self.assertTrue(text.startswith('# Changelog\n\n## v1.1.0'))
            self.assertIn('## v1.0.0', text)
            self.assertLess(text.index('v1.1.0'), text.index('v1.0.0'))

    def test_creates_file_with_header_when_absent(self):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / 'CHANGELOG.md'
            prepend_changelog('## v0.1.0\n\nNo changes recorded.\n', path)
            self.assertTrue(path.read_text().startswith('# Changelog\n\n## v0.1.0'))


CHART_FIXTURE = (
    'apiVersion: v2\nname: runnerscout\nversion: 0.1.0-dev.1\n'
    'appVersion: development\ndescription: x\n'
    'annotations:\n  artifacthub.io/images: |\n'
    '    - name: runnerscout\n      image: ghcr.io/tsouza/runnerscout:v0.1.0-dev.1\n'
)


class ChartVersionBump(unittest.TestCase):
    def test_updates_all_three_fields_only(self):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / 'Chart.yaml'
            path.write_text(CHART_FIXTURE)
            bump_chart_version('v1.2.3', path)
            text = path.read_text()
            self.assertIn('version: 1.2.3\n', text)
            self.assertIn('appVersion: 1.2.3\n', text)
            self.assertIn('image: ghcr.io/tsouza/runnerscout:v1.2.3\n', text)
            self.assertIn('description: x\n', text)  # untouched lines survive

    def test_missing_fields_raise_rather_than_silently_no_op(self):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / 'Chart.yaml'
            path.write_text('apiVersion: v2\nname: runnerscout\n')
            with self.assertRaises(ValueError):
                bump_chart_version('v1.2.3', path)

    def test_missing_artifacthub_image_line_raises(self):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / 'Chart.yaml'
            path.write_text('apiVersion: v2\nname: runnerscout\nversion: 0.1.0\nappVersion: development\n')
            with self.assertRaises(ValueError):
                bump_chart_version('v1.2.3', path)


class CommitSubjectsSince(unittest.TestCase):
    def test_returns_only_commits_after_the_given_tag(self):
        with tempfile.TemporaryDirectory() as raw:
            repo = git_repo(Path(raw))
            commit(repo, 'fix: old one', tag='v1.0.0')
            commit(repo, 'fix: new one')
            commit(repo, 'docs: another new one')
            self.assertEqual(commit_subjects_since('v1.0.0', repo), ['docs: another new one', 'fix: new one'])

    def test_no_tag_returns_full_history(self):
        with tempfile.TemporaryDirectory() as raw:
            repo = git_repo(Path(raw))
            commit(repo, 'fix: only one')
            self.assertEqual(commit_subjects_since(None, repo), ['fix: only one'])
