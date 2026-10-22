"""Run with: python3 -m unittest discover -s scripts -p 'test_pocket_connect.py'."""
import importlib.util
import json
from pathlib import Path
import stat
import tempfile
import tomllib
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('pocket_connect', Path(__file__).with_name('pocket-connect.py'))
connect = importlib.util.module_from_spec(spec)
spec.loader.exec_module(connect)


class ConfigTests(unittest.TestCase):
    def test_codex_preserves_other_settings_and_safe_renewal(self):
        original = '# My config\nmodel = "example"\n[mcp_servers.other]\nurl = "https://other.test/mcp"\n'
        result = connect.updated_config(original, 'codex', 'pocket_core', 'https://core.test/mcp', 'secret')
        data = tomllib.loads(result)
        self.assertEqual(data['model'], 'example')
        self.assertEqual(data['mcp_servers']['other'], {'url': 'https://other.test/mcp'})
        self.assertEqual(data['mcp_servers']['pocket_core']['http_headers'], {'Authorization': 'Bearer secret'})
        renewed = connect.updated_config(result, 'codex', 'pocket_core', 'https://core.test/mcp', 'new-secret', True)
        self.assertEqual(renewed.count('[mcp_servers.pocket_core]'), 1)
        self.assertIn('# My config', renewed)
        self.assertEqual(tomllib.loads(renewed)['mcp_servers']['pocket_core']['http_headers']['Authorization'], 'Bearer new-secret')

    def test_existing_connections_need_explicit_replacement(self):
        for client, original in [('codex', '[mcp_servers.pocket_core]\nurl="https://other.test"'),
                                 ('claude', '{"mcpServers":{"pocket_core":{"url":"https://other.test"}}}')]:
            with self.assertRaises(ValueError):
                connect.updated_config(original, client, 'pocket_core', 'https://core.test/mcp', 'secret')
        with self.assertRaises(ValueError):
            connect.updated_config('[mcp_servers.pocket_core]\nurl="https://other.test"', 'codex', 'pocket_core', 'https://core.test/mcp', 'secret', True)

    def test_claude_preserves_user_settings(self):
        original = {'projects': {'/my/project': {'allowedTools': ['Read']}}, 'mcpServers': {'other': {'command': 'example'}}}
        result = json.loads(connect.updated_config(json.dumps(original), 'claude', 'pocket_core', 'https://core.test/mcp', 'secret'))
        self.assertEqual(result['projects'], original['projects'])
        self.assertEqual(result['mcpServers']['other'], original['mcpServers']['other'])
        self.assertEqual(result['mcpServers']['pocket_core'], {'type': 'http', 'url': 'https://core.test/mcp', 'headers': {'Authorization': 'Bearer secret'}})

    def test_private_atomic_write_and_concurrent_edit(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.toml'
            connect.write_config(path, '', 'new-config')
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            with self.assertRaises(ValueError):
                connect.write_config(path, '', 'should-not-write')
            self.assertEqual(path.read_text(), 'new-config')
            link = Path(directory) / 'link'
            link.symlink_to(path)
            with self.assertRaises(ValueError):
                connect.read_config(link)

    def test_url_security(self):
        for url in ['http://core.test', 'https://name:password@core.test', 'https://core.test?token=secret', 'https://core.test/#fragment', 'https://core.test/extra', 'https://core.test\n']:
            with self.assertRaises(ValueError):
                connect.core_url(url)
        self.assertEqual(connect.core_url('https://core.test/'), 'https://core.test')
        self.assertEqual(connect.core_url('http://127.0.0.1:1234'), 'http://127.0.0.1:1234')
        self.assertIsNone(connect.NoRedirect().redirect_request(None, None, 302, '', {}, 'https://evil.test'))

    def test_config_preflight_prevents_unnecessary_pairing(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.toml'
            path.write_text('[mcp_servers.pocket_core]\nurl="https://other.test"')
            with patch.object(connect, 'post') as post:
                with self.assertRaises(ValueError):
                    connect.main(['--client', 'codex', '--url', 'https://core.test', '--config', str(path)])
                post.assert_not_called()


if __name__ == '__main__':
    unittest.main()
